package reply

import (
	"math"
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
		return leader(t, req, players, results, now)
	case KindStanding:
		return standing(t, req, asker, players, results, now)
	case KindStreak:
		return streak(t, req, asker, players, results, now)
	case KindToday:
		return today(t, players, results, now)
	case KindScore:
		return score(t, req, asker, players, results, now)
	case KindWins:
		return wins(t, req, asker, players, results, now)
	case KindCatchup:
		return catchup(t, req, asker, players, results, now)
	case KindCount:
		return count(t, req, asker, players, results, now)
	case KindHabits:
		return habits(t, req, asker, players, results, now)
	case KindRules:
		return rules(t, req)
	case KindThanks:
		return t.T("reply.thanks")
	default:
		return t.T("reply.help")
	}
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
	for _, r := range results {
		if r.PlayerID != p.ID || r.PuzzleNo != puzzle {
			continue
		}
		switch {
		case !r.Solved:
			return t.T("reply.score.failed", p.Name, label)
		case r.HardMode:
			return t.T("reply.score.hard", p.Name, label, r.Guesses)
		default:
			return t.T("reply.score.solved", p.Name, label, r.Guesses)
		}
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
				return t.T("reply.wins.player", p.Name, row.Wins)
			}
		}
		return t.T("reply.wins.player", p.Name, 0)
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
func leader(t i18n.Translator, req Request, players []store.Player,
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
	switch {
	case len(m.Winners) > 1:
		return "📊 " + t.T("announce.daily.month.tie", label, leaders, avg)
	case m.Margin != nil:
		points := int(math.Round(*m.Margin * 100))
		return "📊 " + t.T("announce.daily.month.margin", label, leaders, avg,
			points, joinNames(t, names(runnersUp(m))))
	default:
		return "📊 " + t.T("announce.daily.month.alone", label, leaders, avg)
	}
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
		return "🥄 " + t.T("reply.last.tie", label, joinNames(t, bottom), avg)
	}
	return "🥄 " + t.T("reply.last", label, bottom[0], avg)
}

func standing(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	p, ok, text := whom(t, req, asker, players)
	if !ok {
		return text
	}
	label, m := standingOver(t, req, players, results, now)
	for _, mp := range m.Ranked {
		if mp.ID == p.ID {
			return t.T("reply.standing", p.Name, mp.Rank, len(m.Ranked), label,
				t.Decimal(*mp.Average, 2), mp.Games)
		}
	}
	return t.T("reply.standing.none", p.Name, label)
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
		for _, bp := range all {
			if bp.ID == p.ID {
				return t.T("reply.streak.player", p.Name, bp.CurrentStreak, bp.LongestStreak)
			}
		}
		return t.T("reply.streak.player", p.Name, 0, 0)
	}

	current, currentDays := holders(all, func(p stats.Player) int { return p.CurrentStreak })
	longest, longestDays := holders(all, func(p stats.Player) int { return p.LongestStreak })

	var lines []string
	if currentDays > 0 {
		lines = append(lines, "🔥 "+t.T("reply.streak.current", joinNames(t, current), currentDays))
	} else {
		lines = append(lines, t.T("reply.streak.none"))
	}
	if longestDays > 0 {
		lines = append(lines, t.T("reply.streak.ever", joinNames(t, longest), longestDays))
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
	if req.Player == "" {
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
	var matches []store.Player
	for _, p := range players {
		first, _, _ := strings.Cut(strings.ToLower(p.Name), " ")
		if first == want || strings.HasPrefix(strings.ToLower(p.Name), want) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
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
