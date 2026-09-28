package reply

import (
	"sort"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
)

// days is every puzzle's results in a span, as the value each player
// scored: guesses, or 7 for a failure, as every average counts one.
type days map[int]map[int64]int

func dayResults(results []store.BoardResult, first, last int) days {
	out := days{}
	for _, r := range results {
		if r.PuzzleNo < first || r.PuzzleNo > last {
			continue
		}
		if out[r.PuzzleNo] == nil {
			out[r.PuzzleNo] = map[int64]int{}
		}
		out[r.PuzzleNo][r.PlayerID] = resultValue(r)
	}
	return out
}

// resultValue is a result as an average counts it.
func resultValue(r store.BoardResult) int {
	if !r.Solved {
		return failGuesses
	}
	return r.Guesses
}

// dominant is the share of shared days one player must have won for the
// answer to say the duel is theirs: two in three.
const dominant = 2.0 / 3

// versus is two players head to head over a span: where each sits in it,
// and on the days both played, who scored better.
func versus(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	a, b, ok, text := pair(t, req, asker, players)
	if !ok {
		return text
	}
	label, m := standingOver(t, req, players, results, now)
	lines := []string{"⚔️ " + t.T("reply.versus.head", a.Name, b.Name, label)}

	var standing []string
	for _, p := range []store.Player{a, b} {
		if mp, ok := rankedPlayer(m, p.ID); ok {
			standing = append(standing, t.T("reply.versus.place", p.Name, t.Decimal(*mp.Average, 2), mp.Rank))
		} else {
			standing = append(standing, t.T("reply.versus.unplaced", p.Name))
		}
	}
	lines = append(lines, strings.Join(standing, " · "))

	first, last := spanPuzzles(req, results, now)
	var aWins, bWins, draws int
	for _, day := range dayResults(results, first, last) {
		av, aok := day[a.ID]
		bv, bok := day[b.ID]
		switch {
		case !aok || !bok:
		case av < bv:
			aWins++
		case bv < av:
			bWins++
		default:
			draws++
		}
	}
	shared := aWins + bWins + draws
	if shared == 0 {
		lines = append(lines, t.T("reply.versus.never"))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, t.T("reply.versus.days", a.Name, aWins, b.Name, bWins, draws, shared))
	switch {
	case float64(aWins) >= dominant*float64(shared):
		lines = append(lines, t.T("reply.versus.owns", a.Name))
	case float64(bWins) >= dominant*float64(shared):
		lines = append(lines, t.T("reply.versus.owns", b.Name))
	case aWins == bWins:
		lines = append(lines, t.T("reply.versus.even"))
	}
	return strings.Join(lines, "\n")
}

// pair is the two players of a versus question: the two named, or the one
// named against the asker.
func pair(t i18n.Translator, req Request, asker *store.Player, players []store.Player) (store.Player, store.Player, bool, string) {
	first, second := req.Player, req.Other
	if second == "" {
		first, second = "", first
	}
	a, ok, text := whom(t, Request{Player: first}, asker, players)
	if !ok {
		return store.Player{}, store.Player{}, false, text
	}
	b, ok, text := whom(t, Request{Player: second}, nil, players)
	if !ok {
		return store.Player{}, store.Player{}, false, text
	}
	if a.ID == b.ID {
		return store.Player{}, store.Player{}, false, t.T("reply.versus.who")
	}
	return a, b, true, ""
}

// minDayField is how many must have played a day for its best score to be
// a win: alone, a player beats nobody.
const minDayField = 2

// dayWins is who has had the day's best score most often over a span,
// ties shared: every day a small competition of its own.
func dayWins(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	label, _ := standingOver(t, req, players, results, now)
	first, last := spanPuzzles(req, results, now)
	wins := map[int64]int{}
	played := map[int64]int{}
	contested := 0
	for _, day := range dayResults(results, first, last) {
		for id := range day {
			played[id]++
		}
		if len(day) < minDayField {
			continue
		}
		best := failGuesses
		for _, v := range day {
			best = min(best, v)
		}
		if best == failGuesses {
			// Everyone failed it: nobody won that day.
			continue
		}
		contested++
		for id, v := range day {
			if v == best {
				wins[id]++
			}
		}
	}

	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		return "🥇 " + t.T("reply.daywins.player", capitalized(label), p.Name, wins[p.ID], played[p.ID])
	}
	if contested == 0 {
		return t.T("reply.daywins.none", capitalized(label))
	}
	byName := map[int64]string{}
	for _, p := range players {
		byName[p.ID] = p.Name
	}
	type row struct {
		name string
		wins int
	}
	var rows []row
	for id, n := range wins {
		if name, ok := byName[id]; ok && n > 0 {
			rows = append(rows, row{name, n})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].wins != rows[j].wins {
			return rows[i].wins > rows[j].wins
		}
		return rows[i].name < rows[j].name
	})
	var top, rest []string
	for _, r := range rows {
		if r.wins == rows[0].wins {
			top = append(top, r.name)
		} else if len(rest) < 4 {
			rest = append(rest, r.name+" ("+t.Integer(r.wins)+")")
		}
	}
	line := "🥇 " + t.T("reply.daywins", capitalized(label), joinNames(t, top), rows[0].wins, contested)
	if len(rest) > 0 {
		line += " " + t.T("reply.wins.then", strings.Join(rest, ", "))
	}
	return line
}
