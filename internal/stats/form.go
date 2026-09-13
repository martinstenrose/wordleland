package stats

import (
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// TodayForm is the 30-day form score, played-game count and chart. Board ranks
// and eligibility remain based on the normal board rules.
type TodayForm struct {
	Average *float64
	Games   int
	Series  []float64
}

// ComputeTodayForm is Today's form: completed missed days count as seven,
// but an unfinished today and days before a player joined are not misses.
// Results belong to one player; MinGames counts actual filtered games,
// never the synthetic sevens added for absences.
func ComputeTodayForm(results []store.BoardResult, opts Options) TodayForm {
	current := wordle.PuzzleForDate(opts.Now)
	start := current - FormWindow + 1
	played := make(map[int]bool, len(results))
	first := current + 1
	for _, result := range results {
		played[result.PuzzleNo] = true
		if result.PuzzleNo < first {
			first = result.PuzzleNo
		}
	}
	counted := results
	if opts.HardModeOnly {
		counted = filterHardMode(results)
	}
	values, games, series := windowValues(counted, opts, start, current)
	if opts.CountXAsSeven {
		for puzzle := max(start, first); puzzle < current; puzzle++ {
			if !played[puzzle] {
				values = append(values, failedAsSeven)
				series[puzzle-start] = failedAsSeven
			}
		}
	}
	form := TodayForm{Games: games, Series: series}
	if games >= MinGames {
		form.Average = mean(values)
	}
	return form
}

// TodayBaseline is the figure Form is measured against: a lifetime average
// under the same missed-as-seven rule ComputeTodayForm applies, bounded to
// the player's own active window. It ignores opts.CountMissed on purpose —
// Form always counts a 30-day gap as a miss, so comparing it to a baseline
// that only sometimes does would make Delta compare different rules.
func TodayBaseline(results []store.BoardResult, opts Options) *float64 {
	counted := results
	if opts.HardModeOnly {
		counted = filterHardMode(results)
	}
	baseline := opts
	baseline.CountMissed = true
	return mean(countedValues(counted, results, baseline))
}
