package web

import (
	"strings"
	"testing"
)

func TestSparkPath(t *testing.T) {
	tests := []struct {
		name   string
		series []float64
		want   string
	}{
		{
			// Lower is better, so a 1 is at the top and a 7 at the bottom.
			name:   "best to worst spans the box",
			series: []float64{1, 7},
			want:   "M0.0 0.0 L100.0 60.0",
		},
		{
			// A gap is connected across rather than breaking the line.
			name:   "a missing day is skipped",
			series: []float64{1, 0, 7},
			want:   "M0.0 0.0L100.0 60.0",
		},
		{
			name:   "the first point opens the path wherever it falls",
			series: []float64{0, 4, 4},
			want:   "M50.0 30.0 L100.0 30.0",
		},
		{name: "nothing to draw", series: []float64{0, 0, 0}, want: ""},
		{name: "a single point is not a line", series: []float64{4}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sparkPath(tt.series, 100, 60)
			if strings.ReplaceAll(got, " ", "") != strings.ReplaceAll(tt.want, " ", "") {
				t.Errorf("sparkPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An out-of-range value must not draw outside the box, whatever produced it.
//
// Zero and below are the "no game" sentinel rather than scores, so the
// clamp is exercised with values that are positive but outside 1..7.
func TestSparkPathClampsToTheBox(t *testing.T) {
	got := sparkPath([]float64{0.5, 99}, 100, 60)
	for _, coord := range []string{"-", "60.1", "99"} {
		if strings.Contains(got, coord) {
			t.Errorf("path %q escapes the box", got)
		}
	}
	if !strings.Contains(got, "M0.0 0.0") || !strings.Contains(got, "L100.0 60.0") {
		t.Errorf("path = %q, want both values clamped to the edges", got)
	}
}

func TestHasSparkline(t *testing.T) {
	tests := map[string]struct {
		series []float64
		want   bool
	}{
		"two points": {[]float64{3, 4}, true},
		"one point":  {[]float64{0, 3, 0}, false},
		"no points":  {[]float64{0, 0}, false},
		"gappy pair": {[]float64{3, 0, 0, 4}, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := hasSparkline(tt.series); got != tt.want {
				t.Errorf("hasSparkline() = %v, want %v", got, tt.want)
			}
		})
	}
}
