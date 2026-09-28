package reply

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// day is one puzzle for everyone: each result, and the group's average on
// it against its usual, on the daily recap's terms for hard and easy.
func day(t i18n.Translator, req Request, players []store.Player,
	results []store.BoardResult, now time.Time) string {

	date := now
	if req.Date != "" {
		date, _ = time.ParseInLocation(DateLayout, req.Date, now.Location())
	}
	puzzle := wordle.PuzzleForDate(date)
	label := t.T("reply.date", date.Day(), t.T("month."+strconv.Itoa(int(date.Month()))))
	if puzzle > wordle.PuzzleForDate(now) {
		return t.T("reply.score.future", label)
	}
	byName := map[int64]string{}
	for _, p := range players {
		byName[p.ID] = p.Name
	}
	var filed, before []store.BoardResult
	for _, r := range results {
		if _, ok := byName[r.PlayerID]; !ok {
			continue
		}
		switch {
		case r.PuzzleNo == puzzle:
			filed = append(filed, r)
		case r.PuzzleNo < puzzle:
			before = append(before, r)
		}
	}
	head := "📅 " + t.T("reply.day.head", i18n.Identifier(puzzle), label)
	if len(filed) == 0 {
		return head + "\n" + t.T("reply.day.none")
	}
	sort.Slice(filed, func(i, j int) bool {
		vi, vj := resultValue(filed[i]), resultValue(filed[j])
		if vi != vj {
			return vi < vj
		}
		return byName[filed[i].PlayerID] < byName[filed[j].PlayerID]
	})
	var scores []string
	for _, r := range filed {
		score := "X"
		if r.Solved {
			score = strconv.Itoa(r.Guesses)
		}
		if r.HardMode {
			score += "*"
		}
		scores = append(scores, byName[r.PlayerID]+" "+score)
	}
	lines := []string{head, strings.Join(scores, " · ")}

	opts := stats.DefaultOptions(now)
	mean, _ := stats.MeanScore(filed, opts)
	usual, n := stats.MeanScore(before, opts)
	switch {
	case len(filed) < stats.DayMinFiled || n < stats.FormWindow:
		// Too few results to speak for the puzzle, or too little history
		// for a usual: the average alone.
		lines = append(lines, t.T("reply.day.mean", t.Decimal(mean, 2)))
	case mean-usual >= stats.DayDelta:
		lines = append(lines, "🧱 "+t.T("reply.day.hard", t.Decimal(mean, 2), t.Decimal(usual, 2)))
	case usual-mean >= stats.DayDelta:
		lines = append(lines, "🪶 "+t.T("reply.day.easy", t.Decimal(mean, 2), t.Decimal(usual, 2)))
	default:
		lines = append(lines, t.T("reply.day.usual", t.Decimal(mean, 2), t.Decimal(usual, 2)))
	}
	return strings.Join(lines, "\n")
}

// puzzles is the hardest puzzle over a span, or with Worst the easiest:
// the day with the highest, or lowest, group average among days enough
// played for the average to be about the puzzle.
func puzzles(t i18n.Translator, req Request, players []store.Player,
	results []store.BoardResult, now time.Time) string {

	label, _ := standingOver(t, req, players, results, now)
	first, last := spanPuzzles(req, results, now)
	// Today is still being played: its average moves with every result.
	last = min(last, wordle.PuzzleForDate(now)-1)
	known := map[int64]bool{}
	for _, p := range players {
		known[p.ID] = true
	}
	type puzzleMean struct {
		puzzle, filed int
		mean          float64
	}
	var found *puzzleMean
	for puzzle, day := range dayResults(results, first, last) {
		sum, filed := 0, 0
		for id, v := range day {
			if known[id] {
				sum += v
				filed++
			}
		}
		if filed < stats.DayMinFiled {
			continue
		}
		mean := float64(sum) / float64(filed)
		better := found == nil ||
			(!req.Worst && mean > found.mean) || (req.Worst && mean < found.mean) ||
			// A tie goes to the more recent: the one people remember.
			(mean == found.mean && puzzle > found.puzzle)
		if better {
			found = &puzzleMean{puzzle, filed, mean}
		}
	}
	if found == nil {
		return t.T("reply.puzzles.none", capitalized(label))
	}
	date, _ := wordle.DateForPuzzle(found.puzzle)
	when := t.T("reply.date", date.Day(), t.T("month."+strconv.Itoa(int(date.Month()))))
	if req.Worst {
		return "🪶 " + t.T("reply.puzzles.easiest", capitalized(label), i18n.Identifier(found.puzzle), when,
			t.Decimal(found.mean, 2), found.filed)
	}
	return "🧱 " + t.T("reply.puzzles.hardest", capitalized(label), i18n.Identifier(found.puzzle), when,
		t.Decimal(found.mean, 2), found.filed)
}

// Weekday averages need this many results behind them: a player's day of
// the week, or the group's, is noise below it.
const (
	minWeekdayPlayer = 4
	minWeekdayGroup  = 10
)

// weekday is which day of the week is hardest and easiest: for the group,
// or for one player.
func weekday(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	known := map[int64]bool{}
	for _, p := range players {
		known[p.ID] = true
	}
	var who *store.Player
	need := minWeekdayGroup
	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		who, need = &p, minWeekdayPlayer
	}
	var sum, n [7]int
	for _, r := range results {
		if !known[r.PlayerID] || (who != nil && r.PlayerID != who.ID) {
			continue
		}
		d := r.Date.Weekday()
		sum[d] += resultValue(r)
		n[d]++
	}
	hardest, easiest := -1, -1
	var means [7]float64
	for d := range 7 {
		if n[d] < need {
			continue
		}
		means[d] = float64(sum[d]) / float64(n[d])
		if hardest < 0 || means[d] > means[hardest] {
			hardest = d
		}
		if easiest < 0 || means[d] < means[easiest] {
			easiest = d
		}
	}
	if hardest < 0 || hardest == easiest {
		if who != nil {
			return t.T("reply.weekday.player.few", who.Name)
		}
		return t.T("reply.weekday.few")
	}
	name := func(d int) string { return t.T("weekday." + strconv.Itoa(d)) }
	if who != nil {
		return "📆 " + t.T("reply.weekday.player", who.Name, name(easiest), t.Decimal(means[easiest], 2),
			name(hardest), t.Decimal(means[hardest], 2))
	}
	return "📆 " + t.T("reply.weekday", name(hardest), t.Decimal(means[hardest], 2),
		name(easiest), t.Decimal(means[easiest], 2))
}
