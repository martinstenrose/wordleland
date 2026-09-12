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
