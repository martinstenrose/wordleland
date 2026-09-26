package reply

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
)

// failGuesses is the Guesses value a count question uses for a failure:
// one past six, the index the board's distribution keeps failures at.
const failGuesses = 7

// count reads the board's distribution: how many 1s, 2s … 6s and X's each
// player has over their whole history, which is the one place a "how many"
// question has an answer.
func count(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	board := stats.Compute(players, results, stats.DefaultOptions(now))
	all := append(append([]stats.Player(nil), board.Ranked...), board.Unranked...)

	if req.Player == "" && asker == nil && req.Guesses > 0 {
		// Nobody in particular: who has the most of them.
		term := t.T("reply.guess." + strconv.Itoa(req.Guesses))
		best := 0
		for _, p := range all {
			best = max(best, p.Distribution[req.Guesses-1])
		}
		if best == 0 {
			return t.T("reply.count.nobody", term)
		}
		var holders []string
		for _, p := range all {
			if p.Distribution[req.Guesses-1] == best {
				holders = append(holders, p.Name)
			}
		}
		sort.Strings(holders)
		return t.T("reply.count.most", term, joinNames(t, holders), best)
	}

	p, ok, text := whom(t, req, asker, players)
	if !ok {
		return text
	}
	var bp stats.Player
	for _, cand := range all {
		if cand.ID == p.ID {
			bp = cand
		}
	}

	if req.Guesses == 0 {
		// The whole distribution, as "12×2" pairs: the digits and the X
		// read the same in every language, so no term is needed here.
		var parts []string
		for i, n := range bp.Distribution {
			digit := strconv.Itoa(i + 1)
			if i+1 == failGuesses {
				digit = "X"
			}
			parts = append(parts, strconv.Itoa(n)+"×"+digit)
		}
		return t.T("reply.count.all", p.Name, bp.Games, strings.Join(parts, ", "))
	}

	term := t.T("reply.guess." + strconv.Itoa(req.Guesses))
	n := bp.Distribution[req.Guesses-1]
	if n == 0 {
		return t.T("reply.count.none", p.Name, term, bp.Games)
	}
	share := int(math.Round(100 * float64(n) / float64(bp.Games)))
	return t.T("reply.count", p.Name, n, term, bp.Games, share)
}
