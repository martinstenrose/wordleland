package announce

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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

		text := dailyPost(t, newDayContext(players, results, puzzle, date, now, monthResultFollows))

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

// Thresholds for the recap's one line of colour. Each is set so that the
// line appears when there is something to say and stays away otherwise; a
// remark that fires every day is wallpaper.
const (
	// crowdSize is how many sharing the day's best turns a list of names
	// into a count. Three names read; seven do not.
	crowdSize = 4
	// leaderFromDay is the first day of a month on which a change of
	// leader is news. Before it the lead changes hands with every result.
	leaderFromDay = 5
	// dayDelta is how far the day's mean has to sit from the group's usual
	// before the day is called hard or easy: three quarters of a guess.
	dayDelta = 0.75
	// dayMinFiled is how many results a day needs before its mean says
	// anything about the puzzle rather than about who happened to play.
	dayMinFiled = 3
	// beatMargin is how far under their own average a player has to land
	// for it to be the day's surprise: a 3 from somebody averaging 4.5.
	beatMargin = 1.5
	// runMin is the shortest run of opening or closing the day worth
	// counting out loud.
	runMin = 3
)

// streakMilestones are the solved-streak lengths the recap remarks on: the
// early ones singly, then every fifty.
var streakMilestones = []int{10, 25, 50}

func isStreakMilestone(n int) bool {
	for _, m := range streakMilestones {
		if n == m {
			return true
		}
	}
	return n >= 100 && n%50 == 0
}

// dayContext is everything the message is composed from, computed once.
//
// Every figure but the month standing is read from results up to and
// including the recapped puzzle. At 00:01 somebody may already have posted
// the next day's result, and letting it into an average, a streak or a
// month's day count would make the recap of one day describe part of the
// next.
type dayContext struct {
	day  stats.Today
	date time.Time

	// months is the standing as it stands at now, for the 📊 line: a recap
	// posted just after midnight on the first reports the month the day
	// belonged to with every one of its days concluded.
	months []stats.Month
	// before and after bracket the recapped day: the month's standing at
	// the close of the day before, and with this day included.
	before, after []stats.Month

	// board is the group through the recapped day, for streaks. baseline
	// is the group going into it, for what a player usually scores.
	board, baseline stats.Board
	// history is every result before the recapped day; todays are the
	// day's own.
	history, todays []store.BoardResult
	habits          stats.PostingHabits

	monthResultFollows bool
}

func newDayContext(players []store.Player, results []store.BoardResult, puzzle int,
	date, now time.Time, monthResultFollows bool) dayContext {

	upTo := through(results, puzzle)
	prior := through(results, puzzle-1)
	asOfDay := stats.DefaultOptions(date)

	return dayContext{
		day:                stats.ComputeToday(players, upTo, puzzle),
		date:               date,
		months:             stats.ComputeMonths(players, results, stats.DefaultOptions(now)),
		before:             stats.ComputeMonths(players, prior, asOfDay),
		after:              stats.ComputeMonths(players, upTo, stats.DefaultOptions(now)),
		board:              stats.Compute(players, upTo, asOfDay),
		baseline:           stats.Compute(players, prior, asOfDay),
		history:            prior,
		todays:             upTo[len(prior):],
		habits:             stats.ComputePostingHabits(upTo, puzzle),
		monthResultFollows: monthResultFollows,
	}
}

// through keeps the results up to and including puzzle. Results arrive
// oldest first, so this is a prefix.
func through(results []store.BoardResult, puzzle int) []store.BoardResult {
	n := sort.Search(len(results), func(i int) bool { return results[i].PuzzleNo > puzzle })
	return results[:n]
}

// dailyPost is the message: what the day was, who won it, who opened and
// closed it, one thing worth remarking on, and where the month stands. Five
// lines at most, and usually fewer — the third and fourth appear only when
// there is something to say, because a chat message nobody scrolls is one
// that gets read.
func dailyPost(t i18n.Translator, d dayContext) string {
	lines := []string{headLine(t, d.day), bestLine(t, d.day)}
	for _, line := range []string{
		postedLine(t, d),
		spiceLine(t, d),
		monthLine(t, d.months, d.date, d.monthResultFollows),
	} {
		if line != "" {
			lines = append(lines, line)
		}
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

// bestLine names the day's best, or counts them.
//
// A first-guess solve is remarked on whoever did it, because it is the one
// score the group will ask about. Past that: everyone landing on the same
// score is a fact about the puzzle and is said as one; from crowdSize
// sharing the best, a count replaces the list, since seven names in a row
// are not read; and below that the three forms rather than a singular and
// a plural, because a pair takes a word of its own — "both" is wrong for
// three people and "all" is wrong for two. Not the catalogue's .one/.other
// plural mechanism either: that splits at one, and this splits at two.
func bestLine(t i18n.Translator, day stats.Today) string {
	if day.Best == nil {
		return "🥇 " + t.T("announce.daily.noneSolved")
	}
	names := joinNames(t, bestNames(day))
	switch {
	case day.Best.Guesses == 1:
		return "🥇 " + t.T("announce.daily.ace", names)
	case day.FiledCount() >= dayMinFiled && day.BestShared == day.FiledCount():
		return "🥇 " + t.T("announce.daily.bestAll", day.Best.Guesses)
	case day.BestShared >= crowdSize:
		return "🥇 " + t.T("announce.daily.bestCount", day.BestShared, day.FiledCount(), day.Best.Guesses)
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

// postedLine says who opened the day and who closed it, each with a remark
// when that is what they usually do. Empty when fewer than two results
// carry a posting time: then there is no order to report.
//
// Two whole sentences from the catalogue rather than a sentence plus a
// suffix, because where "as usual" goes differs between languages.
func postedLine(t i18n.Translator, d dayContext) string {
	if d.day.First == nil || d.day.Last == nil {
		return ""
	}
	first := habitSentence(t, "announce.daily.first", *d.day.First, d.habits.First[d.day.First.ID], d.habits.Days)
	last := habitSentence(t, "announce.daily.last", *d.day.Last, d.habits.Last[d.day.Last.ID], d.habits.Days)
	return "⏰ " + first + " " + last
}

// habitSentence picks the plain, "as usual" or "N days running" form. A run
// is the more specific claim and wins when both hold; "as usual" needs half
// the window's days, and enough of them for half to mean something.
func habitSentence(t i18n.Translator, key string, e stats.TodayEntry, h stats.Habit, days int) string {
	// The wall clock where the group lives, whatever zone the row came back
	// in; 24-hour in every language, since the languages here all read it.
	clock := e.PostedAt.In(time.Local).Format("15:04")
	switch {
	case h.Run >= runMin:
		return t.T(key+".run", e.Name, clock, h.Run)
	case days >= stats.HabitMinDays && h.Days*2 >= days:
		return t.T(key+".usual", e.Name, clock)
	default:
		return t.T(key, e.Name, clock)
	}
}

// spiceLine is the one remark the recap allows itself, the first of these
// that is true today: a change of leader, a streak reaching a milestone, an
// unusually hard or easy puzzle, who failed it, or somebody well under
// their own average. Rarer and bigger news first, so a day with two stories
// tells the one the group would otherwise miss; failures are frequent and
// visible in the thread, a milestone is neither.
func spiceLine(t i18n.Translator, d dayContext) string {
	for _, f := range []func(i18n.Translator, dayContext) string{
		leaderLine, streakLine, difficultyLine, failedLine, beatLine,
	} {
		if line := f(t, d); line != "" {
			return line
		}
	}
	return ""
}

// leaderLine fires when the day handed the month's lead to somebody who did
// not hold or share it the day before. Not in the month's first days, when
// the lead changes with every result, and not when the standing is being
// withheld for the 🏆 message.
func leaderLine(t i18n.Translator, d dayContext) string {
	if d.date.Day() < leaderFromDay || (d.monthResultFollows && lastDayOfMonth(d.date)) {
		return ""
	}
	before, ok := monthByKey(d.before, d.date.Year(), d.date.Month())
	if !ok || len(before.Winners) == 0 {
		return ""
	}
	after, ok := monthByKey(d.after, d.date.Year(), d.date.Month())
	if !ok || len(after.Winners) != 1 {
		return ""
	}
	for _, w := range before.Winners {
		if w.ID == after.Winners[0].ID {
			return ""
		}
	}
	label := capitalized(t.T("month." + strconv.Itoa(int(d.date.Month()))))
	return "👑 " + t.T("announce.daily.spice.leader", label, after.Winners[0].Name,
		joinNames(t, playerNames(before.Winners)))
}

// streakLine fires when a solve today took somebody's streak onto a
// milestone. The streak is the board's own, so the number is the one on
// their player page; the solve is required because a streak that stood at
// a milestone yesterday and was not played today would read the same.
func streakLine(t i18n.Translator, d dayContext) string {
	var best *stats.Player
	for _, e := range d.day.Filed {
		if !e.Solved {
			continue
		}
		p, ok := boardPlayer(d.board, e.ID)
		if !ok || !isStreakMilestone(p.CurrentStreak) {
			continue
		}
		if best == nil || p.CurrentStreak > best.CurrentStreak ||
			(p.CurrentStreak == best.CurrentStreak && p.Name < best.Name) {
			best = &p
		}
	}
	if best == nil {
		return ""
	}
	return "🔥 " + t.T("announce.daily.spice.streak", best.Name, best.CurrentStreak)
}

// difficultyLine calls the puzzle hard or easy when the day's mean sits far
// from what the group usually scores. It needs a few results to speak for
// the puzzle, and a history of at least the form window to have a "usually"
// at all — in a deployment's first week the day would be compared with a
// mean it dominates.
func difficultyLine(t i18n.Translator, d dayContext) string {
	if d.day.FiledCount() < dayMinFiled {
		return ""
	}
	usual, n := stats.MeanScore(d.history, d.board.Options)
	if n < stats.FormWindow {
		return ""
	}
	today, _ := stats.MeanScore(d.todays, d.board.Options)
	delta := today - usual
	switch {
	case delta >= dayDelta:
		return "🧱 " + t.T("announce.daily.spice.hard", t.Decimal(today, 1), t.Decimal(usual, 1))
	case delta <= -dayDelta:
		return "🪶 " + t.T("announce.daily.spice.easy", t.Decimal(today, 1), t.Decimal(usual, 1))
	default:
		return ""
	}
}

// failedLine names who did not get it. A failure is a result the player
// posted in the group themselves, which is what makes naming it fair game
// where naming an absentee is not. Left out when nobody solved it: the 🥇
// line has already said so.
func failedLine(t i18n.Translator, d dayContext) string {
	if d.day.Best == nil {
		return ""
	}
	var names []string
	for _, e := range d.day.Filed {
		if !e.Solved {
			names = append(names, e.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "💀 " + t.T("announce.daily.spice.failed", joinNames(t, names))
}

// beatLine is the day's surprise: whoever landed furthest under their own
// average going into the day, by at least beatMargin. The day's best is
// left out — the 🥇 line is theirs already — and so is anyone with too few
// games for an average to be worth beating.
func beatLine(t i18n.Translator, d dayContext) string {
	var (
		pick   *stats.TodayEntry
		pickBy float64
		avg    float64
	)
	for i := range d.day.Filed {
		e := &d.day.Filed[i]
		if !e.Solved || e.Guesses == d.day.Best.Guesses {
			continue
		}
		p, ok := boardPlayer(d.baseline, e.ID)
		if !ok || p.Average == nil || p.Games < stats.MinGames {
			continue
		}
		by := *p.Average - float64(e.Guesses)
		if by < beatMargin {
			continue
		}
		if pick == nil || by > pickBy || (by == pickBy && e.Name < pick.Name) {
			pick, pickBy, avg = e, by, *p.Average
		}
	}
	if pick == nil {
		return ""
	}
	return "📈 " + t.T("announce.daily.spice.beat", pick.Name, pick.Guesses, t.Decimal(avg, 1))
}

// boardPlayer finds one player's row, ranked or not.
func boardPlayer(b stats.Board, id int64) (stats.Player, bool) {
	for _, group := range [][]stats.Player{b.Ranked, b.Unranked} {
		for _, p := range group {
			if p.ID == id {
				return p, true
			}
		}
	}
	return stats.Player{}, false
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

	// Capitalized here rather than in the catalogue: this is the only spot
	// the month name leads a chat line instead of following a player's name
	// or a puzzle number, and the catalogue's lowercase form is correct
	// Swedish everywhere else it's used.
	label := capitalized(t.T("month." + strconv.Itoa(int(m.Month))))

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
		// The margin reads as "points" — hundredths of an average guess —
		// rather than as its own decimal average, since a second decimal
		// figure next to the leader's average read as two competing stats.
		points := int(math.Round(*m.Margin * 100))
		return "📊 " + t.T("announce.daily.month.margin", label, leaders, avg,
			points, joinNames(t, playerNames(runnersUp(m))))
	default:
		return "📊 " + t.T("announce.daily.month.alone", label, leaders, avg)
	}
}

// capitalized upper-cases s's first rune, leaving the rest untouched.
func capitalized(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
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
