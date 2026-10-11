package stats

import (
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// MonthFigures are the figures a month's awards are decided on that the
// month's own table does not carry, one player's worth.
type MonthFigures struct {
	// Spread is the standard deviation of the scores the player posted
	// in the month, nil with fewer than two to spread.
	Spread *float64

	// BestWeek is the player's lowest average over seven days running
	// inside the month, each of them played; nil when no seven days are.
	// A week with a gap in it is not a week of play, and with a miss
	// counted as nothing it would be one good day short of a week.
	BestWeek *Stretch

	// BestBeat is the day the player came in furthest under the group's
	// average for that day; nil when they never came in under it.
	BestBeat *Beat
}

// Stretch is an average over days running, the first of them dated.
type Stretch struct {
	Average float64
	First   time.Time
	Days    int
}

// Beat is how far under the group's average a player came in on a day.
type Beat struct {
	Margin float64
	Date   time.Time
}

// weekDays is how many days running a best week is.
const weekDays = 7

// MonthAwardFigures works out each ranked or thin player's MonthFigures
// for month m, scoring each result as the month does — a failure by
// CountXAsSeven, hard mode only when asked. Days run over the month's
// span, m.First for m.Days, so a running month's week and day can only be
// ones it has had.
func MonthAwardFigures(results []store.BoardResult, opts Options, m Month) map[int64]MonthFigures {
	if opts.HardModeOnly {
		results = filterHardMode(results)
	}
	in := map[int64]bool{}
	for _, p := range m.Ranked {
		in[p.ID] = true
	}
	for _, p := range m.Thin {
		in[p.ID] = true
	}

	// The value each player has on each day of the span, and each day's
	// values across the group.
	byPlayer := map[int64]map[int]float64{}
	played := map[int64]map[int]bool{}
	byDay := map[int][]float64{}
	for _, r := range results {
		if !in[r.PlayerID] || r.PuzzleNo < m.First || r.PuzzleNo >= m.First+m.Days {
			continue
		}
		if played[r.PlayerID] == nil {
			played[r.PlayerID] = map[int]bool{}
			byPlayer[r.PlayerID] = map[int]float64{}
		}
		played[r.PlayerID][r.PuzzleNo] = true
		if v, ok := value(r, opts); ok {
			byPlayer[r.PlayerID][r.PuzzleNo] = v
			byDay[r.PuzzleNo] = append(byDay[r.PuzzleNo], v)
		}
	}

	out := map[int64]MonthFigures{}
	for id, days := range byPlayer {
		var f MonthFigures

		var values []float64
		for d := m.First; d < m.First+m.Days; d++ {
			if v, ok := days[d]; ok {
				values = append(values, v)
			}
		}
		if avg := mean(values); avg != nil {
			f.Spread = spread(values, *avg)
		}

		// Every window of seven days running, the earliest kept on a tie.
		for start := m.First; start+weekDays <= m.First+m.Days; start++ {
			var week []float64
			whole := true
			for d := start; d < start+weekDays; d++ {
				if !played[id][d] {
					whole = false
					break
				}
				if v, ok := days[d]; ok {
					week = append(week, v)
				}
			}
			avg := mean(week)
			if !whole || avg == nil || (f.BestWeek != nil && *avg >= f.BestWeek.Average) {
				continue
			}
			date, _ := wordle.DateForPuzzle(start)
			f.BestWeek = &Stretch{Average: *avg, First: date, Days: weekDays}
		}

		// The day furthest under the group's average, the earliest kept on
		// a tie. The group includes the player, as "the group's average
		// that day" does wherever else it is said.
		for d := m.First; d < m.First+m.Days; d++ {
			v, ok := days[d]
			if !ok {
				continue
			}
			margin := *mean(byDay[d]) - v
			if margin <= 0 || (f.BestBeat != nil && margin <= f.BestBeat.Margin) {
				continue
			}
			date, _ := wordle.DateForPuzzle(d)
			f.BestBeat = &Beat{Margin: margin, Date: date}
		}

		out[id] = f
	}
	return out
}

// SeasonAverages is each player's season average for year: the mean of
// their averages across the months of it that ranked them. A mean of
// months rather than of games, so a month counts the same however many
// days of it somebody played, as the season's places do.
func SeasonAverages(months []Month, year int) map[int64]float64 {
	sums, counts := map[int64]float64{}, map[int64]int{}
	for _, m := range months {
		if m.Year != year {
			continue
		}
		for _, p := range m.Ranked {
			sums[p.ID] += *p.Average
			counts[p.ID]++
		}
	}
	out := make(map[int64]float64, len(sums))
	for id, s := range sums {
		out[id] = s / float64(counts[id])
	}
	return out
}
