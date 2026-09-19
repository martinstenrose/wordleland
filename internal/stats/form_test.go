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

// A gap in the window counts as seven whether or not an attempted failure
// does — the same rule missedValues and the months backfill follow.
//
// This used to assert the opposite, on the reasoning that with a failure
// scored as nothing there is no number a miss could take either. There is:
// seven is what a Wordle is worth when it was not solved, and turning up is
// a separate question from succeeding.
func TestTodayFormMissedDaysDoNotFollowCountXAsSeven(t *testing.T) {
	opts := DefaultOptions(today(t))
	opts.CountXAsSeven = false
	results := run(1, 1871, 1881, 3, false)
	form := ComputeTodayForm(results, opts)

	// Eleven played days at 3, and every concluded day since counted as a
	// miss — so the average is worse than the days actually played.
	if form.Games != 11 {
		t.Errorf("Games = %d, want the 11 days played; a miss is not a game", form.Games)
	}
	if form.Average == nil || *form.Average <= 3 {
		t.Errorf("missed days were not counted with failures excluded: %+v", form)
	}
	if form.Series[11] != failedAsSeven {
		t.Errorf("chart left a gap uncounted: %v", form.Series)
	}
	// Today is not missed until it is over, whatever the toggles say.
	if form.Series[len(form.Series)-1] != 0 {
		t.Errorf("chart counted the day still in progress: %v", form.Series)
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
