package demo

import (
	"hash/fnv"
	"math/rand"
)

// PersonaFor derives a persona's stable traits from their name alone.
//
// tick has to reproduce the same behaviour for an existing player run after
// run, without knowing the seed the roster was originally generated with
// and without persisting any new state to derive it from later. Hashing the
// name into a seed makes the traits a pure function of it.
func PersonaFor(name string) Persona {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	rng := rand.New(rand.NewSource(int64(h.Sum64())))

	p := Persona{Name: name}

	// A third of the roster plays hard mode almost exclusively; the rest
	// never do. Matches the split docs/decisions.md found in the real
	// history, rather than spreading hard-mode games evenly across a
	// player's own results.
	if rng.Float64() < 1.0/3.0 {
		p.HardModeRate = 0.87 + rng.Float64()*0.09
	}

	p.MissRate = 0.03 + rng.Float64()*0.15

	return p
}

// guessWeights centers the guess distribution on 4, with a small tail of
// failures. Index 0 is a failure; indices 1-6 are that many guesses.
// Weights sum to 100.
var guessWeights = [7]int{5, 1, 8, 25, 32, 20, 9}

// Outcome is one day's result for a player who played.
type Outcome struct {
	Solved   bool
	Guesses  int // 0 when Solved is false; storage records a failure as NULL.
	HardMode bool
}

// Played reports whether the persona has a result for one day of the
// backfill window, and what it is if so.
//
// day and totalDays are both zero-based, oldest to most recent, so
// totalDays-1 is the last day generated. RoleMissing needs that endpoint to
// stop playing well before it: the "Missing" callout only fires past
// AbsentDays (7) of real absence, so this persona is guaranteed to clear it
// by never playing the final third of the window, however wide it is.
func (p Persona) Played(rng *rand.Rand, day, totalDays int) bool {
	switch p.Role {
	case RoleUnbroken:
		return true
	case RoleMissing:
		return day < (totalDays*2)/3
	default:
		return rng.Float64() >= p.MissRate
	}
}

// Play samples one game's outcome for a persona who played.
func (p Persona) Play(rng *rand.Rand) Outcome {
	roll := rng.Intn(100)
	sum := 0
	bucket := 0
	for i, w := range guessWeights {
		sum += w
		if roll < sum {
			bucket = i
			break
		}
	}

	var out Outcome
	if bucket > 0 {
		out.Solved = true
		out.Guesses = bucket
	}
	out.HardMode = rng.Float64() < p.HardModeRate
	return out
}
