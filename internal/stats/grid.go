package stats

import (
	"sort"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

// GridSpan is the default range: recent enough to read, with the whole
// history a click away. The design offers exactly these two.
const GridSpan = 90

// GridCell is one player's outcome on one day. The zero value is "did not
// play", which is the absence of a result rather than a score of nothing.
type GridCell struct {
	Played   bool
	Solved   bool
	HardMode bool
	Guesses  int
}

// GridRow is one puzzle across every column.
type GridRow struct {
	PuzzleNo int
	Date     time.Time
	// Cells is aligned with Grid.Players, index for index.
	Cells []GridCell
}

// Grid is the whole history as a table.
type Grid struct {
	// Players are the columns, in name order.
	//
	// Not in rank order: the grid is a table to scan down, and ranking it
	// means the columns move whenever the range changes or somebody has a
	// good week. A reader looking for one person should find them in the
	// same place every time. The rank is still on the column, as a figure.
	Players []Player
	// Rows are newest first, which is where the interesting end is.
	Rows []GridRow
	// Total is how many puzzles the grid covers.
	Total int
	// Hidden counts players behind the inactive toggle.
	Hidden int
}

// GridWindow returns the results the grid will show, given a span in
// puzzles. Zero or less means all of them.
//
// Exported so a caller can rank the columns over the same window the cells
// come from: ranking a 150-day grid by a 30-day figure describes something
// the reader is not looking at.
func GridWindow(results []store.BoardResult, opts Options, span int) []store.BoardResult {
	if opts.HardModeOnly {
		results = filterHardMode(results)
	}
	if span <= 0 {
		return results
	}

	seen := map[int]bool{}
	var order []int
	for _, r := range results {
		if !seen[r.PuzzleNo] {
			seen[r.PuzzleNo] = true
			order = append(order, r.PuzzleNo)
		}
	}
	if len(order) <= span {
		return results
	}
	sort.Ints(order)

	cutoff := order[len(order)-span]
	kept := make([]store.BoardResult, 0, len(results))
	for _, r := range results {
		if r.PuzzleNo >= cutoff {
			kept = append(kept, r)
		}
	}
	return kept
}

// ComputeGrid lays the history out as days by players.
//
// A player with nothing in the window shown is left out unless
// showInactive: a column of nothing but blanks is noise, and it is the
// window that decides, not the whole history — the active flag is about
// membership rather than whether somebody has been playing.
// span bounds how many puzzles are shown; zero or less means all of them.
func ComputeGrid(board Board, results []store.BoardResult, opts Options, showInactive bool, span int) Grid {
	if opts.HardModeOnly {
		results = filterHardMode(results)
	}

	// Work out which puzzles the grid covers before deciding the columns:
	// a player is hidden for having nothing *here*, which is not the same
	// as having nothing ever. Somebody who stopped in the spring should not
	// be a column of blanks across a window they were never in.
	seen := map[int]bool{}
	var order []int
	for _, r := range results {
		if !seen[r.PuzzleNo] {
			seen[r.PuzzleNo] = true
			order = append(order, r.PuzzleNo)
		}
	}
	// Sorted here rather than trusting the caller. The store hands them
	// over in puzzle order, but a window taken from an unsorted list would
	// be an arbitrary set of days rather than the most recent ones.
	sort.Ints(order)

	var grid Grid
	grid.Total = len(order)

	window := order
	if span > 0 && len(order) > span {
		window = order[len(order)-span:]
	}
	inWindow := make(map[int]bool, len(window))
	for _, p := range window {
		inWindow[p] = true
	}

	played := map[int64]bool{}
	for _, r := range results {
		if inWindow[r.PuzzleNo] {
			played[r.PlayerID] = true
		}
	}

	// Inactive here means two things, and both belong behind the toggle:
	// somebody who has left the group, and somebody with
	// nothing in the range shown. Either way the column would be blanks.
	inactive := func(p Player) bool { return !p.Active || !played[p.ID] }

	for _, group := range [][]Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			if inactive(p) {
				grid.Hidden++
				if !showInactive {
					continue
				}
			}
			grid.Players = append(grid.Players, p)
		}
	}

	// Sorted on a folded name so case does not split the order. Byte order
	// puts a, a and o after z, which is what a Swedish reader expects and
	// the roster is Swedish; a locale-aware collator would be the answer if
	// that stops being true.
	sort.Slice(grid.Players, func(i, j int) bool {
		a, b := strings.ToLower(grid.Players[i].Name), strings.ToLower(grid.Players[j].Name)
		if a != b {
			return a < b
		}
		return grid.Players[i].ID < grid.Players[j].ID
	})

	column := make(map[int64]int, len(grid.Players))
	for i, p := range grid.Players {
		column[p.ID] = i
	}

	rows := make(map[int]*GridRow, len(window))
	for _, r := range results {
		i, ok := column[r.PlayerID]
		if !ok || !inWindow[r.PuzzleNo] {
			continue
		}
		row := rows[r.PuzzleNo]
		if row == nil {
			row = &GridRow{
				PuzzleNo: r.PuzzleNo,
				Date:     r.Date,
				Cells:    make([]GridCell, len(grid.Players)),
			}
			rows[r.PuzzleNo] = row
		}
		row.Cells[i] = GridCell{
			Played: true, Solved: r.Solved, HardMode: r.HardMode, Guesses: r.Guesses,
		}
	}

	// Newest first, which is where the interesting end is.
	for i := len(window) - 1; i >= 0; i-- {
		if row := rows[window[i]]; row != nil {
			grid.Rows = append(grid.Rows, *row)
		}
	}
	return grid
}

// GridRanking is the same columns in finishing order for the window the
// grid covers.
//
// The grid itself stays alphabetical — see Grid.Players for why — but the
// rail beside it is a leaderboard, and a leaderboard that is not in order
// is just a list. Splitting the two orders means the reader can find a
// person in the grid and read the standings next to it without either
// moving when the range changes.
//
// Unranked players keep the alphabetical order they arrived in and sit at
// the end: they have no place to sort into.
func GridRanking(players []Player) []Player {
	ranked := make([]Player, len(players))
	copy(ranked, players)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Ranked() != b.Ranked() {
			return a.Ranked()
		}
		if !a.Ranked() {
			return false
		}
		return a.Rank < b.Rank
	})
	return ranked
}
