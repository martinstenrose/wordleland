package announce

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// NewWeekly returns the week's closure, called after every live message and
// by the run just after midnight. The week is Monday to Sunday, and it is
// posted once every active player has filed Sunday's puzzle, or at the first
// run after Sunday closes, whichever comes first.
//
// It has the same contract as NewMonthly and NewDaily.
//
// dailyGoesFirst says whether the day's recap is also configured. When it
// is, the week waits for Sunday's recap to be out, so the group reads the
// day before the week it closed — the two share a trigger, and a week that
// jumped the queue would summarise a Sunday nobody had been told about yet.
func NewWeekly(db *sql.DB, cats i18n.Catalogues, locale string, dailyGoesFirst bool,
	send func(ctx context.Context, text string) error) func(context.Context, time.Time) error {

	t := i18n.NewTranslator(cats, locale)
	// The same race as the day's: the last Sunday result and the midnight run
	// can arrive together.
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

		first, due, err := weeklyDue(ctx, db, players, results, wordle.PuzzleForDate(now), dailyGoesFirst)
		if err != nil || !due {
			return err
		}
		w, err := newWeekContext(players, results, first)
		if err != nil {
			return err
		}
		if len(w.week.Ranked)+len(w.week.Thin) < weekMinPlayers {
			// Not recorded, for the reason a silent month is not: a late
			// result for the week can still make it worth posting.
			return nil
		}

		if err := send(ctx, weeklyPost(t, w)); err != nil {
			return fmt.Errorf("post to signal: %w", err)
		}
		if err := store.RecordWeekAnnouncement(ctx, db, first); err != nil {
			return fmt.Errorf("record that the week from puzzle %d was announced: %w", first, err)
		}
		return nil
	}
}

// SkipWeeklyBacklog is SkipDailyBacklog for the week: on a deployment that
// has never announced one, the week before the current one is marked done,
// so the first weekly recap is about a week the bot was there for.
func SkipWeeklyBacklog(ctx context.Context, db *sql.DB, now time.Time) error {
	announced, err := store.AnyWeekAnnounced(ctx, db)
	if err != nil {
		return err
	}
	if announced {
		return nil
	}
	previous := stats.WeekOf(wordle.PuzzleForDate(now)) - 7
	if err := store.RecordWeekAnnouncement(ctx, db, previous); err != nil {
		return fmt.Errorf("set the week from puzzle %d as the weekly recap's starting point: %w", previous, err)
	}
	return nil
}

// weeklyDue picks the week this run should post about, if any: the current
// week on a Sunday every active player has played, and otherwise the week
// that ended most recently.
//
// Only ever one week back, for the reason dailyDue gives: an outage longer
// than that loses a recap rather than posting two in a burst.
//
// The wait for Sunday's recap lasts only while it can still come. dailyDue
// looks one day back, so from Tuesday on it will never post Sunday, and
// waiting for it then would lose the week too.
func weeklyDue(ctx context.Context, db *sql.DB, players []store.Player,
	results []store.BoardResult, current int, dailyGoesFirst bool) (int, bool, error) {

	first := stats.WeekOf(current)
	if current != first+6 {
		first -= 7
	}
	sunday := first + 6

	day := stats.ComputeToday(players, results, sunday)
	if current == sunday && (len(day.Filed) == 0 || len(day.Missing) > 0) {
		return 0, false, nil
	}
	if len(through(results, sunday)) == len(through(results, first-1)) {
		// Nobody filed all week. Left unrecorded, as an empty day is.
		return 0, false, nil
	}

	done, err := store.WeekAnnounced(ctx, db, first)
	if err != nil {
		return 0, false, fmt.Errorf("check whether the week from puzzle %d was announced: %w", first, err)
	}
	if done {
		return 0, false, nil
	}

	if dailyGoesFirst && len(day.Filed) > 0 && current <= sunday+1 {
		posted, err := store.DayAnnounced(ctx, db, sunday)
		if err != nil {
			return 0, false, fmt.Errorf("check whether puzzle %d was announced: %w", sunday, err)
		}
		if !posted {
			return 0, false, nil
		}
	}
	return first, true, nil
}

// Thresholds for the week's lines. The same rule as the day's: a line
// appears when there is something to say.
const (
	// weekMinPlayers is how many need to have played for a week to be a
	// competition. One player's week is their player page.
	weekMinPlayers = 2
	// podiumPlaces is how far down the table the podium reaches. Ranks
	// are shared by ties, so it can name more than three.
	podiumPlaces = 3
	// regularDays is how many of the seven days a player needs to have
	// played to be named for coming last, or in a close finish. Counted
	// days are what keeps the wooden spoon from naming whoever was away:
	// a missed day scores 7, so last place would otherwise go to an
	// absentee, and naming absentees is what the recaps never do.
	regularDays = 5
	// extrasCap is how many of the extra lines a week gets. The core is
	// four lines; three more keeps it one screen.
	extrasCap = 3
	// crownMin is how many days' best one player needs for it to be a
	// line. Once is a day's news, not a week's.
	crownMin = 3
	// photoGap is the widest gap between neighbours that still reads as a
	// close finish: a quarter of a guess.
	photoGap = 0.25
	// moverMargin is how far under their own average going into the week a
	// player's week has to land to be its turnaround.
	moverMargin = 0.75
	// swingBest is the score that, with an X in the same week, makes it a
	// rollercoaster.
	swingBest = 2
	// weekRunMin is how many weeks in a row at the top make a run worth
	// saying, and weekRunEndMin how long one has to have been for its end
	// to be news. Two in a row is a run; ending one of two is just a new
	// winner.
	weekRunMin    = 2
	weekRunEndMin = 3
	// earlyMinDays is how many stamped posts a player needs for a usual
	// time to be theirs.
	earlyMinDays = 5
)

// medals marks the podium places.
var medals = map[int]string{1: "🥇", 2: "🥈", 3: "🥉"}

// weekContext is everything the week's message is composed from.
type weekContext struct {
	week, previous stats.Week
	monday         time.Time
	players        []store.Player
	// rows are the week's own results, and nothing after the Sunday;
	// history is every result up to the Sunday, for earlier weeks.
	rows, history []store.BoardResult
	// baseline is the group going into the week, for what a player
	// usually scores.
	baseline stats.Board
	opts     stats.Options
}

func newWeekContext(players []store.Player, results []store.BoardResult, first int) (weekContext, error) {
	monday, err := wordle.DateForPuzzle(first)
	if err != nil {
		return weekContext{}, fmt.Errorf("date for puzzle %d: %w", first, err)
	}
	upTo := through(results, first+6)
	prior := through(results, first-1)
	// Scored as of the next Monday: a recap posted early on a Sunday is
	// posted once every active player is in, so nobody still expected
	// could change it.
	opts := stats.DefaultOptions(monday.AddDate(0, 0, 7))
	return weekContext{
		week:     stats.ComputeWeek(players, upTo, first, opts),
		previous: stats.ComputeWeek(players, prior, first-7, opts),
		monday:   monday,
		players:  players,
		rows:     upTo[len(prior):],
		history:  upTo,
		baseline: stats.Compute(players, prior, stats.DefaultOptions(monday)),
		opts:     opts,
	}, nil
}

// weeklyPost is the message: the week, its podium, who came last and how the
// group did, and up to extrasCap of the rest.
func weeklyPost(t i18n.Translator, w weekContext) string {
	var lines []string
	for _, f := range []func(i18n.Translator, weekContext) string{
		weekHeadLine, podiumLine, spoonLine, weekAverageLine,
	} {
		if line := f(t, w); line != "" {
			lines = append(lines, line)
		}
	}
	extras := 0
	for _, f := range []func(i18n.Translator, weekContext) string{
		runLine, crownLine, photoLine, moverLine, swingLine, attendanceLine, earlyLine,
	} {
		if extras == extrasCap {
			break
		}
		if line := f(t, w); line != "" {
			lines = append(lines, line)
			extras++
		}
	}
	return strings.Join(lines, "\n")
}

func weekHeadLine(t i18n.Translator, w weekContext) string {
	_, number := w.monday.ISOWeek()
	return "🗓️ " + t.T("announce.weekly.head", number,
		len(w.week.Ranked)+len(w.week.Thin), len(w.rows))
}

// podiumLine is every rank up to podiumPlaces with its average, ties
// sharing a medal.
func podiumLine(t i18n.Translator, w weekContext) string {
	var parts []string
	for _, group := range rankGroups(w.week.Ranked) {
		if group[0].Rank > podiumPlaces {
			break
		}
		parts = append(parts, medals[group[0].Rank]+" "+joinNames(t, playerNames(group))+
			" "+t.Decimal(*group[0].Average, 2))
	}
	return strings.Join(parts, " · ")
}

// spoonLine names the bottom of the table among those who played most of
// the week — see regularDays — and stays away when that is somebody already
// on the podium, which in a small group it can be.
func spoonLine(t i18n.Translator, w weekContext) string {
	groups := rankGroups(regulars(w.week.Ranked))
	if len(groups) < 2 {
		return ""
	}
	last := groups[len(groups)-1]
	if last[0].Rank <= podiumPlaces {
		return ""
	}
	avg := t.Decimal(*last[0].Average, 2)
	if len(last) > 1 {
		return "🥄 " + t.T("announce.weekly.spoonShared", joinNames(t, playerNames(last)), avg)
	}
	return "🥄 " + t.T("announce.weekly.spoon", last[0].Name, avg)
}

// weekAverageLine is the group's average over the results it posted, set
// against the week before when there was one.
func weekAverageLine(t i18n.Translator, w weekContext) string {
	if w.week.GroupAverage == nil {
		return ""
	}
	now := *w.week.GroupAverage
	avg := t.Decimal(now, 2)
	if w.previous.GroupAverage == nil {
		return "📊 " + t.T("announce.weekly.average", avg)
	}
	delta := math.Round((now-*w.previous.GroupAverage)*100) / 100
	switch {
	case delta < 0:
		return "📊 " + t.T("announce.weekly.averageBetter", avg, t.Decimal(-delta, 2))
	case delta > 0:
		return "📊 " + t.T("announce.weekly.averageWorse", avg, t.Decimal(delta, 2))
	default:
		return "📊 " + t.T("announce.weekly.averageSame", avg)
	}
}

// runLine is a run of weeks at the top: the week's winner on their second
// week running or more, or, failing that, the week that ended somebody
// else's run of weekRunEndMin or more. A shared win counts for everyone who
// shares it, as a shared month does.
func runLine(t i18n.Translator, w weekContext) string {
	longest, names := 0, []string(nil)
	for _, p := range w.week.Winners {
		n := 1 + weeksRunning(w, p.ID, w.week.First-7)
		switch {
		case n > longest:
			longest, names = n, []string{p.Name}
		case n == longest:
			names = append(names, p.Name)
		}
	}
	if longest >= weekRunMin {
		sort.Strings(names)
		return "🔁 " + t.T("announce.weekly.run", joinNames(t, names), longest)
	}

	won := make(map[int64]bool)
	for _, p := range w.week.Winners {
		won[p.ID] = true
	}
	longest, names = 0, nil
	for _, p := range w.previous.Winners {
		if won[p.ID] {
			continue
		}
		n := weeksRunning(w, p.ID, w.week.First-7)
		switch {
		case n > longest:
			longest, names = n, []string{p.Name}
		case n == longest:
			names = append(names, p.Name)
		}
	}
	if longest < weekRunEndMin || len(w.week.Winners) == 0 {
		return ""
	}
	sort.Strings(names)
	return "🔁 " + t.T("announce.weekly.runEnded",
		joinNames(t, playerNames(w.week.Winners)), joinNames(t, names), longest)
}

// weeksRunning counts the weeks in a row, back from the one starting at
// first, that player was among the winners of. It stops at the first week
// they were not, which a week with nobody in it always is.
func weeksRunning(w weekContext, player int64, first int) int {
	n := 0
	for ; ; first -= 7 {
		week := stats.ComputeWeek(w.players, w.history, first, w.opts)
		if len(week.Ranked)+len(week.Thin) < weekMinPlayers || !isWinner(week, player) {
			return n
		}
		n++
	}
}

func isWinner(week stats.Week, player int64) bool {
	for _, p := range week.Winners {
		if p.ID == player {
			return true
		}
	}
	return false
}

// crownLine is whoever had the day's best most often. A shared best counts
// for everyone sharing it, as the day's 🥇 line names them all.
func crownLine(t i18n.Translator, w weekContext) string {
	crowns := make(map[int64]int)
	days := 0
	for p := w.week.First; p <= w.week.Last; p++ {
		day := stats.ComputeToday(w.players, w.rows, p)
		if day.Best == nil {
			continue
		}
		days++
		for _, e := range day.Filed {
			if e.Solved && e.Guesses == day.Best.Guesses {
				crowns[e.ID]++
			}
		}
	}
	most := 0
	for _, n := range crowns {
		most = max(most, n)
	}
	if most < crownMin {
		return ""
	}
	var names []string
	for _, p := range w.players {
		if crowns[p.ID] == most {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	if len(names) > 1 {
		return "👑 " + t.T("announce.weekly.crownsShared", joinNames(t, names), most, days)
	}
	return "👑 " + t.T("announce.weekly.crowns", names[0], most, days)
}

// photoLine is the closest finish between neighbouring places, among those
// who played most of the week. A tie is not a close finish, it is a tie,
// and the podium already says so. The higher of two equal gaps wins: a
// close race for first is the better story than one for fifth.
func photoLine(t i18n.Translator, w weekContext) string {
	groups := rankGroups(regulars(w.week.Ranked))
	var ahead, behind []stats.MonthPlayer
	gap := photoGap
	for i := 1; i < len(groups); i++ {
		d := *groups[i][0].Average - *groups[i-1][0].Average
		if d <= gap && (ahead == nil || d < gap) {
			ahead, behind, gap = groups[i-1], groups[i], d
		}
	}
	if ahead == nil {
		return ""
	}
	return "📸 " + t.T("announce.weekly.photo", joinNames(t, playerNames(ahead)),
		joinNames(t, playerNames(behind)), t.Decimal(gap, 2))
}

// moverLine is the week's turnaround: whoever landed furthest under their
// own average going into it, by at least moverMargin. The week's average
// counts missed days as 7, so this only ever finds somebody who played.
func moverLine(t i18n.Translator, w weekContext) string {
	var (
		pick   *stats.MonthPlayer
		pickBy float64
		usual  float64
	)
	for i, p := range w.week.Ranked {
		if p.Games < regularDays {
			continue
		}
		b, ok := boardPlayer(w.baseline, p.ID)
		if !ok || b.Average == nil || b.Games < stats.MinGames {
			continue
		}
		by := *b.Average - *p.Average
		if by < moverMargin {
			continue
		}
		if pick == nil || by > pickBy || (by == pickBy && p.Name < pick.Name) {
			pick, pickBy, usual = &w.week.Ranked[i], by, *b.Average
		}
	}
	if pick == nil {
		return ""
	}
	return "🚀 " + t.T("announce.weekly.mover", pick.Name, t.Decimal(*pick.Average, 2), t.Decimal(usual, 2))
}

// swingLine is a week of extremes, or failing that one of none: a 2 or
// better and an X from the same player, or the same score all seven days.
func swingLine(t i18n.Translator, w weekContext) string {
	type span struct {
		best, worst, solved, games int
		failed                     bool
	}
	spans := make(map[int64]*span)
	for _, r := range w.rows {
		s := spans[r.PlayerID]
		if s == nil {
			s = &span{best: 7}
			spans[r.PlayerID] = s
		}
		s.games++
		if !r.Solved {
			s.failed = true
			continue
		}
		s.solved++
		s.best = min(s.best, r.Guesses)
		s.worst = max(s.worst, r.Guesses)
	}

	// The lowest best among the rollercoasters, everyone who shares it named.
	lowest := swingBest + 1
	for _, s := range spans {
		if s.failed && s.best < lowest {
			lowest = s.best
		}
	}
	if lowest <= swingBest {
		var names []string
		for _, p := range w.players {
			if s := spans[p.ID]; s != nil && s.failed && s.best == lowest {
				names = append(names, p.Name)
			}
		}
		sort.Strings(names)
		return "🎢 " + t.T("announce.weekly.rollercoaster", joinNames(t, names), lowest)
	}

	// The metronome: every day played, every one solved, every one the
	// same. The best of them by the week's average, if several.
	for _, p := range w.week.Ranked {
		s := spans[p.ID]
		if s != nil && s.games == 7 && s.solved == 7 && s.worst == s.best {
			return "🎯 " + t.T("announce.weekly.metronome", p.Name, s.best)
		}
	}
	return ""
}

// attendanceLine is said only when everyone who played the week played all
// of it. Naming the ones who did would name the ones who did not by
// elimination.
func attendanceLine(t i18n.Translator, w weekContext) string {
	for _, group := range [][]stats.MonthPlayer{w.week.Ranked, w.week.Thin} {
		for _, p := range group {
			if p.Games != 7 {
				return ""
			}
		}
	}
	return "✅ " + t.T("announce.weekly.attendance")
}

// earlyLine is the week's early bird: the earliest usual posting time, the
// median of a player's stamped posts on the group's clock. The median
// rather than the mean, so one sleepless night does not make somebody an
// early riser.
func earlyLine(t i18n.Translator, w weekContext) string {
	minutes := make(map[int64][]int)
	for _, r := range w.rows {
		if r.PostedAt == nil {
			continue
		}
		at := r.PostedAt.In(time.Local)
		minutes[r.PlayerID] = append(minutes[r.PlayerID], at.Hour()*60+at.Minute())
	}
	earliest := -1
	var names []string
	for _, p := range w.players {
		m := minutes[p.ID]
		if len(m) < earlyMinDays {
			continue
		}
		sort.Ints(m)
		median := m[(len(m)-1)/2]
		switch {
		case earliest < 0 || median < earliest:
			earliest, names = median, []string{p.Name}
		case median == earliest:
			names = append(names, p.Name)
		}
	}
	if names == nil {
		return ""
	}
	sort.Strings(names)
	clock := fmt.Sprintf("%02d:%02d", earliest/60, earliest%60)
	return "⏰ " + t.T("announce.weekly.early", joinNames(t, names), clock)
}

// rankGroups splits a ranked table into its places, ties together.
func rankGroups(ranked []stats.MonthPlayer) [][]stats.MonthPlayer {
	var groups [][]stats.MonthPlayer
	for i, p := range ranked {
		if i > 0 && *p.Average == *ranked[i-1].Average {
			groups[len(groups)-1] = append(groups[len(groups)-1], p)
			continue
		}
		groups = append(groups, []stats.MonthPlayer{p})
	}
	return groups
}

// regulars keeps the ranked players who played at least regularDays of the
// week, in their order.
func regulars(ranked []stats.MonthPlayer) []stats.MonthPlayer {
	var out []stats.MonthPlayer
	for _, p := range ranked {
		if p.Games >= regularDays {
			out = append(out, p)
		}
	}
	return out
}
