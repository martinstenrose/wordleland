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

	// of is how many of the asked-for score a player has: that score, or
	// with OrBetter that score and every better one.
	of := func(p stats.Player) int {
		if !req.OrBetter {
			return p.Distribution[req.Guesses-1]
		}
		n := 0
		for _, c := range p.Distribution[:req.Guesses] {
			n += c
		}
		return n
	}
	if req.OrBetter {
		if req.Player == "" {
			who, n := holders(all, of)
			if n == 0 {
				return t.T("reply.count.nobody", t.T("reply.count.orBetter.term", req.Guesses))
			}
			return t.T("reply.count.orBetter.most", req.Guesses, joinNames(t, who), n)
		}
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		bp, _ := boardPlayer(board, p.ID)
		if bp.Games == 0 {
			return t.T("reply.profile.none", p.Name)
		}
		n := of(bp)
		return t.T("reply.count.orBetter", p.Name, n, req.Guesses, bp.Games,
			int(math.Round(100*float64(n)/float64(bp.Games))))
	}

	if req.Player == "" && req.Guesses > 0 && req.Worst {
		// The fewest, among the ranked: somebody with three games has
		// few of everything, which is not what "who has the fewest X's"
		// asks.
		if len(board.Ranked) == 0 {
			return t.T("reply.count.nobody", t.T("reply.guess."+strconv.Itoa(req.Guesses)))
		}
		fewest := of(board.Ranked[0])
		for _, p := range board.Ranked {
			fewest = min(fewest, of(p))
		}
		var who []string
		for _, p := range board.Ranked {
			if of(p) == fewest {
				who = append(who, p.Name)
			}
		}
		sort.Strings(who)
		return t.T("reply.count.fewest", t.T("reply.guess."+strconv.Itoa(req.Guesses)), joinNames(t, who), fewest)
	}

	if req.Player == "" && req.Guesses > 0 {
		// Nobody in particular, whoever is asking: who has the most of
		// them. "How many 2s do I have" names the asker, by the prompt.
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
