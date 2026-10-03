package stats

import (
	"slices"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// gridResult is a solved result with its grid, the score read off the grid.
func gridResult(player int64, puzzle int, grid string) store.BoardResult {
	return store.BoardResult{
		PlayerID: player, PuzzleNo: puzzle, Solved: true,
		Guesses: len(wordle.Grid(grid).Rows()), Grid: grid,
	}
}

func TestDayBadges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		day  []store.BoardResult
		want map[int64][]string
	}{
		{
			name: "twins",
			day: []store.BoardResult{
				gridResult(1, 1, "nynnn/ngygn/ggggg"),
				gridResult(2, 1, "nynnn/ngygn/ggggg"),
				gridResult(3, 1, "nnnyn/nyggn/ggggg"),
			},
			want: map[int64][]string{1: {BadgeTwins}, 2: {BadgeTwins}},
		},
		{
			name: "two-row twins are a coincidence",
			day: []store.BoardResult{
				gridResult(1, 1, "nynnn/ggggg"),
				gridResult(2, 1, "nynnn/ggggg"),
			},
			want: map[int64][]string{},
		},
		{
			name: "mirror",
			day: []store.BoardResult{
				gridResult(1, 1, "nynnn/ngygn/ggggg"),
				gridResult(2, 1, "nnnyn/ngygn/ggggg"),
			},
			want: map[int64][]string{1: {BadgeMirror}, 2: {BadgeMirror}},
		},
		{
			// A symmetric grid is its own mirror; two of them are twins.
			name: "symmetric twins are twins, not a mirror",
			day: []store.BoardResult{
				gridResult(1, 1, "nynyn/ngygn/ggggg"),
				gridResult(2, 1, "nynyn/ngygn/ggggg"),
			},
			want: map[int64][]string{1: {BadgeTwins}, 2: {BadgeTwins}},
		},
		{
			name: "same opening",
			day: []store.BoardResult{
				gridResult(1, 1, "ynnnn/ggggg"),
				gridResult(2, 1, "ynnnn/ngnyn/ggggg"),
				gridResult(3, 1, "ynnnn/nggyn/ggggg"),
			},
			want: map[int64][]string{1: {BadgeSameOpening}, 2: {BadgeSameOpening}, 3: {BadgeSameOpening}},
		},
		{
			name: "a result without a grid leaves the opening unknown",
			day: []store.BoardResult{
				gridResult(1, 1, "ynnnn/ggggg"),
				gridResult(2, 1, "ynnnn/ngnyn/ggggg"),
				gridResult(3, 1, "ynnnn/nggyn/ggggg"),
				{PlayerID: 4, PuzzleNo: 1, Solved: true, Guesses: 4},
			},
			want: map[int64][]string{},
		},
		{
			name: "a grid's own badges come along",
			day: []store.BoardResult{
				gridResult(1, 1, "gynnn/gnynn/gnnyn/ggggg"),
				gridResult(2, 1, "gynnn/gnynn/gnnyn/ggggg"),
			},
			want: map[int64][]string{
				1: {wordle.BadgeStaircase, BadgeTwins},
				2: {wordle.BadgeStaircase, BadgeTwins},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DayBadges(tt.day)
			if len(got) != len(tt.want) {
				t.Fatalf("DayBadges = %v, want %v", got, tt.want)
			}
			for id, want := range tt.want {
				if !slices.Equal(got[id], want) {
					t.Errorf("player %d: %v, want %v", id, got[id], want)
				}
			}
		})
	}
}

func TestTallyBadges(t *testing.T) {
	t.Parallel()
	results := []store.BoardResult{
		gridResult(1, 10, "nnnnn/ggggg"),
		gridResult(1, 12, "nnnnn/ggggg"),
		gridResult(1, 11, "yyyyy/ggggg"),
		gridResult(2, 12, "nnnnn/ggggg"),
	}
	tallies := TallyBadges(ComputeBadges(results), 1)
	if len(tallies) != len(Badges) {
		t.Fatalf("%d tallies, want one per badge (%d)", len(tallies), len(Badges))
	}
	got := map[string]BadgeTally{}
	for _, tally := range tallies {
		got[tally.Badge] = tally
	}
	if g := got[wordle.BadgeFromNothing]; g.Count != 2 || g.Last != 12 {
		t.Errorf("from nothing = %+v, want twice, last on 12", g)
	}
	if g := got[wordle.BadgeAnagram]; g.Count != 1 || g.Last != 11 {
		t.Errorf("anagram = %+v, want once, on 11", g)
	}
	if g := got[wordle.BadgeRoyalStaircase]; g.Count != 0 || g.Last != 0 {
		t.Errorf("royal staircase = %+v, want never", g)
	}
}

func TestOpenings(t *testing.T) {
	t.Parallel()
	results := []store.BoardResult{
		gridResult(1, 1, "nnnnn/ggggg"),
		gridResult(1, 2, "nnnnn/ynnnn/ggggg"),
		gridResult(1, 3, "ynnnn/ggggg"),
		{PlayerID: 1, PuzzleNo: 4, Solved: true, Guesses: 3},
		gridResult(2, 1, "nnnyn/ggggg"),
	}
	if got := Openings(results, 1); got != 2 {
		t.Errorf("Openings = %d, want 2", got)
	}
}
