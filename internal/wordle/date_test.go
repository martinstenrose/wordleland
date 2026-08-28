package wordle

import (
	"testing"
	"time"
)

// withZone runs fn with time.Local set to the named zone.
func withZone(t *testing.T, name string, fn func()) {
	t.Helper()

	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	original := time.Local
	time.Local = loc
	defer func() { time.Local = original }()
	fn()
}

func TestDateForPuzzleEpoch(t *testing.T) {
	withZone(t, "Europe/Stockholm", func() {
		got, err := DateForPuzzle(0)
		if err != nil {
			t.Fatalf("DateForPuzzle(0) failed: %v", err)
		}
		want := time.Date(2021, time.June, 19, 0, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("DateForPuzzle(0) = %s, want %s", got, want)
		}
	})
}

// Owner-verified against real data at both ends of the range.
func TestDateForPuzzleKnownValues(t *testing.T) {
	withZone(t, "Europe/Stockholm", func() {
		tests := map[int]string{
			0:    "2021-06-19",
			1:    "2021-06-20",
			1890: "2026-08-22",
			1891: "2026-08-23",
		}
		for puzzle, want := range tests {
			got, err := DateForPuzzle(puzzle)
			if err != nil {
				t.Fatalf("DateForPuzzle(%d) failed: %v", puzzle, err)
			}
			if got.Format(time.DateOnly) != want {
				t.Errorf("DateForPuzzle(%d) = %s, want %s", puzzle, got.Format(time.DateOnly), want)
			}
		}
	})
}

// Calendar arithmetic, not duration arithmetic: across a DST boundary the gap
// between local midnights is 23 or 25 hours, and dividing that by 24
// truncates to the wrong day.
func TestDateForPuzzleAcrossDSTBoundaries(t *testing.T) {
	withZone(t, "Europe/Stockholm", func() {
		// Europe/Stockholm springs forward on 29 March 2026 and falls back on
		// 25 October 2026. Walking each boundary must advance exactly one
		// calendar day per puzzle.
		for _, boundary := range []string{"2026-03-29", "2026-10-25"} {
			day, err := time.ParseInLocation(time.DateOnly, boundary, time.Local)
			if err != nil {
				t.Fatalf("parse %s: %v", boundary, err)
			}
			base := PuzzleForDate(day)

			for offset := -2; offset <= 2; offset++ {
				got, err := DateForPuzzle(base + offset)
				if err != nil {
					t.Fatalf("DateForPuzzle failed: %v", err)
				}
				want := day.AddDate(0, 0, offset)
				if got.Format(time.DateOnly) != want.Format(time.DateOnly) {
					t.Errorf("around %s, puzzle %d = %s, want %s",
						boundary, base+offset, got.Format(time.DateOnly), want.Format(time.DateOnly))
				}
			}
		}
	})
}

// The two functions must agree everywhere, including across both boundaries.
func TestPuzzleDateRoundTrip(t *testing.T) {
	withZone(t, "Europe/Stockholm", func() {
		for puzzle := 0; puzzle < 2500; puzzle++ {
			date, err := DateForPuzzle(puzzle)
			if err != nil {
				t.Fatalf("DateForPuzzle(%d) failed: %v", puzzle, err)
			}
			if got := PuzzleForDate(date); got != puzzle {
				t.Fatalf("round trip: puzzle %d became %d via %s", puzzle, got, date.Format(time.DateOnly))
			}
		}
	})
}

// The time of day must not matter: a result posted at 23:50 belongs to that
// day's puzzle, not the next one.
func TestPuzzleForDateIgnoresTimeOfDay(t *testing.T) {
	withZone(t, "Europe/Stockholm", func() {
		day, err := time.ParseInLocation(time.DateOnly, "2026-08-23", time.Local)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		want := PuzzleForDate(day)

		for _, hour := range []int{0, 1, 12, 23} {
			at := time.Date(2026, time.August, 23, hour, 50, 0, 0, time.Local)
			if got := PuzzleForDate(at); got != want {
				t.Errorf("at %02d:50 the puzzle is %d, want %d", hour, got, want)
			}
		}
	})
}

// A southern-hemisphere zone shifts DST to the opposite half of the year, so
// the epoch no longer falls in summer time. Duration arithmetic would break
// here even though it passes for Stockholm.
func TestPuzzleDateRoundTripInOppositeHemisphere(t *testing.T) {
	withZone(t, "Pacific/Auckland", func() {
		for puzzle := 0; puzzle < 2500; puzzle++ {
			date, err := DateForPuzzle(puzzle)
			if err != nil {
				t.Fatalf("DateForPuzzle(%d) failed: %v", puzzle, err)
			}
			if got := PuzzleForDate(date); got != puzzle {
				t.Fatalf("round trip: puzzle %d became %d via %s", puzzle, got, date.Format(time.DateOnly))
			}
		}
	})
}

func TestDateForPuzzleRejectsOutOfRange(t *testing.T) {
	for _, n := range []int{-1, maxPuzzleNo + 1} {
		if _, err := DateForPuzzle(n); err == nil {
			t.Errorf("DateForPuzzle(%d) succeeded, want an error", n)
		}
	}
}
