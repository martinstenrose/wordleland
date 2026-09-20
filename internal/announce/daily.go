package announce

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// NewDaily returns the day's closure, called after every live message and by
// RunDaily just after midnight.
//
// It has the same contract as NewMonthly: nil for "nothing to do", an error
// only for a store read or a send going wrong, and a recorded row only after
// a message has actually landed.
//
// monthResultFollows says whether a monthly announcement is also configured.
// When it is, the recap for a day whose month has closed withholds the
// standing and points at noon instead of printing the month's result twelve
// hours before the 🏆 message does. When it is not, no other message will
// ever say it, so the recap says it itself.
func NewDaily(db *sql.DB, cats i18n.Catalogues, locale string, monthResultFollows bool,
	send func(ctx context.Context, text string) error) func(context.Context, time.Time) error {

	t := i18n.NewTranslator(cats, locale)
	// The midnight run and the last player's result can arrive together —
	// somebody filing at 23:59:59 is exactly when both fire — so the whole
	// check/send/record sequence is serialized, as it is for the month.
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

		puzzle, due, err := dailyDue(ctx, db, players, results, wordle.PuzzleForDate(now))
		if err != nil || !due {
			return err
		}
		date, err := wordle.DateForPuzzle(puzzle)
		if err != nil {
			return fmt.Errorf("date for puzzle %d: %w", puzzle, err)
		}

		// The month is read as it stands at now, so a recap posted just after
		// midnight on the first reports the month the day belonged to with
		// every one of its days concluded — which is what it is by then.
		months := stats.ComputeMonths(players, results, stats.DefaultOptions(now))
		text := dailyPost(t, stats.ComputeToday(players, results, puzzle), months, date,
			monthResultFollows)

		if err := send(ctx, text); err != nil {
			return fmt.Errorf("post to signal: %w", err)
		}
		if err := store.RecordDayAnnouncement(ctx, db, puzzle); err != nil {
			// Same trade as the month: the message is out, so losing this
			// write risks a duplicate rather than losing the recap.
			return fmt.Errorf("record that puzzle %d was announced: %w", puzzle, err)
		}
		return nil
	}
}

// SkipDailyBacklog marks the day before now as already announced, but only
// when no day ever has been.
//
// It is what stops the very first run from opening with yesterday's recap.
// The days already in the database happened before this deployment announced
// anything, so they are history, not a missed post — and the bot's first word
// in the group should be about the puzzle the group is currently playing.
//
// One row, not the whole history, because the daily check only ever looks one
// day back: marking yesterday is enough to make the backlog invisible to it.
// Called once at startup, before the scheduler's first check. On every later
// start the table has rows and this does nothing.
func SkipDailyBacklog(ctx context.Context, db *sql.DB, now time.Time) error {
	announced, err := store.AnyDayAnnounced(ctx, db)
	if err != nil {
		return err
	}
	if announced {
		return nil
	}
	previous := wordle.PuzzleForDate(now) - 1
	if err := store.RecordDayAnnouncement(ctx, db, previous); err != nil {
		return fmt.Errorf("set puzzle %d as the daily recap's starting point: %w", previous, err)
	}
	return nil
}

// dailyDue picks the puzzle this run should post about, if any.
//
// The day that has just closed is considered first. It can no longer change,
// and taking it first is what lets a live result catch up a midnight run
// missed to a restart — the same catch-up the monthly announcement gets from
// being checked after every message.
//
// Today is posted only once every active player has filed. That is the
// "whichever comes first" half of the rule: a full house ends the day early,
// and midnight ends it otherwise.
//
// Only ever one day back, deliberately. A longer outage leaves the days
// before yesterday unannounced rather than posting a week of recaps in a
// burst when the app returns, which is the noisier of the two failures and
// the harder one to undo.
func dailyDue(ctx context.Context, db *sql.DB, players []store.Player,
	results []store.BoardResult, current int) (int, bool, error) {

	closed := current - 1
	if len(stats.ComputeToday(players, results, closed).Filed) > 0 {
		done, err := store.DayAnnounced(ctx, db, closed)
		if err != nil {
			return 0, false, fmt.Errorf("check whether puzzle %d was announced: %w", closed, err)
		}
		if !done {
			return closed, true, nil
		}
	}

	// Nobody filed at all is not a full house: an empty group day has
	// nothing to report, and reporting it would be the bot talking to
	// itself. Left unrecorded, so a late result still gets its recap.
	today := stats.ComputeToday(players, results, current)
	if len(today.Filed) == 0 || len(today.Missing) > 0 {
		return 0, false, nil
	}
	done, err := store.DayAnnounced(ctx, db, current)
	if err != nil {
		return 0, false, fmt.Errorf("check whether puzzle %d was announced: %w", current, err)
	}
	return current, !done, nil
}

// dailyPost is the message: what the day was, who won it, and where the
// month stands. Three lines, because a chat message nobody scrolls is one
// that gets read.
func dailyPost(t i18n.Translator, day stats.Today, months []stats.Month, date time.Time,
	monthResultFollows bool) string {

	lines := []string{headLine(t, day), bestLine(t, day)}
	if month := monthLine(t, months, date, monthResultFollows); month != "" {
		lines = append(lines, month)
	}
	return strings.Join(lines, "\n")
}

// lastDayOfMonth reports whether date is its month's final day. Calendar
// arithmetic rather than a table of lengths, so February and leap years need
// no special case.
func lastDayOfMonth(date time.Time) bool {
	return date.AddDate(0, 0, 1).Month() != date.Month()
}

// headLine names the puzzle and says which way the day ended. Absentees are
// counted, not named: the count is the fact, and a list of names in a chat
// message reads as the bot calling people out.
func headLine(t i18n.Translator, day stats.Today) string {
	puzzle := i18n.Identifier(day.PuzzleNo)
	if len(day.Missing) == 0 {
		return "🏁 " + t.T("announce.daily.head.complete", puzzle)
	}
	return "🏁 " + t.T("announce.daily.head.closed", puzzle, day.FiledCount(), day.Expected())
}

// bestLine has three forms rather than a singular and a plural, because a
// pair takes a word of its own: "both" is wrong for three people and "all" is
// wrong for two. Not the catalogue's .one/.other plural mechanism either —
// that splits at one, and this splits at two.
func bestLine(t i18n.Translator, day stats.Today) string {
	if day.Best == nil {
		return "🥇 " + t.T("announce.daily.noneSolved")
	}
	names := joinNames(t, bestNames(day))
	switch {
	case day.BestShared == 2:
		return "🥇 " + t.T("announce.daily.bestPair", names, day.Best.Guesses)
	case day.BestShared > 2:
		return "🥇 " + t.T("announce.daily.bestMany", names, day.Best.Guesses)
	default:
		return "🥇 " + t.T("announce.daily.best", names, day.Best.Guesses)
	}
}

// bestNames is everyone who tied for the day's lowest solve. Today.Filed is
// already ordered best first and then by name, so these come out
// alphabetically without sorting again.
func bestNames(day stats.Today) []string {
	names := make([]string, 0, day.BestShared)
	for _, e := range day.Filed {
		if e.Solved && e.Guesses == day.Best.Guesses {
			names = append(names, e.Name)
		}
	}
	return names
}

// monthLine is the standing in the month the day belongs to — not the month
// now is in, which differs for a recap posted just after midnight on the
// first and would report an empty new month instead of the one that was
// actually played.
//
// It returns "" rather than a sentence when there is no month to report, and
// the caller leaves the line out. The day's own two lines still stand on
// their own, which is why this is a missing line rather than an error.
func monthLine(t i18n.Translator, months []stats.Month, date time.Time,
	monthResultFollows bool) string {

	m, ok := monthByKey(months, date.Year(), date.Month())
	if !ok || len(m.Winners) == 0 {
		return ""
	}

	label := t.T("month." + strconv.Itoa(int(m.Month)))

	// On the month's last day the standing is not a standing any more, it is
	// the result — and the 🏆 message at noon on the first is the one that
	// exists to deliver it. Printing it here would hand the group the winner,
	// the average and the margin before the message whose whole job that is.
	//
	// Keyed on the recapped day being the month's last rather than on the
	// month having closed by now, because both ways a last day can be
	// recapped give the result away: the 00:01 run after it, and the early
	// post on the day itself once every active player is in — by which point
	// nobody is left to change the figures.
	if monthResultFollows && lastDayOfMonth(date) {
		return "📊 " + t.T("announce.daily.month.wrapped", label)
	}

	leaders := joinNames(t, playerNames(m.Winners))
	avg := t.Decimal(*m.Winners[0].Average, 2)

	switch {
	case len(m.Winners) > 1:
		return "📊 " + t.T("announce.daily.month.tie", label, leaders, avg)
	case m.Margin != nil:
		return "📊 " + t.T("announce.daily.month.margin", label, leaders, avg,
			t.Decimal(*m.Margin, 2), joinNames(t, playerNames(runnersUp(m))))
	default:
		return "📊 " + t.T("announce.daily.month.alone", label, leaders, avg)
	}
}

// runnersUp is everyone sharing the next distinct average below the leader —
// the players Month.Margin measures the gap to. A slice for the same reason
// Winners is one: a shared second place is not one person, and naming one of
// them would invent a placing.
func runnersUp(m stats.Month) []stats.MonthPlayer {
	next := len(m.Winners)
	if next >= len(m.Ranked) {
		return nil
	}
	second := *m.Ranked[next].Average
	var out []stats.MonthPlayer
	for _, p := range m.Ranked[next:] {
		if *p.Average != second {
			break
		}
		out = append(out, p)
	}
	return out
}
