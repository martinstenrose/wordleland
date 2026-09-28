package demo

import (
	"strings"

	"github.com/martinstenrose/wordleland/internal/wordle"
)

// GridDays is how many of the most recent days a backfill gives grids to.
// A real board has grids only from the day it started keeping them, so the
// demo does the same: the pages then show both kinds of day, and the note
// that says where the grids begin.
const GridDays = 7

// GridFor invents the squares a persona shared for an outcome: one row per
// guess, greener towards the end, the last row all green on a solve and no
// row all green on a miss.
//
// It draws from its own generator, keyed like DailyRNG, rather than the one
// that decided the outcome, so adding grids did not change which scores an
// existing seed produces.
func GridFor(key string, puzzleNo int, seed int64, o Outcome) wordle.Grid {
	rng := DailyRNG(key+"/grid", puzzleNo, seed)
	rows := o.Guesses
	if !o.Solved {
		rows = wordle.MaxGuesses
	}
	out := make([]string, rows)
	for r := range out {
		if o.Solved && r == rows-1 {
			out[r] = "ggggg"
			continue
		}
		green := float64(r+1) / float64(rows+1) * 0.8
		var row [5]byte
		for c := range row {
			switch u := rng.Float64(); {
			case u < green:
				row[c] = 'g'
			case u < green+0.22:
				row[c] = 'y'
			default:
				row[c] = 'n'
			}
		}
		if string(row[:]) == "ggggg" {
			row[rng.Intn(5)] = 'y'
		}
		out[r] = string(row[:])
	}
	return wordle.Grid(strings.Join(out, "/"))
}
