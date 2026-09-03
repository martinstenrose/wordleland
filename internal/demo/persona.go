package demo

import (
	"encoding/binary"
	"hash/fnv"
	"math/rand"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// PersonaFor derives a persona's stable traits from their name alone.
//
// tick has to reproduce the same behaviour for an existing player run after
// run, without knowing the seed the roster was originally generated with
// and without persisting any new state to derive it from later. Hashing the
// name into a seed makes the traits a pure function of it.
//
// Deriving from the name assumes it identifies the persona uniquely. A
// second `demo seed` run on top of an uncleared roster can produce a player
// sharing a name with an existing one — see docs/decisions.md's "Staging
// and demo data" — at which point the two become indistinguishable here.
// Nothing in this package resolves that; keeping the roster free of
// duplicate names is the caller's job.
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

// DailyRNG derives the random source for one persona's decision on one
// puzzle: whether they play at all, and if so what happens.
//
// tick must be safe to run more than once for the same puzzle — a cron
// misfire, a manual retry — and reproduce not just an already-filed result
// (ResultFor's job) but also an earlier decision to sit the day out, which
// leaves no row to check against. Keying the source on the player's name and
// the puzzle number, rather than the time the command happened to run,
// makes that decision a pure function of the two things that actually
// identify it: nothing changes unless one of them does. seed is a salt for
// tests that need a different simulated day without waiting for the puzzle
// number to change; leaving it at zero is what makes two ordinary
// invocations for the same puzzle agree.
func DailyRNG(name string, puzzleNo int, seed int64) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(puzzleNo))
	binary.LittleEndian.PutUint64(buf[8:], uint64(seed))
	_, _ = h.Write(buf[:])
	return rand.New(rand.NewSource(int64(h.Sum64())))
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
// AbsentDays (7) of real absence, so this persona stops at whichever comes
// first of the final third of the window or AbsentDays before the end —
// the latter is what keeps the guarantee true for a short --days rather
// than just the default 200, where the final third alone already clears
// it. Below stats.AbsentDays+1 total days there is no room left to
// guarantee: demoSeed's flag validation rejects --days that small.
func (p Persona) Played(rng *rand.Rand, day, totalDays int) bool {
	switch p.Role {
	case RoleUnbroken:
		return true
	case RoleMissing:
		cutoff := (totalDays * 2) / 3
		if last := totalDays - stats.AbsentDays; last < cutoff {
			cutoff = last
		}
		return day < cutoff
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
