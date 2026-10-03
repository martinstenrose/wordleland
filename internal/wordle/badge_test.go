package wordle

import (
	"slices"
	"testing"
)

func TestBadges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		grid Grid
		want []string
	}{
		{
			// The example the badge was asked for by: the yellow walks
			// right while the green stays put.
			name: "staircase",
			grid: "gynnn/gnynn/gnnyn/ggggg",
			want: []string{BadgeStaircase},
		},
		{
			name: "staircase walking left, in green",
			grid: "nnnng/nnngn/nngnn/ggggg",
			want: []string{BadgeStaircase, BadgeSeaOfGreens},
		},
		{
			name: "two steps is not a staircase",
			grid: "nynnn/nnynn/ggggg",
			want: []string{BadgeLateBloomer},
		},
		{
			name: "a step that turns back is not a staircase",
			grid: "ynnnn/nynnn/ynnnn/ggggg",
			want: []string{BadgeLateBloomer},
		},
		{
			name: "something else changing breaks the step",
			grid: "gynnn/gnynn/nnnyn/ggggg",
			want: nil,
		},
		{
			name: "grand staircase is not also a staircase",
			grid: "ynnnn/nynnn/nnynn/nnnyn/ggggg",
			want: []string{BadgeGrandStaircase, BadgeLateBloomer},
		},
		{
			name: "green staircase from the left",
			grid: "ynnnn/ggnnn/gggnn/ggggn/ggggg",
			want: []string{BadgeGreenStaircase},
		},
		{
			name: "green staircase from the right, three rows",
			grid: "nnggg/ngggg/ggggg",
			want: []string{BadgeGreenStaircase, BadgeSeaOfGreens},
		},
		{
			name: "two rows is not a green staircase",
			grid: "nyynn/ggggn/ggggg",
			want: nil,
		},
		{
			name: "a green staircase has to reach the solve",
			grid: "gnnnn/ggnnn/gggnn/nynnn/ggggg",
			want: nil,
		},
		{
			// The royal flush: the green staircase is not awarded as well.
			name: "royal staircase",
			grid: "gnnnn/ggnnn/gggnn/ggggn/ggggg",
			want: []string{BadgeRoyalStaircase, BadgeSeaOfGreens},
		},
		{
			name: "one to five in six guesses is a green staircase",
			grid: "nnnnn/gnnnn/ggnnn/gggnn/ggggn/ggggg",
			want: []string{BadgeGreenStaircase, BadgeSeaOfGreens},
		},
		{
			name: "space invader",
			grid: "nynyn/yngny/ngggn/ggggg",
			want: []string{BadgeSpaceInvader},
		},
		{
			name: "checkerboard",
			grid: "gngng/ngngn/ggggg",
			want: []string{BadgeCheckerboard, BadgeSeaOfGreens},
		},
		{
			name: "sea of greens",
			grid: "nngnn/gngng/ggggg",
			want: []string{BadgeSeaOfGreens},
		},
		{
			name: "anagram",
			grid: "yyyyy/ggggg",
			want: []string{BadgeAnagram},
		},
		{
			name: "late bloomer",
			grid: "nynnn/ynyny/ggggg",
			want: []string{BadgeLateBloomer},
		},
		{
			name: "from nothing",
			grid: "nnnnn/ggggg",
			want: []string{BadgeSeaOfGreens, BadgeFromNothing},
		},
		{
			name: "trap escape",
			grid: "ngggg/ngggg/ngggg/ggggg",
			want: []string{BadgeSeaOfGreens, BadgeTrapEscape},
		},
		{
			name: "two four-green rows is no trap",
			grid: "nyngn/ngggg/ngggg/ggggg",
			want: nil,
		},
		{
			name: "a miss earns nothing",
			grid: "yyyyy/nnnnn/nnnnn/nnnnn/nnnnn/nnnnn",
			want: nil,
		},
		{
			name: "no grid earns nothing",
			grid: "",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.grid.Badges(); !slices.Equal(got, tt.want) {
				t.Errorf("Badges(%q) = %v, want %v", tt.grid, got, tt.want)
			}
		})
	}
}
