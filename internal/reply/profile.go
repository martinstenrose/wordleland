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
)

// monthLabel names a month as the answers do: the month, and its year when
// it is not this one.
func monthLabel(t i18n.Translator, year int, month time.Month, now time.Time) string {
	label := t.T("month." + strconv.Itoa(int(month)))
	if year != now.Year() {
		label += " " + strconv.Itoa(year)
	}
	return label
}

// reasonKey is the board's own words for why a player is not ranked.
func reasonKey(reason string) string {
	switch reason {
	case stats.ReasonInactive:
		return "board.reason.inactive"
	case stats.ReasonNoRecentGames:
		return "board.reason.noRecentGames"
	default:
		return "board.reason.lowData"
	}
}

// distribution is a player's scores as "12×2" pairs: the digits and the X
// read the same in every language.
func distribution(d [7]int) string {
	parts := make([]string, 0, len(d))
	for i, n := range d {
		digit := strconv.Itoa(i + 1)
		if i+1 == failGuesses {
			digit = "X"
		}
		parts = append(parts, strconv.Itoa(n)+"×"+digit)
	}
	return strings.Join(parts, ", ")
}

// profile is everything about one player in one answer: "tell me about
// Bo", and "roast Alma", whose material is the same figures — the trait
// the board gives them is the tease, and it is the board's, not made up.
func profile(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	p, ok, text := whom(t, req, asker, players)
	if !ok {
		return text
	}
	board := stats.Compute(players, results, stats.DefaultOptions(now))
	bp, _ := boardPlayer(board, p.ID)
	if bp.Games == 0 {
		return t.T("reply.profile.none", p.Name)
	}

	lines := []string{"👤 " + p.Name}
	if trait := stats.NewTraiter(board).For(bp); trait != "" {
		lines[0] += " — " + t.T("trait."+trait) + ": " + lowerFirst(t.T("trait."+trait+".why"))
	}
	if bp.Ranked() {
		lines = append(lines, t.T("reply.profile.board", bp.Rank, len(board.Ranked), t.Decimal(*bp.Average, 2), bp.Games))
	} else {
		lines = append(lines, t.T("reply.profile.unranked", t.T(reasonKey(bp.Reason)), bp.Games))
	}
	m := currentMonth(players, results, now)
	if mp, ok := rankedPlayer(m, p.ID); ok {
		lines = append(lines, t.T("reply.profile.month", capitalized(monthLabel(t, now.Year(), now.Month(), now)),
			mp.Rank, len(m.Ranked), t.Decimal(*mp.Average, 2)))
	}
	lines = append(lines, t.T("reply.profile.streak", bp.CurrentStreak, bp.LongestStreak))
	lines = append(lines, t.T("reply.profile.scores", distribution(bp.Distribution)))
	if bp.Form != nil {
		lines = append(lines, t.T("reply.profile.form", t.Decimal(*bp.Form, 2), stats.FormWindow))
	}
	season := stats.ComputeSeason(stats.ComputeMonths(players, results, stats.DefaultOptions(now)), now)
	for _, row := range season.Rows {
		if row.ID == p.ID && row.Wins > 0 {
			lines = append(lines, "🏆 "+winsOf(t, p.Name, row.Wins, false))
		}
	}
	return strings.Join(lines, "\n")
}

// lowerFirst lowercases a sentence's first letter, for a sentence used as
// a clause.
func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

// historyMonths is how many months a player's history lists: half a year
// reads in a chat, a lifetime does not.
const historyMonths = 6

// history is one player's months, newest first, and their best.
func history(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	p, ok, text := whom(t, req, asker, players)
	if !ok {
		return text
	}
	months := stats.ComputeMonths(players, results, stats.DefaultOptions(now))
	var marks []string
	for _, m := range months {
		mp, ok := rankedPlayer(m, p.ID)
		if !ok {
			continue
		}
		if len(marks) == historyMonths {
			break
		}
		mark := monthLabel(t, m.Year, m.Month, now) + " " + t.Decimal(*mp.Average, 2) +
			" (" + t.T("reply.history.place", mp.Rank) + ")"
		if isWinner(m, p.ID) && m.Complete(now) {
			mark += " 🏆"
		}
		marks = append(marks, mark)
	}
	if len(marks) == 0 {
		return t.T("reply.history.none", p.Name)
	}
	lines := []string{"📚 " + t.T("reply.history.head", p.Name), strings.Join(marks, " · ")}
	season := stats.ComputeSeason(months, now)
	for _, row := range season.Rows {
		if row.ID == p.ID && row.Best != nil {
			lines = append(lines, t.T("reply.history.best", monthLabel(t, row.BestYear, row.BestMonth, now),
				t.Decimal(*row.Best, 2)))
		}
	}
	return strings.Join(lines, "\n")
}

// records is the group's all-time records: the best month anyone has had,
// the closest finish, the longest streak, the most 1s and 2s, the most
// titles. Only finished months count for the month records: a month a few
// days old has an average nobody has had to hold.
func records(t i18n.Translator, players []store.Player, results []store.BoardResult, now time.Time) string {
	opts := stats.DefaultOptions(now)
	board := stats.Compute(players, results, opts)
	months := stats.ComputeMonths(players, results, opts)
	var lines []string

	var bestAvg *float64
	var bestWho []string
	var bestWhen string
	closest := math.Inf(1)
	var closestWhen string
	for _, m := range months {
		if !m.Complete(now) || len(m.Winners) == 0 {
			continue
		}
		avg := *m.Winners[0].Average
		label := monthLabel(t, m.Year, m.Month, now)
		if bestAvg == nil || avg < *bestAvg {
			bestAvg, bestWho, bestWhen = &avg, names(m.Winners), label
		}
		if m.Margin != nil && *m.Margin < closest {
			closest, closestWhen = *m.Margin, label
		}
	}
	if bestAvg != nil {
		lines = append(lines, "📉 "+t.T("reply.records.month", joinNames(t, bestWho), t.Decimal(*bestAvg, 2), bestWhen))
	}
	if closestWhen != "" {
		lines = append(lines, "🤏 "+t.T("reply.records.closest", closestWhen, int(math.Round(closest*100))))
	}
	all := append(append([]stats.Player(nil), board.Ranked...), board.Unranked...)
	if who, n := holders(all, func(p stats.Player) int { return p.LongestStreak }); n > 0 {
		lines = append(lines, "🔥 "+t.T("reply.records.streak", joinNames(t, who), t.TN("reply.days", n)))
	}
	for _, g := range []int{1, 2} {
		if who, n := holders(all, func(p stats.Player) int { return p.Distribution[g-1] }); n > 0 {
			lines = append(lines, "🎯 "+t.T("reply.count.most", t.T("reply.guess."+strconv.Itoa(g)), joinNames(t, who), n))
		}
	}
	season := stats.ComputeSeason(months, now)
	most := 0
	var champs []string
	for _, row := range season.Rows {
		switch {
		case row.Wins > most:
			most, champs = row.Wins, []string{row.Name}
		case row.Wins == most && most > 0:
			champs = append(champs, row.Name)
		}
	}
	if most > 0 {
		sort.Strings(champs)
		lines = append(lines, "🏆 "+t.T("reply.wins", joinNames(t, champs), most))
	}
	if len(lines) == 0 {
		return t.T("reply.records.none")
	}
	return strings.Join(append([]string{t.T("reply.records.head")}, lines...), "\n")
}

// group is the group as a whole: who plays, how much, and how well.
func group(t i18n.Translator, players []store.Player, results []store.BoardResult, now time.Time) string {
	opts := stats.DefaultOptions(now)
	board := stats.Compute(players, results, opts)
	active, games := 0, 0
	var dist [7]int
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			if p.Games > 0 {
				active++
			}
			games += p.Games
			for i, n := range p.Distribution {
				dist[i] += n
			}
		}
	}
	if games == 0 {
		return t.T("reply.group.none")
	}
	lines := []string{"👥 " + t.T("reply.group.head", active, games, board.Days)}
	mean, _ := stats.MeanScore(results, opts)
	line := t.T("reply.group.mean", t.Decimal(mean, 2))
	if m := currentMonth(players, results, now); m.GroupAverage != nil {
		line += " " + t.T("reply.group.month", monthLabel(t, now.Year(), now.Month(), now), t.Decimal(*m.GroupAverage, 2))
	}
	lines = append(lines, line)
	lines = append(lines, t.T("reply.group.scores", distribution(dist)))
	return strings.Join(lines, "\n")
}
