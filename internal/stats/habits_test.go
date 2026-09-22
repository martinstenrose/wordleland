package stats

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

// posted stamps a result with a posting time on its own date.
func posted(r store.BoardResult, hour, minute int) store.BoardResult {
	at := time.Date(r.Date.Year(), r.Date.Month(), r.Date.Day(), hour, minute, 0, 0, time.Local)
	r.PostedAt = &at
	return r
}

// Alma opens every day and Bosse closes it, for a run of five; on the day
// before that Bosse was first. The window counts the days, the run stops
// where the order changed.
func TestPostingHabitsCountDaysAndRuns(t *testing.T) {
	var results []store.BoardResult
	for p := 1901; p <= 1905; p++ {
		results = append(results,
			posted(result(1, p, 3, false), 6, 0),
			posted(result(2, p, 4, false), 22, 0),
			posted(result(3, p, 4, false), 12, 0),
		)
	}
	results = append(results,
		posted(result(2, 1900, 4, false), 6, 0),
		posted(result(1, 1900, 3, false), 9, 0),
	)

	h := ComputePostingHabits(results, 1905)

	if h.Days != 6 {
		t.Errorf("Days = %d, want 6 qualifying puzzles", h.Days)
	}
	if got := h.First[1]; got != (Habit{Days: 5, Run: 5}) {
		t.Errorf("First[alma] = %+v, want 5 days and a run of 5", got)
	}
	if got := h.First[2]; got != (Habit{Days: 1}) {
		t.Errorf("First[bosse] = %+v, want 1 day and no run", got)
	}
	if got := h.Last[2]; got != (Habit{Days: 5, Run: 5}) {
		t.Errorf("Last[bosse] = %+v, want 5 days and a run of 5", got)
	}
	if got := h.Last[1]; got != (Habit{Days: 1}) {
		t.Errorf("Last[alma] = %+v, want 1 day and no run", got)
	}
}

// A day with only one posting time has no order, and it breaks a run even
// though the same player opened on either side of it. Puzzles after through
// are not read at all.
func TestPostingHabitsSkipDaysWithoutAnOrder(t *testing.T) {
	results := []store.BoardResult{
		posted(result(1, 1901, 3, false), 6, 0), posted(result(2, 1901, 4, false), 9, 0),
		posted(result(1, 1902, 3, false), 6, 0), result(2, 1902, 4, false), // Bosse by hand
		posted(result(1, 1903, 3, false), 6, 0), posted(result(2, 1903, 4, false), 9, 0),
		posted(result(2, 1904, 3, false), 6, 0), posted(result(1, 1904, 4, false), 9, 0), // tomorrow
	}

	h := ComputePostingHabits(results, 1903)

	if h.Days != 2 {
		t.Errorf("Days = %d, want 2: the by-hand day has no order and tomorrow is not yet", h.Days)
	}
	if got := h.First[1]; got != (Habit{Days: 2, Run: 1}) {
		t.Errorf("First[alma] = %+v, want 2 days and a run of 1", got)
	}
}

// The window is the last HabitWindow qualifying puzzles, not the last
// HabitWindow calendar days.
func TestPostingHabitsWindow(t *testing.T) {
	var results []store.BoardResult
	for p := 1800; p <= 1900; p++ {
		first, second := int64(1), int64(2)
		if p <= 1870 {
			first, second = 2, 1
		}
		results = append(results,
			posted(result(first, p, 3, false), 6, 0),
			posted(result(second, p, 4, false), 9, 0),
		)
	}

	h := ComputePostingHabits(results, 1900)

	if h.Days != HabitWindow {
		t.Errorf("Days = %d, want the window of %d", h.Days, HabitWindow)
	}
	if got := h.First[1]; got != (Habit{Days: 30, Run: 30}) {
		t.Errorf("First[alma] = %+v, want the whole window", got)
	}
	if _, ok := h.First[2]; ok {
		t.Errorf("First[bosse] = %+v, want nothing: those days fell out of the window", h.First[2])
	}
}

// Today's first and last poster, and the two cases where there is no order
// to report.
func TestTodayPostingOrder(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse"), player(3, "cilla")}

	today := ComputeToday(players, []store.BoardResult{
		posted(result(1, 1900, 3, false), 9, 30),
		posted(result(2, 1900, 2, false), 6, 12),
		result(3, 1900, 4, false),
	}, 1900)
	if today.First == nil || today.First.Slug != "bosse" {
		t.Errorf("First = %v, want bosse at 06:12", today.First)
	}
	if today.Last == nil || today.Last.Slug != "alma" {
		t.Errorf("Last = %v, want alma at 09:30, not cilla whose time is unknown", today.Last)
	}

	today = ComputeToday(players, []store.BoardResult{
		posted(result(1, 1900, 3, false), 9, 30),
		result(2, 1900, 2, false),
	}, 1900)
	if today.First != nil || today.Last != nil {
		t.Errorf("First/Last = %v/%v with one stamped result, want neither", today.First, today.Last)
	}

	// The same minute: the order falls to the name so it is stable.
	today = ComputeToday(players, []store.BoardResult{
		posted(result(2, 1900, 3, false), 8, 0),
		posted(result(1, 1900, 3, false), 8, 0),
	}, 1900)
	if today.First == nil || today.First.Slug != "alma" || today.Last == nil || today.Last.Slug != "bosse" {
		t.Errorf("tie = %v/%v, want alma then bosse by name", today.First, today.Last)
	}
}
