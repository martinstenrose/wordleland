package reply

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// answer renders one request. Pure: every figure comes from stats over the
// history it is handed, which is what makes each kind testable against a
// fixture rather than a model.
func answer(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	switch req.Kind {
	case KindLeader:
		return leader(t, req, asker, players, results, now)
	case KindStanding:
		return standing(t, req, asker, players, results, now)
	case KindStreak:
		return streak(t, req, asker, players, results, now)
	case KindToday:
		return today(t, players, results, now)
	case KindScore:
		return score(t, req, asker, players, results, now)
	case KindWins:
		if req.Month != "" {
			// A month named is that month's result, not a tally of titles:
			// "vem vann juni?" read as wins is still a question about June.
			won := req
			won.Kind, won.Span, won.Player, won.Worst = KindLeader, SpanMonth, "", false
			return leader(t, won, nil, players, results, now)
		}
		return wins(t, req, asker, players, results, now)
	case KindCatchup:
		if req.Month != "" && closedMonth(req, now) {
			// "Who wins September?" once September is over has an
			// answer, not a race: its winner.
			won := req
			won.Kind, won.Span, won.Player, won.Worst = KindLeader, SpanMonth, "", false
			return leader(t, won, nil, players, results, now)
		}
		return catchup(t, req, asker, players, results, now)
	case KindCount:
		return count(t, req, asker, players, results, now)
	case KindHabits:
		return habits(t, req, asker, players, results, now)
	case KindWhatIf:
		return whatIf(t, req, players, results, now)
	case KindVersus:
		return versus(t, req, asker, players, results, now)
	case KindDayWins:
		return dayWins(t, req, asker, players, results, now)
	case KindDay:
		return day(t, req, players, results, now)
	case KindPuzzles:
		return puzzles(t, req, players, results, now)
	case KindWeekday:
		return weekday(t, req, asker, players, results, now)
	case KindForm:
		switch req.Span {
		case SpanWeek, SpanLastWeek, SpanDays:
			// "Best form this week" is best over this week: form is
			// otherwise the board's own window, which a span overrides.
			if req.Player == "" {
				over := req
				over.Kind = KindLeader
				return leader(t, over, asker, players, results, now)
			}
			over := req
			over.Kind = KindStanding
			return standing(t, over, asker, players, results, now)
		}
		return form(t, req, asker, players, results, now)
	case KindSteady:
		return steady(t, req, asker, players, results, now)
	case KindProfile:
		return profile(t, req, asker, players, results, now)
	case KindHistory:
		return history(t, req, asker, players, results, now)
	case KindRecords:
		return records(t, players, results, now)
	case KindGroup:
		return group(t, players, results, now)
	case KindRules:
		return rules(t, req)
	case KindThanks:
		return cheer(t, req, asker, players, results, now)
	case KindBot:
		return t.Vary("reply.bot")
	case KindHelp:
		return t.T("reply.help")
	default:
		// Short, because this is a chat: what the bot can do is one
		// question away, and listing it after every misread reads as
		// the bot lecturing the room.
		return t.Vary("reply.unknown")
	}
}

// cheer answers praise of a player, or of several. Calling them the best is
// a claim the figures settle: agreed when they top it, and otherwise
// answered with who does, since a bot that nods along to the wrong name
// is no use as a scorekeeper. The claim is over the period it said — a
// day, a week, this month, the board — and without one it is a cheer.
func cheer(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	var praised []store.Player
	for _, name := range req.Praised {
		if p, ok := findPlayer(name, players); ok && !slices.ContainsFunc(praised, func(q store.Player) bool { return q.ID == p.ID }) {
			praised = append(praised, p)
		}
	}
	if len(praised) == 0 {
		return t.Vary("reply.thanks")
	}
	all := make([]string, len(praised))
	for i, p := range praised {
		all[i] = p.Name
	}
	who := joinNames(t, all)
	you := len(praised) == 1 && isAsker(praised[0], asker)
	plain := func() string {
		if you {
			return t.Vary("reply.cheer.you")
		}
		return t.Vary("reply.cheer", who)
	}
	if req.Span == "" && req.Date == "" {
		return plain()
	}
	where, value, best, ok := bestOver(t, req, players, results, now)
	if !ok {
		// Nobody with a result over it: nothing to settle it with.
		return plain()
	}
	var top []store.Player
	for _, p := range praised {
		if slices.ContainsFunc(best, func(b store.Player) bool { return b.ID == p.ID }) {
			top = append(top, p)
		}
	}
	names := make([]string, len(best))
	for i, b := range best {
		names[i] = b.Name
	}
	winners := joinNames(t, names)
	switch {
	case len(top) == 0 && you:
		return t.T("reply.cheer.notbest.you", where, winners, value)
	case len(top) == 0:
		return t.T("reply.cheer.notbest", who, where, winners, value)
	case len(top) < len(praised):
		return t.T("reply.cheer.best.some", where, winners, value)
	case you && len(best) > 1:
		return t.T("reply.cheer.best.you.tie", where, value)
	case you:
		return t.T("reply.cheer.best.you", where, value)
	case len(best) > 1:
		return t.T("reply.cheer.best.tie", where, winners, value)
	default:
		return t.T("reply.cheer.best", where, who, value)
	}
}

// bestOver is who was best over a praise's period, said as the answer
// says it: where ("i dag", "den här veckan", "totalt"), and the score
// that won it — the day's guesses, or a period's average. False when
// nobody has a result over it.
func bestOver(t i18n.Translator, req Request, players []store.Player,
	results []store.BoardResult, now time.Time) (where, value string, best []store.Player, ok bool) {

	if req.Date != "" {
		date, err := time.ParseInLocation(DateLayout, req.Date, now.Location())
		if err != nil {
			return "", "", nil, false
		}
		puzzle := wordle.PuzzleForDate(date)
		lowest := 0
		for _, r := range results {
			if r.PuzzleNo != puzzle {
				continue
			}
			p, found := findByID(players, r.PlayerID)
			if !found {
				continue
			}
			switch v := resultValue(r); {
			case lowest == 0 || v < lowest:
				lowest, best = v, []store.Player{p}
			case v == lowest:
				best = append(best, p)
			}
		}
		if lowest == 0 {
			return "", "", nil, false
		}
		value = t.TN("reply.cheer.guesses", lowest)
		if lowest == failGuesses {
			value = t.T("reply.cheer.x")
		}
		return dayLabel(t, date, now), value, best, true
	}
	label, m := standingOver(t, req, players, results, now)
	if len(m.Winners) == 0 || m.Winners[0].Average == nil {
		return "", "", nil, false
	}
	for _, w := range m.Winners {
		best = append(best, w.Player)
	}
	if req.Span == SpanMonth {
		label = t.T("reply.cheer.in", label)
	}
	return label, t.T("reply.cheer.avg", t.Decimal(*m.Winners[0].Average, 2)), best, true
}

func findByID(players []store.Player, id int64) (store.Player, bool) {
	for _, p := range players {
		if p.ID == id {
			return p, true
		}
	}
	return store.Player{}, false
}

// isAsker says the player an answer is about is the one asking, who is
// answered as "you" rather than with their own name back, as a person in
// the chat would.
func isAsker(p store.Player, asker *store.Player) bool {
	return asker != nil && asker.ID == p.ID
}

// score is one player's result on one day, looked up in the history rather
// than computed: the one kind of question with a single stored answer.
func score(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	p, ok, text := whom(t, req, asker, players)
	if !ok {
		return text
	}
	date := now
	if req.Date != "" {
		// Validated by parseRequest; a bad one is already "".
		date, _ = time.ParseInLocation(DateLayout, req.Date, now.Location())
	}
	label := t.T("reply.date", date.Day(), t.T("month."+strconv.Itoa(int(date.Month()))))
	puzzle := wordle.PuzzleForDate(date)
	if puzzle > wordle.PuzzleForDate(now) {
		return t.T("reply.score.future", label)
	}
	you := isAsker(p, asker)
	for _, r := range results {
		if r.PlayerID != p.ID || r.PuzzleNo != puzzle {
			continue
		}
		switch {
		case !r.Solved && you:
			return t.T("reply.score.failed.you", label)
		case !r.Solved:
			return t.T("reply.score.failed", p.Name, label)
		case r.HardMode && you:
			return t.T("reply.score.hard.you", r.Guesses, label)
		case r.HardMode:
			return t.T("reply.score.hard", p.Name, r.Guesses, label)
		case you:
			return t.T("reply.score.solved.you", r.Guesses, label)
		default:
			return t.T("reply.score.solved", p.Name, r.Guesses, label)
		}
	}
	if you {
		return t.T("reply.score.none.you", label)
	}
	return t.T("reply.score.none", p.Name, label)
}

// wins reads the season, whose Wins counts closed months only: a month
// still running has no winner yet.
func wins(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	season := stats.ComputeSeason(stats.ComputeMonths(players, results, stats.DefaultOptions(now)), now)

	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		for _, row := range season.Rows {
			if row.ID == p.ID {
				return winsOf(t, p.Name, row.Wins, isAsker(p, asker))
			}
		}
		return winsOf(t, p.Name, 0, isAsker(p, asker))
	}

	best := 0
	for _, row := range season.Rows {
		if row.Wins > best {
			best = row.Wins
		}
	}
	if best == 0 {
		return t.T("reply.wins.none")
	}
	var leaders, rest []string
	for _, row := range season.Rows {
		switch {
		case row.Wins == best:
			leaders = append(leaders, row.Name)
		case row.Wins > 0:
			rest = append(rest, row.Name+" ("+t.Integer(row.Wins)+")")
		}
	}
	sort.Strings(leaders)
	line := "🏆 " + t.T("reply.wins", joinNames(t, leaders), best)
	if len(rest) > 0 {
		line += " " + t.T("reply.wins.then", strings.Join(rest, ", "))
	}
	return line
}

// winsOf is a player's month wins, one of them in the singular, said to
// them when they asked about themselves.
func winsOf(t i18n.Translator, name string, wins int, you bool) string {
	switch {
	case wins == 1 && you:
		return t.T("reply.wins.player.single.you")
	case wins == 1:
		return t.T("reply.wins.player.single", name)
	case you:
		return t.T("reply.wins.player.you", wins)
	}
	return t.T("reply.wins.player", name, wins)
}

// rules is one catalogue text per topic. The texts describe what
// internal/stats does and are kept true by hand: a change to a rule there
// is a change to its sentence here, in every language.
func rules(t i18n.Translator, req Request) string {
	if req.Topic == "" {
		return t.T("reply.rules.which")
	}
	return t.T("reply.rules." + string(req.Topic))
}

// standingOver ranks the span a request names, and labels it. The month's
// rules apply to a span of days too — a day not played is a failure —
// which is what makes "best over the last week" the same competition on a
// shorter window. All time is the board's own ranking, career averages
// with the board's minimum of games.
func standingOver(t i18n.Translator, req Request, players []store.Player,
	results []store.BoardResult, now time.Time) (string, stats.Month) {

	opts := stats.DefaultOptions(now)
	switch req.Span {
	case SpanDays:
		return t.T("reply.span.days", req.Days), stats.ComputeRecent(players, results, opts, req.Days)
	case SpanAll:
		return t.T("reply.span.all"), boardAsStanding(stats.Compute(players, results, opts))
	case SpanWeek, SpanLastWeek:
		first, last := spanPuzzles(req, results, now)
		w := stats.ComputeWeek(players, results, first, opts)
		return t.T("reply.span." + string(req.Span)), stats.Month{
			First: w.First, Last: w.Last, Days: last - first + 1, GroupAverage: w.GroupAverage,
			Ranked: w.Ranked, Thin: w.Thin, Winners: w.Winners, Margin: w.Margin,
		}
	default:
		year, month := namedMonth(req, now)
		label := t.T("month." + strconv.Itoa(int(month)))
		if year != now.Year() {
			label += " " + strconv.Itoa(year)
		}
		for _, m := range stats.ComputeMonths(players, results, opts) {
			if m.Year == year && m.Month == month {
				return label, m
			}
		}
		return label, stats.Month{Year: year, Month: month}
	}
}

// spanPuzzles is the first and last puzzle of the span a request names,
// the last never past today's: the days a question over the span can be
// answered from. All time starts at the group's first result.
func spanPuzzles(req Request, results []store.BoardResult, now time.Time) (int, int) {
	current := wordle.PuzzleForDate(now)
	switch req.Span {
	case SpanDays:
		return current - req.Days + 1, current
	case SpanAll:
		first := current
		for _, r := range results {
			first = min(first, r.PuzzleNo)
		}
		return first, current
	case SpanWeek:
		return stats.WeekOf(current), current
	case SpanLastWeek:
		monday := stats.WeekOf(current) - 7
		return monday, monday + 6
	default:
		year, month := namedMonth(req, now)
		start := time.Date(year, month, 1, 0, 0, 0, 0, now.Location())
		first := wordle.PuzzleForDate(start)
		return first, min(wordle.PuzzleForDate(start.AddDate(0, 1, 0))-1, current)
	}
}

// namedMonth is the month a request names, or the current one. The layout
// is checked by parseRequest, so a bad value has already become "".
func namedMonth(req Request, now time.Time) (int, time.Month) {
	if req.Month != "" {
		if m, err := time.ParseInLocation(MonthLayout, req.Month, now.Location()); err == nil {
			return m.Year(), m.Month()
		}
	}
	return now.Year(), now.Month()
}

// closedMonth says a request names a month that has ended, whose leader is
// its winner and is spoken of as such.
func closedMonth(req Request, now time.Time) bool {
	year, month := namedMonth(req, now)
	return stats.Month{Year: year, Month: month}.Complete(now)
}

// boardAsStanding reads the board's ranked table into the month's shape, so
// one rendering serves every span. Winners and Margin follow the month's
// definitions: everyone on the lowest average, and the gap to the next.
func boardAsStanding(b stats.Board) stats.Month {
	var m stats.Month
	for _, p := range b.Ranked {
		m.Ranked = append(m.Ranked, stats.MonthPlayer{
			Player: p.Player, Games: p.Games, Average: p.Average, Rank: p.Rank,
		})
	}
	if len(m.Ranked) == 0 {
		return m
	}
	lowest := *m.Ranked[0].Average
	for _, p := range m.Ranked {
		if *p.Average == lowest {
			m.Winners = append(m.Winners, p)
		}
	}
	if next := len(m.Winners); next < len(m.Ranked) {
		margin := *m.Ranked[next].Average - lowest
		m.Margin = &margin
	}
	return m
}

// leader mirrors internal/announce's month line, with the span's label in
// place of the month's, so the answer to "who is leading" reads exactly as
// the daily recap's standing does.
func leader(t i18n.Translator, req Request, asker *store.Player, players []store.Player,
	results []store.BoardResult, now time.Time) string {

	span, m := standingOver(t, req, players, results, now)
	// Capitalised where it opens the sentence; the closed month's line
	// has it mid-sentence, where "juli" stays lowercase in Swedish.
	label := capitalized(span)
	if req.Worst {
		return last(t, label, m)
	}
	if len(m.Winners) == 0 {
		return t.T("reply.leader.none", label)
	}
	leaders := joinNames(t, names(m.Winners))
	avg := t.Decimal(*m.Winners[0].Average, 2)
	if req.Span == SpanMonth && closedMonth(req, now) {
		// A month that has ended has a winner, not a leader: the 🏆
		// message's own wording, since that is the result being asked for.
		switch {
		case len(m.Winners) > 1:
			return "🏆 " + leaders + ": " + t.T("announce.line.tie", avg)
		case m.Margin != nil:
			return "🏆 " + t.T("announce.line.margin", leaders, span, avg, t.Decimal(*m.Margin, 2), m.Days)
		default:
			return "🏆 " + t.T("announce.line.alone", leaders, avg, m.Days)
		}
	}
	var line string
	switch {
	case len(m.Winners) > 1:
		line = "📊 " + t.T("announce.daily.month.tie", label, leaders, avg)
	case m.Margin != nil:
		points := int(math.Round(*m.Margin * 100))
		line = "📊 " + t.T("announce.daily.month.margin", label, leaders, avg,
			points, joinNames(t, names(runnersUp(m))))
	default:
		line = "📊 " + t.T("announce.daily.month.alone", label, leaders, avg)
	}
	if req.Span == SpanMonth && req.Month == "" {
		if aside := raceAside(t, m, asker, now); aside != "" {
			line += " " + aside
		}
	}
	return line
}

// tightPoints is a lead small enough to call the race close: a tenth of a
// guess on average, which one bad day undoes.
const tightPoints = 10

// raceAside is the one remark an answer about the month's lead allows
// itself, the first of these that is true: the asker is the one leading,
// the lead is tight with days still to play, or today settles it. What it
// says is what the line above it already shows, said as a person would.
func raceAside(t i18n.Translator, m stats.Month, asker *store.Player, now time.Time) string {
	for _, w := range m.Winners {
		if asker != nil && w.ID == asker.ID {
			if len(m.Winners) > 1 {
				return t.T("reply.leader.you.tie")
			}
			return t.T("reply.leader.you")
		}
	}
	left := daysInMonth(now) - now.Day()
	switch {
	case left == 0:
		return t.T("reply.leader.lastDay")
	case len(m.Winners) == 1 && m.Margin != nil && int(math.Round(*m.Margin*100)) <= tightPoints:
		return t.Vary("reply.leader.close", t.TN("reply.days", left))
	}
	return ""
}

// daysInMonth is how many days the month of now has.
func daysInMonth(now time.Time) int {
	return time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
}

// regularShare is the share of a span's days a player must have played to
// be named for coming last: the weekly recap's five of seven, applied to
// any span. A missed day scores 7, so without it last place would go to
// whoever was away, and naming absentees is what the bot never does.
const regularShare = 5.0 / 7

// last names the bottom of the table among those who played most of the
// span. An all-time table has no days to count; there the board's own
// minimum of games already keeps absentees off it.
func last(t i18n.Translator, label string, m stats.Month) string {
	need := int(math.Ceil(float64(m.Days) * regularShare))
	var regulars []stats.MonthPlayer
	for _, p := range m.Ranked {
		if p.Games >= need {
			regulars = append(regulars, p)
		}
	}
	if len(regulars) < 2 {
		return t.T("reply.last.none", label)
	}
	worst := *regulars[len(regulars)-1].Average
	var bottom []string
	for _, p := range regulars {
		if *p.Average == worst {
			bottom = append(bottom, p.Name)
		}
	}
	avg := t.Decimal(worst, 2)
	if len(bottom) > 1 {
		return t.T("mark.last") + " " + t.T("reply.last.tie", label, joinNames(t, bottom), avg)
	}
	return t.T("mark.last") + " " + t.T("reply.last", label, bottom[0], avg)
}

func standing(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	// Nobody named is everybody: "ställningen", the whole table, whoever
	// asks. "How am I doing" names the asker, by the prompt.
	if req.Player == "" {
		label, m := standingOver(t, req, players, results, now)
		if len(m.Ranked) == 0 {
			return t.T("reply.leader.none", capitalized(label))
		}
		lines := []string{capitalized(label) + ":"}
		for _, p := range m.Ranked {
			// Rank, name, average: the same in every language, so no key.
			lines = append(lines, fmt.Sprintf("%d. %s %s", p.Rank, p.Name, t.Decimal(*p.Average, 2)))
		}
		return strings.Join(lines, "\n")
	}

	p, ok, text := whom(t, req, asker, players)
	if !ok {
		return text
	}
	label, m := standingOver(t, req, players, results, now)
	you := isAsker(p, asker)
	for _, mp := range m.Ranked {
		if mp.ID != p.ID {
			continue
		}
		var line string
		if you {
			line = t.T("reply.standing.you", mp.Rank, len(m.Ranked), label, t.Decimal(*mp.Average, 2), mp.Games)
		} else {
			line = t.T("reply.standing", p.Name, mp.Rank, len(m.Ranked), label,
				t.Decimal(*mp.Average, 2), mp.Games)
		}
		if gap := gapLine(t, m, mp); gap != "" {
			line += " " + gap
		}
		return line
	}
	if req.Span == SpanAll {
		// All time is the board, which leaves a player out for a reason
		// — too few games, none lately, gone inactive — and says so;
		// "no games" is for somebody who has none.
		for _, bp := range stats.Compute(players, results, stats.DefaultOptions(now)).Unranked {
			if bp.ID != p.ID || bp.Average == nil {
				continue
			}
			if you {
				return t.T("reply.standing.unranked.you", t.T(reasonKey(bp.Reason)), bp.Games, t.Decimal(*bp.Average, 2))
			}
			return t.T("reply.standing.unranked", p.Name, t.T(reasonKey(bp.Reason)), bp.Games, t.Decimal(*bp.Average, 2))
		}
	}
	if you {
		return t.T("reply.standing.none.you", label)
	}
	return t.T("reply.standing.none", p.Name, label)
}

// gapLine is how far a player is from the place that matters to them: the
// lead over whoever is next for a leader, the company of a shared lead,
// and for anybody else the points to the place just above. A place is a
// number; the gap is what makes it read as a race.
func gapLine(t i18n.Translator, m stats.Month, mp stats.MonthPlayer) string {
	if len(m.Winners) > 0 && *mp.Average == *m.Winners[0].Average {
		var others []string
		for _, w := range m.Winners {
			if w.ID != mp.ID {
				others = append(others, w.Name)
			}
		}
		switch {
		case len(others) > 0:
			return t.T("reply.standing.level", joinNames(t, others))
		case m.Margin != nil:
			next := joinNames(t, names(runnersUp(m)))
			if points := int(math.Round(*m.Margin * 100)); points > 0 {
				return t.T("reply.standing.clear", points, next)
			}
			// A gap under half a point is a gap, and not "0 points".
			return t.T("reply.standing.clear.just", next)
		}
		return ""
	}
	// The place above is everyone on the best average still worse than
	// the leader's and better than this player's.
	var above []string
	var aboveAvg float64
	for _, o := range m.Ranked {
		if *o.Average >= *mp.Average {
			break
		}
		if len(above) == 0 || *o.Average != aboveAvg {
			above, aboveAvg = nil, *o.Average
		}
		above = append(above, o.Name)
	}
	if len(above) == 0 {
		return ""
	}
	if points := int(math.Round((*mp.Average - aboveAvg) * 100)); points > 0 {
		return t.T("reply.standing.behind", points, joinNames(t, above))
	}
	return t.T("reply.standing.behind.just", joinNames(t, above))
}

// streak reads the board, whose streaks are computed from the unfiltered
// history for every player, ranked or not.
func streak(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	board := stats.Compute(players, results, stats.DefaultOptions(now))
	all := append(append([]stats.Player(nil), board.Ranked...), board.Unranked...)

	// A player was named — the asker's own name when they asked about
	// themselves, which is how the model reports "my streak".
	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		current, longest := 0, 0
		for _, bp := range all {
			if bp.ID == p.ID {
				current, longest = bp.CurrentStreak, bp.LongestStreak
			}
		}
		if isAsker(p, asker) {
			return t.T("reply.streak.player.you", t.TN("reply.days", current), longest)
		}
		return t.T("reply.streak.player", p.Name, t.TN("reply.days", current), longest)
	}

	current, currentDays := holders(all, func(p stats.Player) int { return p.CurrentStreak })
	longest, longestDays := holders(all, func(p stats.Player) int { return p.LongestStreak })

	var lines []string
	if currentDays > 0 {
		lines = append(lines, "🔥 "+t.T("reply.streak.current", joinNames(t, current), t.TN("reply.days", currentDays)))
	} else {
		lines = append(lines, t.T("reply.streak.none"))
	}
	if longestDays > 0 {
		lines = append(lines, t.T("reply.streak.ever", joinNames(t, longest), t.TN("reply.days", longestDays)))
	}
	return strings.Join(lines, "\n")
}

// holders names everyone sharing the highest value of a figure, in name
// order, so a tie is named rather than decided by the board's order.
func holders(all []stats.Player, figure func(stats.Player) int) ([]string, int) {
	best := 0
	for _, p := range all {
		if v := figure(p); v > best {
			best = v
		}
	}
	var names []string
	for _, p := range all {
		if figure(p) == best {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, best
}

func today(t i18n.Translator, players []store.Player, results []store.BoardResult, now time.Time) string {
	puzzle := wordle.PuzzleForDate(now)
	day := stats.ComputeToday(players, results, puzzle)

	lines := []string{t.T("reply.today.head", i18n.Identifier(puzzle), day.FiledCount(), day.Expected())}
	switch {
	case day.Best != nil:
		var best []string
		for _, e := range day.Filed {
			if e.Solved && e.Guesses == day.Best.Guesses {
				best = append(best, e.Name)
			}
		}
		lines = append(lines, t.T("reply.today.best", joinNames(t, best), day.Best.Guesses))
	case len(day.Filed) > 0:
		lines = append(lines, t.T("reply.today.noneSolved"))
	}
	if len(day.Missing) > 0 {
		var missing []string
		for _, p := range day.Missing {
			missing = append(missing, p.Name)
		}
		lines = append(lines, t.T("reply.today.missing", joinNames(t, missing)))
	} else if len(day.Filed) > 0 {
		lines = append(lines, t.T("reply.today.everyone"))
	}
	return strings.Join(lines, "\n")
}

// whom finds the player a question is about: the one named, or the asker
// when nobody is. When there is neither, or the name is not a player's, the
// answer is the list of who it could be.
func whom(t i18n.Translator, req Request, asker *store.Player, players []store.Player) (store.Player, bool, string) {
	all := make([]string, 0, len(players))
	for _, p := range players {
		all = append(all, p.Name)
	}
	if req.Player == "" || req.Player == Asker {
		if asker != nil {
			return *asker, true, ""
		}
		return store.Player{}, false, t.T("reply.standing.who", joinNames(t, all))
	}
	if p, ok := findPlayer(req.Player, players); ok {
		return p, true, ""
	}
	return store.Player{}, false, t.T("reply.player.unknown", req.Player, joinNames(t, all))
}

// findPlayer is forgiving about case and about a first name standing in
// for a full one, because the model copies what it was given but the
// group does not always say it that way. Forgiving, not guessing: a name
// that fits more than one player is nobody, and the answer asks.
func findPlayer(name string, players []store.Player) (store.Player, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return store.Player{}, false
	}
	for _, p := range players {
		if strings.ToLower(p.Name) == want {
			return p, true
		}
	}
	// A whole first name before a prefix: "Bo" is Bo Andersson even with a
	// Bob in the group, and only a bare prefix has to be unique.
	var firsts, prefixes []store.Player
	for _, p := range players {
		lower := strings.ToLower(p.Name)
		first, _, _ := strings.Cut(lower, " ")
		switch {
		case first == want:
			firsts = append(firsts, p)
		case strings.HasPrefix(lower, want):
			prefixes = append(prefixes, p)
		}
	}
	if len(firsts) == 1 {
		return firsts[0], true
	}
	if len(firsts) == 0 && len(prefixes) == 1 {
		return prefixes[0], true
	}
	return store.Player{}, false
}

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

func names(ps []stats.MonthPlayer) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// joinNames renders a tie as every name, as internal/announce does.
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

func capitalized(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
