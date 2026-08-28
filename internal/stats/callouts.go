package stats

import (
	"sort"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

// Significance is the smallest form-versus-average gap worth remarking on.
//
// Without a floor the largest delta always wins and noise gets published as
// a finding, which is the failure is guarding against.
const Significance = 0.25

// AbsentDays is how long a regular has to be gone before it is worth saying.
const AbsentDays = 7

// Callout kinds. The view maps these to localised copy; nothing in this
// package produces a sentence.
const (
	CalloutUnbroken   = "unbroken"
	CalloutOneAndDone = "oneAndDone"
	CalloutOnForm     = "onForm"
	CalloutOffForm    = "offForm"
	CalloutMissing    = "missing"
)

// Callout is one generated observation, as data rather than prose.
type Callout struct {
	Kind string

	Name string
	Slug string

	// Value carries the figure that cleared the threshold: a delta, a
	// streak length, a day count. Its meaning depends on Kind.
	Value float64
	Count int

	// Since is set where the callout refers to a date.
	Since time.Time
}

// ComputeCallouts generates the observations that clear their thresholds.
//
// It returns only what is true. An empty result is a valid answer and the
// view omits the card rather than padding it: a quiet week should look
// quiet.
func ComputeCallouts(board Board, results []store.BoardResult, now time.Time) []Callout {
	var out []Callout

	if c, ok := unbroken(board); ok {
		out = append(out, c)
	}
	if c, ok := oneAndDone(board, results, board.CurrentPuzzle-FormWindow+1); ok {
		out = append(out, c)
	}
	if c, ok := onForm(board); ok {
		out = append(out, c)
	}
	if c, ok := offForm(board); ok {
		out = append(out, c)
	}
	if c, ok := missing(board, now); ok {
		out = append(out, c)
	}
	return out
}

// unbroken reports the longest running streak.
//
// The design asserts a streak reaches back to the start of the window
// without checking. Here the streak is required to be both the longest
// anybody currently holds and their own longest ever, so "still going" is
// a claim the data supports rather than an assumption about the window.
func unbroken(board Board) (Callout, bool) {
	var best *Player
	for i := range board.Ranked {
		p := &board.Ranked[i]
		if p.CurrentStreak < MinGames || p.CurrentStreak != p.LongestStreak {
			continue
		}
		if best == nil || p.CurrentStreak > best.CurrentStreak {
			best = p
		}
	}
	if best == nil {
		return Callout{}, false
	}
	return Callout{
		Kind: CalloutUnbroken, Name: best.Name, Slug: best.Slug,
		Count: best.CurrentStreak,
	}, true
}

// oneAndDone reports first-guess solves within the recent window.
//
// Two corrections to the design. It claims "the only" one without counting,
// so this counts them. And it looks at the whole history, where a
// first-guess solve stops being news: over a long enough run there are
// always some, and a callout that always fires is decoration. Bounding it
// to the form window is what keeps it a finding.
func oneAndDone(board Board, results []store.BoardResult, windowStart int) (Callout, bool) {
	names := make(map[int64]Player, len(board.Ranked)+len(board.Unranked))
	for _, group := range [][]Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			names[p.ID] = p
		}
	}

	var holders []Player
	seen := make(map[int64]bool)
	total := 0
	for _, r := range results {
		if !r.Solved || r.Guesses != 1 || r.PuzzleNo < windowStart {
			continue
		}
		total++
		if p, ok := names[r.PlayerID]; ok && !seen[p.ID] {
			seen[p.ID] = true
			holders = append(holders, p)
		}
	}
	if total == 0 {
		return Callout{}, false
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].Name < holders[j].Name })

	c := Callout{Kind: CalloutOneAndDone, Count: total}
	if len(holders) == 1 {
		c.Name, c.Slug = holders[0].Name, holders[0].Slug
	}
	return c, true
}

// onForm reports the best improvement against a player's own average.
func onForm(board Board) (Callout, bool) {
	p, delta, ok := extremeDelta(board, true)
	if !ok || delta > -Significance {
		return Callout{}, false
	}
	return Callout{Kind: CalloutOnForm, Name: p.Name, Slug: p.Slug, Value: -delta}, true
}

// offForm reports the worst slide against a player's own average.
func offForm(board Board) (Callout, bool) {
	p, delta, ok := extremeDelta(board, false)
	if !ok || delta < Significance {
		return Callout{}, false
	}
	return Callout{Kind: CalloutOffForm, Name: p.Name, Slug: p.Slug, Value: delta}, true
}

func extremeDelta(board Board, lowest bool) (Player, float64, bool) {
	var best Player
	var bestDelta float64
	found := false
	for _, p := range board.Ranked {
		if p.Delta == nil {
			continue
		}
		d := *p.Delta
		if !found || (lowest && d < bestDelta) || (!lowest && d > bestDelta) {
			best, bestDelta, found = p, d, true
		}
	}
	return best, bestDelta, found
}

// missing reports a regular who has stopped.
//
// It only fires for someone with a real history, so a newcomer who tried it
// once and drifted off is not announced as a loss.
func missing(board Board, now time.Time) (Callout, bool) {
	var best *Player
	for i := range board.Unranked {
		p := &board.Unranked[i]
		// A retired player is not missing, they left. Announcing them as
		// absent would be true forever and useful never.
		if !p.Active || p.Games <= MinGames || p.LastPlayed == nil || p.DaysSince < AbsentDays {
			continue
		}
		if best == nil || p.DaysSince > best.DaysSince {
			best = p
		}
	}
	if best == nil {
		return Callout{}, false
	}
	return Callout{
		Kind: CalloutMissing, Name: best.Name, Slug: best.Slug,
		Count: best.DaysSince, Since: *best.LastPlayed,
	}, true
}
