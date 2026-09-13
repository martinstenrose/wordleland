package stats

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

func TestTodayFormCountsCompletedMissesAndLeavesTodayOpen(t *testing.T) {
	opts := DefaultOptions(today(t)) // puzzle 1900
	results := run(1, 1871, 1881, 3, false)
	form := ComputeTodayForm(results, opts)
	// Eleven actual games and eighteen completed misses; today stays open.
	if form.Average == nil || *form.Average != 159.0/29 || form.Games != 11 {
		t.Fatalf("form = %+v, want average 159/29 over eleven played games", form)
	}
	if len(form.Series) != 30 || form.Series[0] != 3 || form.Series[10] != 3 || form.Series[11] != 7 || form.Series[28] != 7 || form.Series[29] != 0 {
		t.Errorf("chart does not reflect played games, completed misses and open today: %v", form.Series)
	}
	filed := append(append([]store.BoardResult{}, results...), result(1, 1900, 2, false))
	form = ComputeTodayForm(filed, opts)
	if form.Average == nil || *form.Average != 161.0/30 || form.Games != 12 || form.Series[29] != 2 {
		t.Errorf("today's filed result was not counted: %+v", form)
	}
	// Tomorrow drops the oldest three and adds a seven for yesterday.
	opts.Now = opts.Now.Add(24 * time.Hour)
	form = ComputeTodayForm(results, opts)
	if form.Average == nil || *form.Average != 163.0/29 || form.Games != 10 {
		t.Errorf("yesterday should now count as missed: %+v", form)
	}
}

func TestTodayFormMinimumCountsGamesNotMisses(t *testing.T) {
	opts := DefaultOptions(today(t))
	results := run(1, 1871, 1879, 3, false)
	form := ComputeTodayForm(results, opts)
	if form.Average != nil || form.Games != 9 {
		t.Errorf("missed days qualified form: %+v", form)
	}
	results = append(results, result(1, 1880, 3, false))
	form = ComputeTodayForm(results, opts)
	if form.Average == nil || form.Games != 10 {
		t.Errorf("ten played games did not qualify form: %+v", form)
	}
}

func TestTodayFormDoesNotManufactureAbsences(t *testing.T) {
	opts := DefaultOptions(today(t))
	opts.HardModeOnly = true
	results := []store.BoardResult{result(1, 1889, 4, false)}
	results = append(results, run(1, 1890, 1899, 3, true)...)
	results = append(results, result(1, 1901, 6, true))
	form := ComputeTodayForm(results, opts)
	if form.Average == nil || *form.Average != 3 || form.Games != 10 {
		t.Errorf("days before joining, ordinary-mode games or future results changed form: %+v", form)
	}
}

// With the toggle off there is no number a miss could take, so a gap must
// stay uncounted rather than falling back to seven — the same rule
// missedValues and the months backfill follow.
func TestTodayFormMissedDaysFollowCountXAsSeven(t *testing.T) {
	opts := DefaultOptions(today(t))
	opts.CountXAsSeven = false
	results := run(1, 1871, 1881, 3, false)
	form := ComputeTodayForm(results, opts)
	if form.Average == nil || *form.Average != 3 || form.Games != 11 {
		t.Errorf("missed days were counted with the toggle off: %+v", form)
	}
	if form.Series[11] != 0 {
		t.Errorf("chart put a seven in a day that was never played: %v", form.Series)
	}
}

// TodayBaseline always counts a gap as a miss the same way Form does, even
// when the board's CountMissed toggle would otherwise exclude it — so a
// player whose gaps in the window match their gaps over their whole active
// stretch gets a Delta near zero instead of a false swing. Before this fix,
// Average (CountMissed off by default) ignored these gaps entirely while
// Form always counted them, mixing two different rules.
func TestTodayBaselineMatchesFormForASteadyGapPattern(t *testing.T) {
	opts := DefaultOptions(today(t)) // CountMissed defaults off; window is [1871, 1900)
	var results []store.BoardResult
	for p := 1871; p <= 1899; p += 2 {
		results = append(results, result(1, p, 4, false))
	}
	// 15 played games at 4, 14 missed days at 7, over the same 29-puzzle span.
	const want = (15.0*4 + 14.0*7) / 29

	baseline := TodayBaseline(results, opts)
	if baseline == nil || *baseline != want {
		t.Fatalf("baseline = %v, want %v", baseline, want)
	}
	form := ComputeTodayForm(results, opts)
	if form.Average == nil || *form.Average != want {
		t.Fatalf("form.Average = %v, want %v", form.Average, want)
	}
}
