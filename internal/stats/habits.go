package stats

import (
	"sort"

	"github.com/martinstenrose/wordleland/internal/store"
)

// HabitWindow is how many recent qualifying puzzles a posting habit is read
// from. Thirty is the form window: long enough that one week away does not
// erase a habit, short enough that a habit that has changed shows within a
// month.
const HabitWindow = 30

// HabitMinDays is how many qualifying puzzles the window must hold before
// "as usual" is a claim rather than a coincidence. Half of three days is
// not a habit.
const HabitMinDays = 10

// Habit is one player's record of being first (or last) to post.
type Habit struct {
	// Days counts the qualifying puzzles in the window on which this player
	// posted first (or last).
	Days int
	// Run counts consecutive qualifying puzzles, ending at the puzzle the
	// habits were computed through, on which this player was first (or
	// last). A day nobody's posting time is known for ends the run, as does
	// a day someone else was first: "five days in a row" is a calendar
	// claim, and it has to be true on the calendar.
	Run int
}

// PostingHabits is who tends to open the day and who tends to close it, as
// data rather than prose. Nothing here decides whether a habit is worth
// mentioning; the message does, with the thresholds above.
type PostingHabits struct {
	// Days counts the qualifying puzzles in the window: those with a
	// posting time on at least two results, so that there was an order.
	Days int
	// First and Last are keyed by player id.
	First, Last map[int64]Habit
}

// ComputePostingHabits reads the posting order of the last HabitWindow
// qualifying puzzles up to and including through.
//
// A puzzle qualifies when at least two of its results carry a posting time:
// one stamped result makes its player first and last at once, which is no
// order at all. Results without a time are invisible here, so a player who
// files by hand is neither first nor last, ever, rather than "last" because
// the row was entered late.
func ComputePostingHabits(results []store.BoardResult, through int) PostingHabits {
	type order struct{ first, last int64 }
	orders := make(map[int]order)

	byPuzzle := make(map[int][]store.BoardResult)
	for _, r := range results {
		if r.PostedAt != nil && r.PuzzleNo <= through {
			byPuzzle[r.PuzzleNo] = append(byPuzzle[r.PuzzleNo], r)
		}
	}
	var puzzles []int
	for puzzle, rows := range byPuzzle {
		if len(rows) < 2 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if !a.PostedAt.Equal(*b.PostedAt) {
				return a.PostedAt.Before(*b.PostedAt)
			}
			return a.PlayerID < b.PlayerID
		})
		orders[puzzle] = order{first: rows[0].PlayerID, last: rows[len(rows)-1].PlayerID}
		puzzles = append(puzzles, puzzle)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(puzzles)))
	if len(puzzles) > HabitWindow {
		puzzles = puzzles[:HabitWindow]
	}

	h := PostingHabits{Days: len(puzzles), First: map[int64]Habit{}, Last: map[int64]Habit{}}
	for _, puzzle := range puzzles {
		o := orders[puzzle]
		f := h.First[o.first]
		f.Days++
		h.First[o.first] = f
		l := h.Last[o.last]
		l.Days++
		h.Last[o.last] = l
	}

	// The runs belong to whoever holds the position on the through puzzle;
	// everybody else's is zero by definition.
	if o, ok := orders[through]; ok {
		f := h.First[o.first]
		f.Run = runOf(orders, through, func(x order) int64 { return x.first }, o.first)
		h.First[o.first] = f
		l := h.Last[o.last]
		l.Run = runOf(orders, through, func(x order) int64 { return x.last }, o.last)
		h.Last[o.last] = l
	}
	return h
}

// runOf walks back from through, a puzzle at a time, while the puzzle
// qualifies and pick(order) is player.
func runOf[T any](orders map[int]T, through int, pick func(T) int64, player int64) int {
	n := 0
	for puzzle := through; ; puzzle-- {
		o, ok := orders[puzzle]
		if !ok || pick(o) != player {
			return n
		}
		n++
	}
}
