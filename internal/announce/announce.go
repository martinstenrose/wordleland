// Package announce builds what the Signal bridge posts back into the group.
//
// There are three announcements, each a closure of the same shape — given the
// current time, work out whether there is anything to say and say it — and
// each restart-safe through a row it writes only after a send succeeds:
//
//   - NewDaily posts the day's recap, once every active player has filed or
//     just after midnight, whichever comes first.
//   - NewWeekly posts the Monday-to-Sunday week's recap, right after
//     Sunday's, by the same rule.
//   - NewMonthly posts the month's winner, right after the recaps of its
//     last day, by the same rule again.
//
// All three close the same way, and when they close together — a month
// ending on a Sunday — they go out smallest first: the day, the week, the
// month.
//
// It sits above internal/store, internal/stats and internal/i18n — none of
// which the bridge package itself depends on — so bridge stays able to
// receive and file results without knowing what a "month" or a
// translation is. cmd/wordleland/serve.go wires these against the real
// database and a bridge.Sender to build the bridge.Announcer it passes to
// bridge.New, the same way it wires ingest.Apply into a bridge.Deliverer.
package announce

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// NewMonthly returns the month's closure, called after every live message
// and by the run just after midnight.
//
// It reports what happened by returning nil for "nothing to do" — already
// announced, or nobody posted a scorable result that month — and a non-nil
// error only for a genuine failure: a store read, or the send, going wrong.
// The caller (the bridge) logs an error and tries again on the next live
// message; it never treats "nothing to do" as one.
//
// dailyGoesFirst and weeklyGoesFirst say which other announcements are
// configured. The month waits for the recaps of its last day that are, so
// the group reads the day and the week before the month they closed.
//
// send is a bridge.Sender by value, not by import: this package has no
// need to know the bridge exists, only that something can post text to the
// group, which keeps the dependency running one way.
func NewMonthly(db *sql.DB, cats i18n.Catalogues, locale string, dailyGoesFirst, weeklyGoesFirst bool,
	send func(ctx context.Context, text string) error) func(context.Context, time.Time) error {

	t := i18n.NewTranslator(cats, locale)
	// The midnight run and a live result can arrive together. Serialize the
	// whole check/send/record sequence so both cannot observe a missing record
	// and post the same announcement.
	var mu sync.Mutex

	return func(ctx context.Context, now time.Time) error {
		mu.Lock()
		defer mu.Unlock()

		players, err := store.ListPlayers(ctx, db)
		if err != nil {
			return fmt.Errorf("list players: %w", err)
		}
		results, err := store.ResultsForBoard(ctx, db)
		if err != nil {
			return fmt.Errorf("read results: %w", err)
		}

		year, month, due, err := monthlyDue(ctx, db, players, results, now, dailyGoesFirst, weeklyGoesFirst)
		if err != nil || !due {
			return err
		}

		// Scored as of the month's end even when posted on its last evening:
		// it is only posted early once every active player is in.
		end := time.Date(year, month+1, 1, 0, 0, 0, 0, now.Location())
		months := stats.ComputeMonths(players, results, stats.DefaultOptions(end))
		m, found := monthByKey(months, year, month)
		if !found {
			// Nobody posted anything at all that month — the group was
			// silent, or the bridge only just started watching it. Nothing
			// to say, and nothing to hold against a future month either:
			// this is not recorded as announced, so if results for that
			// month appear later (a backfill, a correction), the next live
			// message picks it up rather than staying silent forever.
			return nil
		}

		line, ok := winnerLine(t, m)
		if !ok {
			// Months no longer have a minimum-games threshold, so this is
			// not currently reachable — every player with a posted result
			// gets a scored month under the fixed defaults this package
			// uses. Kept as a defensive no-op rather than a panic: silence,
			// not an unprompted "nobody qualified" message that would read
			// as the bot scolding a quiet month. Also not recorded, for the
			// same reason as above.
			return nil
		}

		if err := send(ctx, line); err != nil {
			return fmt.Errorf("post to signal: %w", err)
		}

		if err := store.RecordMonthAnnouncement(ctx, db, year, month); err != nil {
			// The message is already out. Losing this write risks a
			// duplicate post next time rather than losing the
			// announcement, which is the safer of the two failures.
			return fmt.Errorf("record that %d-%d was announced: %w", year, month, err)
		}
		return nil
	}
}

// monthlyDue picks the month this run should announce, if any: the current
// month on its last day once every active player has played it, and
// otherwise the month before now's.
//
// The month before stays due for the whole of the current month, so a live
// result catches up a midnight the app was down for however late it comes —
// a month's result is not stale on the 10th the way a day's is.
//
// The waits for the last day's recaps last only while those can still come,
// which is until the day after it: from then on dailyDue no longer looks
// back far enough to post the day, and the week stops waiting for the day.
func monthlyDue(ctx context.Context, db *sql.DB, players []store.Player, results []store.BoardResult,
	now time.Time, dailyGoesFirst, weeklyGoesFirst bool) (int, time.Month, bool, error) {

	current := wordle.PuzzleForDate(now)
	year, month := previousMonth(now)
	if lastDayOfMonth(now) && fullHouse(players, results, current) {
		year, month = now.Year(), now.Month()
	} else if lastDayOfMonth(now) {
		return 0, 0, false, nil
	}

	done, err := store.MonthAnnounced(ctx, db, year, month)
	if err != nil {
		return 0, 0, false, fmt.Errorf("check whether %d-%d was announced: %w", year, month, err)
	}
	if done {
		return 0, 0, false, nil
	}

	last := wordle.PuzzleForDate(time.Date(year, month+1, 0, 0, 0, 0, 0, now.Location()))
	if current > last+1 {
		return year, month, true, nil
	}
	if dailyGoesFirst && len(stats.ComputeToday(players, results, last).Filed) > 0 {
		posted, err := store.DayAnnounced(ctx, db, last)
		if err != nil {
			return 0, 0, false, fmt.Errorf("check whether puzzle %d was announced: %w", last, err)
		}
		if !posted {
			return 0, 0, false, nil
		}
	}
	if first := stats.WeekOf(last); weeklyGoesFirst && first+6 == last && weekContested(players, results, first) {
		posted, err := store.WeekAnnounced(ctx, db, first)
		if err != nil {
			return 0, 0, false, fmt.Errorf("check whether the week from puzzle %d was announced: %w", first, err)
		}
		if !posted {
			return 0, 0, false, nil
		}
	}
	return year, month, true, nil
}

// previousMonth is the month just before now's.
func previousMonth(now time.Time) (int, time.Month) {
	year, month := now.Year(), now.Month()
	if month == time.January {
		return year - 1, time.December
	}
	return year, month - 1
}

func monthByKey(months []stats.Month, year int, month time.Month) (stats.Month, bool) {
	for _, m := range months {
		if m.Year == year && m.Month == month {
			return m, true
		}
	}
	return stats.Month{}, false
}

// winnerLine mirrors the branching internal/web's months page uses to pick
// a tie, a clear margin or an "alone at the top" sentence for a closed
// month — closed being the only case this package ever sees, so there is
// no "running" branch to carry here. Kept as its own small copy rather
// than a shared function: the two sides format for a browser and for a
// chat message respectively, and the branching itself is stable enough
// that "read both before changing either" costs less than a type both
// packages would have to agree on.
//
// The wording itself has its own "announce.line.*" keys rather than
// reusing the board's "months.line.*": the trophy and the winner's
// average read naturally in a chat announcement, but would duplicate the
// average the board already shows as one of the four stat figures beside
// the winner's name, and an emoji sitting in the page's prose would look
// out of place there. See internal/i18n's package doc for the keys that
// do stay shared between the two surfaces.
func winnerLine(t i18n.Translator, m stats.Month) (string, bool) {
	if len(m.Winners) == 0 {
		return "", false
	}
	w := m.Winners[0]
	names := joinNames(t, playerNames(m.Winners))
	avg := t.Decimal(*w.Average, 2)

	switch {
	case len(m.Winners) > 1:
		return "🏆 " + names + ": " + t.T("announce.line.tie", avg), true
	case m.Margin != nil:
		return "🏆 " + t.T("announce.line.margin", names, monthLabel(t, m),
			avg, t.Decimal(*m.Margin, 2), m.Days), true
	default:
		return "🏆 " + t.T("announce.line.alone", names, avg, m.Days), true
	}
}

func monthLabel(t i18n.Translator, m stats.Month) string {
	return t.T("month."+strconv.Itoa(int(m.Month))) + " " + strconv.Itoa(m.Year)
}

func playerNames(ps []stats.MonthPlayer) []string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

// joinNames renders a tie as every name, because a tie is the result.
func joinNames(t i18n.Translator, names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	out := ""
	for i, n := range names {
		switch {
		case i == 0:
			out = n
		case i == len(names)-1:
			out += " " + t.T("list.and") + " " + n
		default:
			out += ", " + n
		}
	}
	return out
}
