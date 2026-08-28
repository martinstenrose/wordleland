// Package announce builds the Signal bridge's Announcer: the closure that,
// called at noon on the first of a month or after a later live message,
// posts the previous calendar month's winner if nobody has yet done so.
//
// It sits above internal/store, internal/stats and internal/i18n — none of
// which the bridge package itself depends on — so bridge stays able to
// receive and file results without knowing what a "month" or a
// translation is. cmd/wordleland/serve.go wires this package's New against
// the real database and a bridge.Sender to build the bridge.Announcer it
// passes to bridge.New, the same way it wires ingest.Apply into a
// bridge.Deliverer.
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
)

// New returns the closure the bridge calls after every live message.
//
// It reports what happened by returning nil for "nothing to do" — already
// announced, or nobody reached the minimum games — and a non-nil error only
// for a genuine failure: a store read, or the send, going wrong. The
// caller (the bridge) logs an error and tries again on the next live
// message; it never treats "nothing to do" as one.
//
// send is a bridge.Sender by value, not by import: this package has no
// need to know the bridge exists, only that something can post text to the
// group, which keeps the dependency running one way.
func New(db *sql.DB, cats i18n.Catalogues, locale string,
	send func(ctx context.Context, text string) error) func(context.Context, time.Time) error {

	t := i18n.NewTranslator(cats, locale)
	// The noon scheduler and a live result can arrive together. Serialize the
	// whole check/send/record sequence so both cannot observe a missing record
	// and post the same announcement.
	var mu sync.Mutex

	return func(ctx context.Context, now time.Time) error {
		if !announcementDue(now) {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()

		year, month := previousMonth(now)

		done, err := store.MonthAnnounced(ctx, db, year, month)
		if err != nil {
			return fmt.Errorf("check whether %d-%d was announced: %w", year, month, err)
		}
		if done {
			return nil
		}

		players, err := store.ListPlayers(ctx, db)
		if err != nil {
			return fmt.Errorf("list players: %w", err)
		}
		results, err := store.ResultsForBoard(ctx, db)
		if err != nil {
			return fmt.Errorf("read results: %w", err)
		}

		months := stats.ComputeMonths(players, results, stats.DefaultOptions(now))
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
			// Nobody reached the minimum games. Silence, deliberately: an
			// unprompted "nobody qualified this month" reads as the bot
			// scolding a group that had a quiet month, and the board
			// already says so for anyone who looks. Also not recorded, for
			// the same reason as above.
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

// announcementDue keeps the whole morning of the first clear for late
// closing-day results. On later days it remains true so a live result can
// catch up a noon run missed while the app was offline.
func announcementDue(now time.Time) bool {
	noon := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, now.Location())
	return !now.Before(noon)
}

// previousMonth is the month just before now's. announcementDue guarantees
// that this is not selected until noon on the first day of the new month.
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

// winnerLine mirrors the switch internal/web's months page uses to render
// WinnerLine for a closed month — closed being the only case this package
// ever sees, so there is no "running" branch to carry here. Kept as its
// own small copy rather than a shared function: the two sides format for a
// browser and for a chat message respectively, and the branching itself
// — tie, clear margin, alone at the top — is stable enough that "read both
// before changing either" costs less than a type both packages would have
// to agree on.
func winnerLine(t i18n.Translator, m stats.Month) (string, bool) {
	if len(m.Winners) == 0 {
		return "", false
	}
	w := m.Winners[0]
	names := joinNames(m.Winners)

	switch {
	case len(m.Winners) > 1:
		return names + ": " + t.T("months.line.tie", formatAverage(*w.Average)), true
	case m.Margin != nil:
		return t.T("months.line.margin", names, monthLabel(t, m),
			formatAverage(*m.Margin), w.Games), true
	default:
		return t.T("months.line.alone", names, w.Games), true
	}
}

func formatAverage(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func monthLabel(t i18n.Translator, m stats.Month) string {
	return t.T("month."+strconv.Itoa(int(m.Month))) + " " + strconv.Itoa(m.Year)
}

// joinNames renders a tie as every name, because a tie is the result.
func joinNames(ps []stats.MonthPlayer) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
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
			out += " & " + n
		default:
			out += ", " + n
		}
	}
	return out
}
