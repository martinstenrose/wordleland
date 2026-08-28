// Package stats computes the board from players and results.
//
// It is pure: no database, no clock beyond the one it is given. Every rule
// lives here rather than in SQL, so each can be checked against a
// hand-built fixture instead of a seeded database.
package stats

import (
	"math"
	"sort"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// FormWindow is how many puzzles back "recent" reaches.
const FormWindow = 30

// MinGames is the number of games needed to be ranked, and the number
// needed within the form window for a form figure at all. Below it a player
// is listed separately with a reason rather than ranked on noise.
const MinGames = 10

// failedAsSeven is the value a failure takes when the count-X-as-7 toggle is
// on. Storage never holds it: the convention lives in computation only.
const failedAsSeven = 7

// Reasons a player is not ranked. The ladder is first-match-wins, ordered
// most-specific first.
const (
	ReasonInactive      = "inactive"        // no games at all
	ReasonNoRecentGames = "no recent games" // played, but nothing recent
	ReasonLowData       = "low data"        // fewer than MinGames in total
)

// Options are the board's two toggles plus the hard-mode filter.
type Options struct {
	// CountXAsSeven treats a failure as a 7 in averages. Default on.
	CountXAsSeven bool
	// CountMissed counts missed puzzles as failures, bounded to each
	// player's own window — first result to last. Default off.
	CountMissed bool
	// HardModeOnly restricts averages and distributions to hard-mode games.
	HardModeOnly bool
	// Now fixes "today", and therefore where the form window ends.
	Now time.Time
}

// DefaultOptions matches's stated defaults.
func DefaultOptions(now time.Time) Options {
	return Options{CountXAsSeven: true, CountMissed: false, Now: now}
}

// Player is one row of the board.
type Player struct {
	store.Player

	Games int
	// Average over every counted game. Nil when there is nothing to average.
	Average *float64
	// Form is the average over the last FormWindow puzzles, nil unless the
	// player has at least MinGames within it.
	Form      *float64
	FormGames int
	// Delta is Form minus Average, nil unless both exist.
	Delta *float64

	// Spread is the standard deviation of the counted scores: how steady a
	// player is, as distinct from how good. Nil below two games, where
	// there is no spread to speak of.
	Spread *float64

	// Streaks are always computed from the unfiltered history: see
	// computeStreaks.
	CurrentStreak int
	LongestStreak int

	// Distribution counts guesses 1..6 at indices 0..5, with failures at
	// index 6.
	Distribution [7]int

	// HardModeGames is the count in the unfiltered history, so the share can
	// be shown even on the unfiltered board.
	HardModeGames int

	LastPlayed *time.Time
	DaysSince  int

	// Series is the last FormWindow puzzles, oldest first, for the
	// sparkline. A zero entry means no game that day.
	Series []float64

	// Rank is 1-based among ranked players, 0 when not ranked.
	Rank int
	// Reason is empty when ranked.
	Reason string
}

// Ranked reports whether the player made the ranked table.
func (p Player) Ranked() bool { return p.Rank > 0 }

// Board is everything the board template needs.
type Board struct {
	Ranked   []Player
	Unranked []Player

	// GroupSeries is the pooled daily average across all counted games, for
	// the dashed comparison line. A zero entry means nobody played.
	GroupSeries []float64

	// CurrentPuzzle is the puzzle "today" maps to.
	CurrentPuzzle int

	// Days counts the distinct puzzles anybody has played, which is the
	// group's history rather than the calendar's.
	Days int

	// ExcludedByFilter counts players left out entirely because they have no
	// games in the current filter. Reported so the omission is visible.
	ExcludedByFilter int

	Options Options
}

// Compute reduces players and results to a board.
func Compute(players []store.Player, results []store.BoardResult, opts Options) Board {
	current := wordle.PuzzleForDate(opts.Now)
	played := make(map[int]bool, len(results))
	for _, r := range results {
		played[r.PuzzleNo] = true
	}

	windowStart := current - FormWindow + 1

	// Indexed by player, in puzzle order, from the whole history. The
	// filtered view is derived from this rather than replacing it, because
	// some figures must not see the filter at all.
	byPlayer := make(map[int64][]store.BoardResult, len(players))
	for _, r := range results {
		byPlayer[r.PlayerID] = append(byPlayer[r.PlayerID], r)
	}

	board := Board{CurrentPuzzle: current, Days: len(played), Options: opts}
	pooled := make([]scoreAccumulator, FormWindow)

	for _, sp := range players {
		history := byPlayer[sp.ID]

		// Filtering changes which games count toward averages and
		// distributions. It must not change anything about absence — see
		// computeStreaks.
		counted := history
		if opts.HardModeOnly {
			counted = filterHardMode(history)
		}

		// A player with nothing in the current filter is not on this board
		// at all. Under hard mode they are not ranked low; they play a
		// different game.
		if opts.HardModeOnly && len(counted) == 0 {
			board.ExcludedByFilter++
			continue
		}

		p := Player{Player: sp, Games: len(counted)}
		p.HardModeGames = countHardMode(history)
		for _, r := range counted {
			p.Distribution[distIndex(r)]++
		}

		values := countedValues(counted, history, opts)
		if avg := mean(values); avg != nil {
			p.Average = avg
			p.Spread = spread(values, *avg)
		}

		formValues, formGames, series := windowValues(counted, opts, windowStart, current)
		p.FormGames = formGames
		p.Series = series
		if formGames >= MinGames {
			p.Form = mean(formValues)
			if p.Form != nil && p.Average != nil {
				d := *p.Form - *p.Average
				p.Delta = &d
			}
		}

		// Streaks read the unfiltered history on purpose.
		p.CurrentStreak, p.LongestStreak = computeStreaks(history, current)

		if len(history) > 0 {
			last := history[len(history)-1]
			p.LastPlayed = &last.Date
			p.DaysSince = current - last.PuzzleNo
		}

		// Eligibility is judged on the unfiltered history too, so a reason
		// means the same thing whatever the toggles say.
		p.Reason = eligibility(history, windowStart)

		for i, v := range series {
			if v > 0 {
				pooled[i].add(v)
			}
		}

		if p.Reason == "" {
			board.Ranked = append(board.Ranked, p)
		} else {
			board.Unranked = append(board.Unranked, p)
		}
	}

	board.GroupSeries = make([]float64, FormWindow)
	for i, acc := range pooled {
		board.GroupSeries[i] = acc.mean()
	}

	sortRanked(board.Ranked)
	sortUnranked(board.Unranked)
	for i := range board.Ranked {
		board.Ranked[i].Rank = i + 1
	}
	return board
}

// eligibility applies's ladder against the unfiltered history.
func eligibility(history []store.BoardResult, windowStart int) string {
	if len(history) == 0 {
		return ReasonInactive
	}
	var recent int
	for _, r := range history {
		if r.PuzzleNo >= windowStart {
			recent++
		}
	}
	switch {
	case recent == 0:
		return ReasonNoRecentGames
	case len(history) < MinGames:
		return ReasonLowData
	default:
		return ""
	}
}

// computeStreaks walks the unfiltered history.
//
// Deliberately unfiltered, and this is not an oversight to tidy up later. A
// streak is a statement about absence, and filtering manufactures absences:
// a player who plays hard mode in 169 of 178 games would have those nine
// ordinary games read as days they did not play, breaking the streak nine
// times for games they actually turned up for. Averages and distributions
// are a question about a population and filter correctly; this is not.
func computeStreaks(history []store.BoardResult, current int) (currentStreak, longest int) {
	if len(history) == 0 {
		return 0, 0
	}

	solved := make(map[int]bool, len(history))
	for _, r := range history {
		solved[r.PuzzleNo] = r.Solved
	}

	first := history[0].PuzzleNo
	var run, beforeToday int
	for puzzle := first; puzzle <= current; puzzle++ {
		// The run as it stood before today, which is what still counts
		// while today is unfinished.
		if puzzle == current {
			beforeToday = run
		}

		ok, played := solved[puzzle]
		// A failure and a missed day both break it.
		if played && ok {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}

	// Today only breaks a streak once it has actually been failed. Until
	// somebody files, the day is not a missed one — it is a day still
	// going, and reporting a long run as "—" all morning is simply wrong.
	if _, playedToday := solved[current]; !playedToday {
		return beforeToday, longest
	}
	return run, longest
}

// countedValues turns results into the numbers an average is taken over.
//
// counted is the filtered set the average is over; history is the whole of
// it, used only to decide what counts as an absence.
func countedValues(counted, history []store.BoardResult, opts Options) []float64 {
	values := make([]float64, 0, len(counted))
	for _, r := range counted {
		if v, ok := value(r, opts); ok {
			values = append(values, v)
		}
	}
	if opts.CountMissed {
		values = append(values, missedValues(counted, history, opts)...)
	}
	return values
}

// missedValues counts puzzles a player skipped, bounded to their own active
// window: first result to last, never all puzzles ever. Outside that
// window a player who joined late or dropped off would accumulate misses for
// a period they were never part of, and the average would say nothing.
//
// Whether a puzzle was played is judged against the unfiltered history, for
// the same reason streaks are: a filter must not manufacture absences. Under
// the hard-mode filter an ordinary game sits in the middle of a run — it is
// outside the population being averaged, but it is emphatically not a day
// the player failed to turn up, and scoring it as a miss would be a lie
// about their attendance.
func missedValues(counted, history []store.BoardResult, opts Options) []float64 {
	if len(counted) < 2 || !opts.CountXAsSeven {
		// With X not counted as 7 there is no number a miss could take
		// either, so the toggle has nothing to add.
		return nil
	}
	played := make(map[int]bool, len(history))
	for _, r := range history {
		played[r.PuzzleNo] = true
	}

	first, last := counted[0].PuzzleNo, counted[len(counted)-1].PuzzleNo
	var missed []float64
	for puzzle := first; puzzle <= last; puzzle++ {
		if !played[puzzle] {
			missed = append(missed, failedAsSeven)
		}
	}
	return missed
}

// windowValues returns the values inside the form window, how many games
// they came from, and the series the sparkline draws.
func windowValues(counted []store.BoardResult, opts Options, windowStart, current int) ([]float64, int, []float64) {
	series := make([]float64, FormWindow)
	var (
		values []float64
		games  int
	)
	for _, r := range counted {
		if r.PuzzleNo < windowStart || r.PuzzleNo > current {
			continue
		}
		games++
		v, ok := value(r, opts)
		if !ok {
			continue
		}
		values = append(values, v)
		series[r.PuzzleNo-windowStart] = v
	}
	return values, games, series
}

// value maps a result to its number, reporting whether it counts at all.
func value(r store.BoardResult, opts Options) (float64, bool) {
	if r.Solved {
		return float64(r.Guesses), true
	}
	if opts.CountXAsSeven {
		return failedAsSeven, true
	}
	// With the toggle off a failure is excluded rather than scored.
	return 0, false
}

func distIndex(r store.BoardResult) int {
	if !r.Solved {
		return 6
	}
	return r.Guesses - 1
}

func filterHardMode(history []store.BoardResult) []store.BoardResult {
	out := make([]store.BoardResult, 0, len(history))
	for _, r := range history {
		if r.HardMode {
			out = append(out, r)
		}
	}
	return out
}

func countHardMode(history []store.BoardResult) int {
	var n int
	for _, r := range history {
		if r.HardMode {
			n++
		}
	}
	return n
}

// sortRanked orders by average ascending, since lower is better. Ties fall
// back to more games, then to slug so the order is stable across reloads.
func sortRanked(players []Player) {
	sort.SliceStable(players, func(i, j int) bool {
		a, b := players[i], players[j]
		switch {
		case a.Average == nil && b.Average == nil:
			return a.Slug < b.Slug
		case a.Average == nil:
			return false
		case b.Average == nil:
			return true
		case *a.Average != *b.Average:
			return *a.Average < *b.Average
		case a.Games != b.Games:
			return a.Games > b.Games
		default:
			return a.Slug < b.Slug
		}
	})
}

// sortUnranked puts the most-played first: they are the nearest to being
// ranked and the most interesting to a reader.
func sortUnranked(players []Player) {
	sort.SliceStable(players, func(i, j int) bool {
		if players[i].Games != players[j].Games {
			return players[i].Games > players[j].Games
		}
		return players[i].Slug < players[j].Slug
	})
}

type scoreAccumulator struct {
	sum   float64
	count int
}

func (a *scoreAccumulator) add(v float64) {
	a.sum += v
	a.count++
}

func (a scoreAccumulator) mean() float64 {
	if a.count == 0 {
		return 0
	}
	return a.sum / float64(a.count)
}

// spread is the population standard deviation of the counted scores.
//
// Population rather than sample: these are all the games played, not a
// sample drawn from a larger set, so there is nothing to correct for.
func spread(values []float64, avg float64) *float64 {
	if len(values) < 2 {
		return nil
	}
	var sum float64
	for _, v := range values {
		d := v - avg
		sum += d * d
	}
	sd := math.Sqrt(sum / float64(len(values)))
	return &sd
}

func mean(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	m := sum / float64(len(values))
	return &m
}
