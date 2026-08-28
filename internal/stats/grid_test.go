package stats

import (
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

func gridSlugs(g Grid) []string {
	out := make([]string, 0, len(g.Players))
	for _, p := range g.Players {
		out = append(out, p.Slug)
	}
	return out
}

func TestGridLaysOutDaysByPlayers(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse")}
	results := []store.BoardResult{
		result(1, 1898, 3, false),
		result(2, 1898, 4, false),
		result(1, 1899, 2, true),
		// bosse missed 1899
		result(2, 1900, 5, false),
	}

	board := Compute(players, results, DefaultOptions(today(t)))
	grid := ComputeGrid(board, results, DefaultOptions(today(t)), false, 0)

	if len(grid.Players) != 2 {
		t.Fatalf("columns = %d, want 2", len(grid.Players))
	}
	if grid.Rows[0].PuzzleNo != 1900 {
		t.Errorf("first row is puzzle %d, want the newest", grid.Rows[0].PuzzleNo)
	}
	if grid.Rows[len(grid.Rows)-1].PuzzleNo != 1898 {
		t.Errorf("last row is puzzle %d, want the oldest", grid.Rows[len(grid.Rows)-1].PuzzleNo)
	}

	col := map[string]int{}
	for i, p := range grid.Players {
		col[p.Slug] = i
	}
	for _, row := range grid.Rows {
		if row.PuzzleNo != 1899 {
			continue
		}
		alma := row.Cells[col["alma"]]
		bosse := row.Cells[col["bosse"]]
		if !alma.Played || alma.Guesses != 2 || !alma.HardMode {
			t.Errorf("alma's 1899 cell = %+v, want a hard-mode 2", alma)
		}
		if bosse.Played {
			t.Errorf("bosse's 1899 cell = %+v, want a blank", bosse)
		}
	}
}

// Inactive means two things and both belong behind the toggle: somebody who
// has left the group, and somebody with nothing in the range shown. Either
// way the column would be blanks.
func TestGridHidesInactivePlayers(t *testing.T) {
	left := player(2, "left")
	left.Active = false
	players := []store.Player{player(1, "current"), left, player(3, "never")}

	results := run(1, 1871, 1900, 3, false)
	// The retired player was playing right up to the end, so it is the flag
	// hiding them rather than an absence of games.
	results = append(results, run(2, 1871, 1900, 4, false)...)

	board := Compute(players, results, DefaultOptions(today(t)))
	opts := DefaultOptions(today(t))

	shown := ComputeGrid(board, results, opts, false, 0)
	if len(shown.Players) != 1 || shown.Players[0].Slug != "current" {
		t.Errorf("columns = %v, want only the active player with games", gridSlugs(shown))
	}
	if shown.Hidden != 2 {
		t.Errorf("Hidden = %d, want 2 — one retired, one who never played", shown.Hidden)
	}

	all := ComputeGrid(board, results, opts, true, 0)
	if len(all.Players) != 3 {
		t.Errorf("columns = %v, want everybody when inactive are shown", gridSlugs(all))
	}
}

// Every day is shown: the design's grid is the whole history, and a cap
// would quietly hide the older end of it.
func TestGridShowsEveryDay(t *testing.T) {
	players := []store.Player{player(1, "alma")}
	results := run(1, 1700, 1900, 3, false)

	board := Compute(players, results, DefaultOptions(today(t)))
	grid := ComputeGrid(board, results, DefaultOptions(today(t)), false, 0)

	if len(grid.Rows) != grid.Total {
		t.Errorf("rows = %d for %d puzzles, want every day", len(grid.Rows), grid.Total)
	}
	if grid.Rows[0].PuzzleNo != 1900 {
		t.Errorf("first row = %d, want the newest puzzle", grid.Rows[0].PuzzleNo)
	}
	if last := grid.Rows[len(grid.Rows)-1].PuzzleNo; last != 1700 {
		t.Errorf("last row = %d, want the oldest puzzle", last)
	}
}

// Filtering changes the population here as everywhere else.
func TestGridHonoursTheHardModeFilter(t *testing.T) {
	players, results := realisticRoster(t)
	opts := DefaultOptions(today(t))
	opts.HardModeOnly = true

	board := Compute(players, results, opts)
	grid := ComputeGrid(board, results, opts, false, 0)

	for _, p := range grid.Players {
		if p.Slug[:4] == "norm" {
			t.Errorf("%s has no hard-mode games but is a column", p.Slug)
		}
	}
	for _, row := range grid.Rows {
		if row.PuzzleNo != 1885 {
			continue
		}
		for i, c := range row.Cells {
			if c.Played {
				t.Errorf("%s has a cell on the normal-mode day", grid.Players[i].Slug)
			}
		}
	}
}

// The two ranges the design offers: the recent stretch, and everything.
func TestGridSpanBoundsTheRange(t *testing.T) {
	players := []store.Player{player(1, "alma")}
	results := run(1, 1900-GridSpan*2, 1900, 3, false)

	board := Compute(players, results, DefaultOptions(today(t)))
	opts := DefaultOptions(today(t))

	recent := ComputeGrid(board, results, opts, false, GridSpan)
	if len(recent.Rows) != GridSpan {
		t.Errorf("rows = %d, want %d", len(recent.Rows), GridSpan)
	}
	if recent.Rows[0].PuzzleNo != 1900 {
		t.Errorf("first row = %d, want the newest", recent.Rows[0].PuzzleNo)
	}
	// Total counts the whole history whichever range is shown, so the
	// control can offer "All N days" honestly.
	if recent.Total != GridSpan*2+1 {
		t.Errorf("Total = %d, want the full count", recent.Total)
	}

	all := ComputeGrid(board, results, opts, false, 0)
	if len(all.Rows) != all.Total {
		t.Errorf("rows = %d for %d puzzles", len(all.Rows), all.Total)
	}
}
