package stats

import (
	"math"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// awardMonth is a finished month for the award figures, its first puzzle
// and the puzzle after its last.
func awardMonth(t *testing.T) (first, end int, date time.Time) {
	t.Helper()
	date, _ = wordle.DateForPuzzle(1871)
	first = wordle.PuzzleForDate(time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, date.Location()))
	end = wordle.PuzzleForDate(time.Date(date.Year(), date.Month()+1, 1, 0, 0, 0, 0, date.Location()))
	return first, end, date
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The spread is over the scores posted; the best week is seven days
// running, every one of them played, the earliest kept on a tie; and the
// best beat is the day furthest under that day's group average.
func TestMonthAwardFigures(t *testing.T) {
	t.Parallel()

	first, end, date := awardMonth(t)
	players := []store.Player{player(1, "ana"), player(2, "bo")}

	// Ana plays 4s all month but for 2s on days 10 to 16, and skips day 3,
	// so no week touching day 3 counts. Bo plays 4s throughout but for a
	// 6 on day 12, where Ana's 2 is 2 under the group's 4.
	var results []store.BoardResult
	for p := first; p < end; p++ {
		day := p - first + 1
		switch {
		case day == 3:
		case day >= 10 && day <= 16:
			results = append(results, result(1, p, 2, false))
		default:
			results = append(results, result(1, p, 4, false))
		}
		if day == 12 {
			results = append(results, result(2, p, 6, false))
		} else {
			results = append(results, result(2, p, 4, false))
		}
	}

	opts := DefaultOptions(today(t))
	m := monthOf(t, ComputeMonths(players, results, opts), date.Year(), date.Month())
	figs := MonthAwardFigures(results, opts, m)

	ana := figs[1]
	if ana.BestWeek == nil || !near(ana.BestWeek.Average, 2) || ana.BestWeek.First.Day() != 10 || ana.BestWeek.Days != 7 {
		t.Errorf("Ana's best week = %+v, want 2.00 from the 10th", ana.BestWeek)
	}
	if ana.BestBeat == nil || !near(ana.BestBeat.Margin, 2) || ana.BestBeat.Date.Day() != 12 {
		t.Errorf("Ana's best beat = %+v, want 2.00 under the group on the 12th", ana.BestBeat)
	}

	bo := figs[2]
	if bo.BestWeek == nil || !near(bo.BestWeek.Average, 4) || bo.BestWeek.First.Day() != 1 {
		t.Errorf("Bo's best week = %+v, want 4.00 from the 1st, the earliest of the level ones", bo.BestWeek)
	}
	if bo.Spread == nil || *bo.Spread <= 0 || *bo.Spread >= *ana.Spread {
		t.Errorf("Bo's spread %v is not the smaller: one 6 among 4s against Ana's week of 2s", bo.Spread)
	}
}

// A week with a day not played in it is not a week, and a player who never
// came in under the group has no beat.
func TestMonthAwardFiguresNeedAWholeWeekAndADayUnder(t *testing.T) {
	t.Parallel()

	first, end, date := awardMonth(t)
	players := []store.Player{player(1, "gappy"), player(2, "alone")}

	// Gappy plays every other day; Alone plays the days Gappy does not,
	// so nobody shares a day and there is no group to come in under.
	var results []store.BoardResult
	for p := first; p < end; p++ {
		if (p-first)%2 == 0 {
			results = append(results, result(1, p, 3, false))
		} else {
			results = append(results, result(2, p, 3, false))
		}
	}

	opts := DefaultOptions(today(t))
	m := monthOf(t, ComputeMonths(players, results, opts), date.Year(), date.Month())
	for id, f := range MonthAwardFigures(results, opts, m) {
		if f.BestWeek != nil {
			t.Errorf("player %d has a best week %+v without seven days running", id, f.BestWeek)
		}
		if f.BestBeat != nil {
			t.Errorf("player %d beat a group they were the only one in: %+v", id, f.BestBeat)
		}
	}
}

// A season average is the mean of the months that ranked a player that
// year, not of their games, and another year's months do not count.
func TestSeasonAveragesAreAMeanOfMonths(t *testing.T) {
	t.Parallel()

	avg := func(v float64) *float64 { return &v }
	mp := func(id int64, a float64) MonthPlayer {
		return MonthPlayer{Player: store.Player{ID: id}, Average: avg(a)}
	}
	months := []Month{
		{Year: 2026, Month: time.March, Ranked: []MonthPlayer{mp(1, 3), mp(2, 5)}},
		{Year: 2026, Month: time.February, Ranked: []MonthPlayer{mp(1, 4)}},
		{Year: 2025, Month: time.December, Ranked: []MonthPlayer{mp(1, 6), mp(2, 1)}},
	}
	got := SeasonAverages(months, 2026)
	if !near(got[1], 3.5) || !near(got[2], 5) || len(got) != 2 {
		t.Errorf("season averages = %v, want 1: 3.5 and 2: 5", got)
	}
}
