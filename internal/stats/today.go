package stats

import (
	"sort"

	"github.com/martinstenrose/wordleland/internal/store"
)

// TodayEntry is one player's result for the current puzzle.
type TodayEntry struct {
	store.Player

	// Guesses is 0 for a failure; Solved says which.
	Guesses  int
	Solved   bool
	HardMode bool
}

// Today is the state of the current puzzle: who has filed, who has not, and
// the best score so far.
type Today struct {
	PuzzleNo int

	// Filed is ordered best first, ties broken by name so the order is
	// stable through the day rather than shuffling as rows arrive.
	Filed []TodayEntry

	// Missing lists active players with no result yet. Retired players are
	// left out: they are not expected, so listing them as absent would be
	// wrong every day forever.
	Missing []store.Player

	// Best is the lowest solved score, nil when nobody has solved it yet —
	// including when everyone who filed has failed.
	Best *TodayEntry

	// BestShared counts how many people hold that score. A tie is a real
	// outcome, and naming one of them would be picking a winner the day
	// does not have.
	BestShared int
}

// Filedcount and Expected let the view say "9 of 12" without recounting.
func (t Today) FiledCount() int { return len(t.Filed) }
func (t Today) Expected() int   { return len(t.Filed) + len(t.Missing) }

// ComputeToday reduces the history to the current puzzle.
//
// It reads the unfiltered history on purpose. Today is a factual question —
// who has posted — and answering it through the hard-mode filter would
// report a player as missing on a day they actually played.
func ComputeToday(players []store.Player, results []store.BoardResult, currentPuzzle int) Today {
	byPlayer := make(map[int64]store.BoardResult, len(players))
	for _, r := range results {
		if r.PuzzleNo == currentPuzzle {
			byPlayer[r.PlayerID] = r
		}
	}

	today := Today{PuzzleNo: currentPuzzle}
	for _, p := range players {
		r, filed := byPlayer[p.ID]
		if !filed {
			if p.Active {
				today.Missing = append(today.Missing, p)
			}
			continue
		}
		today.Filed = append(today.Filed, TodayEntry{
			Player: p, Guesses: r.Guesses, Solved: r.Solved, HardMode: r.HardMode,
		})
	}

	sort.Slice(today.Filed, func(i, j int) bool {
		a, b := today.Filed[i], today.Filed[j]
		if a.Solved != b.Solved {
			return a.Solved
		}
		if a.Solved && a.Guesses != b.Guesses {
			return a.Guesses < b.Guesses
		}
		return a.Name < b.Name
	})
	sort.Slice(today.Missing, func(i, j int) bool {
		return today.Missing[i].Name < today.Missing[j].Name
	})

	if len(today.Filed) > 0 && today.Filed[0].Solved {
		best := today.Filed[0]
		today.Best = &best
		for _, e := range today.Filed {
			if e.Solved && e.Guesses == best.Guesses {
				today.BestShared++
			}
		}
	}
	return today
}
