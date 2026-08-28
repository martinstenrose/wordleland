// Package wordle parses Wordle share text and maps puzzle numbers to dates.
//
// It is pure and table-driven-testable. Any binary calling DateForPuzzle or
// PuzzleForDate must blank-import time/tzdata: the distroless base image ships
// no timezone files, and these depend on time.Local.
package wordle

import (
	"fmt"
	"time"
)

// epochYear, epochMonth and epochDay are Wordle puzzle 0: 19 June 2021.
//
// Dates are computed in time.Local, driven by TZ, because the game rolls over
// at each player's local midnight — someone playing at 23:50 sees that day's
// puzzle, and at 00:01 the next one. The whole group is in one timezone, so
// one canonical basis matching what they see is correct; UTC would be a day
// out for anyone playing late in the evening.
const (
	epochYear  = 2021
	epochMonth = time.June
	epochDay   = 19
)

// maxPuzzleNo is a sanity bound. Wordle is at roughly 1900 in 2026, so
// anything past this is a parse error or a typo rather than a real puzzle.
const maxPuzzleNo = 100000

// epoch returns puzzle 0's date in the current local zone.
//
// Computed per call rather than cached in a package variable: time.Local is
// set from TZ at process start, and a package-level value would freeze
// whatever zone happened to be current when the package initialised.
func epoch() time.Time {
	return time.Date(epochYear, epochMonth, epochDay, 0, 0, 0, 0, time.Local)
}

// DateForPuzzle returns the date a puzzle belongs to.
//
// AddDate does calendar arithmetic, deliberately: adding 24-hour durations
// would drift by an hour across a daylight-saving boundary and eventually
// shift a puzzle onto the wrong date. Europe/Stockholm has two such
// transitions a year, so this is not hypothetical.
func DateForPuzzle(n int) (time.Time, error) {
	if n < 0 || n > maxPuzzleNo {
		return time.Time{}, fmt.Errorf("puzzle number %d is out of range", n)
	}
	return epoch().AddDate(0, 0, n), nil
}

// PuzzleForDate returns the puzzle number for a date.
//
// The two dates are compared as calendar days rather than by subtracting
// timestamps. Subtracting local midnights looks equivalent and is not: across
// a daylight-saving boundary the gap is 23 or 25 hours, so dividing by 24
// truncates to the wrong day in one direction. That it currently happens to
// work would be an accident of the epoch falling in summer time, which is not
// something to depend on.
func PuzzleForDate(t time.Time) int {
	return civilDay(t) - civilDay(epoch())
}

// civilDay converts a date to a day number, ignoring clocks entirely.
//
// Both dates are re-anchored at noon UTC, using only their year, month and
// day. Noon leaves twelve hours of headroom either side, so no offset any zone
// applies can push the value across a day boundary.
func civilDay(t time.Time) int {
	noon := time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, time.UTC)
	return int(noon.Unix() / 86400)
}
