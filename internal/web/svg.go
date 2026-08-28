package web

import (
	"fmt"
	"strings"
)

// Sparkline geometry, matching the design's ledger artboard.
const (
	sparkWidth  = 140
	sparkHeight = 30

	// bestScore and worstScore bound the vertical scale. A failure counts as
	// 7 for plotting whatever the averaging toggle says, because the line is
	// about shape rather than arithmetic and a gap where a failure happened
	// would read as a day off.
	bestScore  = 1.0
	worstScore = 7.0
)

// sparkPath builds an SVG path from a series of scores.
//
// A zero entry means no game that day and is skipped, so the line connects
// across a gap rather than breaking. That is the design's behaviour and it
// is the right one here: a missing day is already visible as an absence in
// the streak and games columns, and a broken line at this size reads as a
// rendering fault rather than as information.
//
// Lower is better, so a 1 sits at the top and a 7 at the bottom.
func sparkPath(series []float64, width, height float64) string {
	if len(series) < 2 {
		return ""
	}

	var (
		b     strings.Builder
		steps = float64(len(series) - 1)
		open  bool
	)
	for i, v := range series {
		if v <= 0 {
			continue
		}
		x := float64(i) / steps * width
		// Clamped so an out-of-range value cannot draw outside the box.
		clamped := v
		if clamped < bestScore {
			clamped = bestScore
		}
		if clamped > worstScore {
			clamped = worstScore
		}
		y := (clamped - bestScore) / (worstScore - bestScore) * height

		verb := "L"
		if !open {
			verb = "M"
			open = true
		}
		fmt.Fprintf(&b, "%s%.1f %.1f", verb, x, y)
		if i < len(series)-1 {
			b.WriteByte(' ')
		}
	}
	return strings.TrimSpace(b.String())
}

// hasSparkline reports whether a series has enough points to draw.
func hasSparkline(series []float64) bool {
	var points int
	for _, v := range series {
		if v > 0 {
			points++
			if points >= 2 {
				return true
			}
		}
	}
	return false
}
