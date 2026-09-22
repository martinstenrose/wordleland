package stats

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

func TestMeanScoreFollowsTheScoringRule(t *testing.T) {
	results := []store.BoardResult{
		result(1, 1900, 3, false), result(2, 1900, 5, false), result(3, 1900, 0, false),
	}
	opts := DefaultOptions(time.Now())

	mean, n := MeanScore(results, opts)
	if n != 3 || mean != 5 {
		t.Errorf("with failures as seven: mean %v over %d, want 5 over 3", mean, n)
	}

	opts.CountXAsSeven = false
	mean, n = MeanScore(results, opts)
	if n != 2 || mean != 4 {
		t.Errorf("with failures excluded: mean %v over %d, want 4 over 2", mean, n)
	}

	if mean, n := MeanScore(nil, opts); mean != 0 || n != 0 {
		t.Errorf("empty: mean %v over %d, want 0 over 0", mean, n)
	}
}
