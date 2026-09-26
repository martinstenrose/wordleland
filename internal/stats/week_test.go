package stats

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Every puzzle's week starts on the Monday its own date belongs to, checked
// against the calendar rather than against the arithmetic it is made of.
func TestWeekOfIsTheCalendarMonday(t *testing.T) {
	for puzzle := 1500; puzzle < 1530; puzzle++ {
		monday, err := wordle.DateForPuzzle(WeekOf(puzzle))
		if err != nil {
			t.Fatal(err)
		}
		if monday.Weekday() != time.Monday {
			t.Fatalf("WeekOf(%d) is a %s", puzzle, monday.Weekday())
		}
		if gap := puzzle - WeekOf(puzzle); gap < 0 || gap > 6 {
			t.Fatalf("WeekOf(%d) = %d, %d days away", puzzle, WeekOf(puzzle), gap)
		}
	}
}

// A week is scored as a month is: a day not played counts as 7, and the
// following Monday's result stays out of it.
func TestWeekCountsMissedDaysAndStopsAtSunday(t *testing.T) {
	first := WeekOf(1900)
	players := []store.Player{player(1, "alma"), player(2, "bea")}
	results := run(1, first, first+6, 4, false)
	results = append(results, run(2, first, first+4, 3, false)...)
	results = append(results, run(1, first+7, first+7, 1, false)...)

	sunday, _ := wordle.DateForPuzzle(first + 6)
	w := ComputeWeek(players, results, first, DefaultOptions(sunday.AddDate(0, 0, 1)))

	if w.First != first || w.Last != first+6 {
		t.Errorf("span = %d–%d, want %d–%d", w.First, w.Last, first, first+6)
	}
	if got := slugs(w.Ranked); len(got) != 2 || got[0] != "alma" || got[1] != "bea" {
		t.Fatalf("Ranked = %v, want alma then bea", got)
	}
	if got := *w.Ranked[0].Average; got != 4 {
		t.Errorf("alma = %.2f, want 4 — the next Monday's 1 leaked in", got)
	}
	if got, want := *w.Ranked[1].Average, (5*3+2*7)/7.0; got != want {
		t.Errorf("bea = %.2f, want %.2f", got, want)
	}
	if w.Ranked[1].Games != 5 {
		t.Errorf("bea played %d, want 5", w.Ranked[1].Games)
	}
}
