package stats

import "github.com/martinstenrose/wordleland/internal/store"

// MeanScore averages every result that has a value under opts, and reports
// how many did. Zero and false when none has, rather than a NaN that would
// print as a number.
//
// The same value() every other figure here uses, so a mean a caller
// compares against a board average is a like-for-like comparison.
func MeanScore(results []store.BoardResult, opts Options) (float64, int) {
	var sum float64
	n := 0
	for _, r := range results {
		v, ok := value(r, opts)
		if !ok {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}
