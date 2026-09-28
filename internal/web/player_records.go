package web

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// recordsWindow is how far back the year-long figures on a player's page
// reach: the records that say "over the last year", the rivals, the day of
// the week and the heatmap.
const recordsWindow = 364

// heatWeeks is how many weeks the heatmap draws: a year on a wide screen.
// A phone shows the most recent half of them (see app.css).
const heatWeeks = 52

// dayTable is every score by puzzle, a miss counted as 7, for the lines a
// day's popup says about the group.
type dayTable map[int][]float64

func newDayTable(results []store.BoardResult) dayTable {
	d := dayTable{}
	for _, r := range results {
		d[r.PuzzleNo] = append(d[r.PuzzleNo], scoreOf(r))
	}
	return d
}

// scoreOf is a result's value, a miss as 7.
func scoreOf(r store.BoardResult) float64 {
	if !r.Solved {
		return 7
	}
	return float64(r.Guesses)
}

// dayPopup is what opens from a day on a player's page: the result and when,
// the group that day, and the day's own page.
type dayPopup struct {
	Line1, Line2 string
	Href         string
}

// popupFor builds the popup for one of a player's results.
func popupFor(t translator, days dayTable, r store.BoardResult, prefix string) dayPopup {
	date, _ := wordle.DateForPuzzle(r.PuzzleNo)
	result := t.T("player.popup.failed")
	if r.Solved {
		result = t.TN("player.guesses", r.Guesses)
	}
	line1 := t.T("player.puzzle", t.Puzzle(r.PuzzleNo)) + " · " +
		t.T("weekday.short."+strconv.Itoa(int(date.Weekday()))) + " " + strconv.Itoa(date.Day()) + " " +
		shortMonthName(t, date.Month()) + " · " + result
	if r.HardMode {
		line1 += " *"
	}

	scores := days[r.PuzzleNo]
	var sum float64
	place := 1
	for _, s := range scores {
		sum += s
		if s < scoreOf(r) {
			place++
		}
	}
	line2 := t.T("puzzle.average", t.Decimal(sum/float64(max(1, len(scores))), 2))
	if len(scores) > 1 {
		if place == 1 {
			line2 += " · " + t.T("player.popup.best", len(scores))
		} else {
			line2 += " · " + t.T("player.popup.place", t.Ordinal(place), len(scores))
		}
	}
	return dayPopup{Line1: line1, Line2: line2, Href: puzzlePath(prefix, r.PuzzleNo)}
}

// recordRow is one line of Records and rivals.
type recordRow struct {
	Icon  string
	Label string
	Value string
	Sub   string
}

// playerRecords builds Records and rivals: the player's best month, both
// streaks as they stand against their longest, the ones and twos of the last
// year, and the two players they fare worst and best against.
func playerRecords(t translator, p stats.Player, board stats.Board, months []stats.Month,
	results []store.BoardResult, now time.Time) []recordRow {

	var rows []recordRow

	best := recordRow{Icon: "emoji_events", Label: t.T("player.record.bestMonth"), Value: "—"}
	var bestAvg *float64
	for _, m := range months {
		if !m.Complete(now) {
			continue
		}
		for _, mp := range m.Ranked {
			if mp.ID == p.ID && mp.Average != nil && (bestAvg == nil || *mp.Average < *bestAvg) {
				avg := *mp.Average
				bestAvg = &avg
				best.Value = t.Decimal(avg, 2)
				best.Sub = t.T("month."+strconv.Itoa(int(m.Month))) + " " + strconv.Itoa(m.Year)
			}
		}
	}
	if bestAvg == nil {
		best.Sub = t.T("player.record.noMonth")
	}
	rows = append(rows, best,
		recordRow{Icon: "event_available", Label: t.T("player.record.playStreak"),
			Value: t.TN("player.record.days", p.LongestPlayStreak),
			Sub:   t.T("player.record.playStreakSub", t.Integer(p.PlayStreak))},
		recordRow{Icon: "local_fire_department", Label: t.T("player.record.solveStreak"),
			Value: t.TN("player.record.days", p.LongestStreak),
			Sub:   t.T("player.record.solveStreakSub", t.Integer(p.CurrentStreak))},
	)

	from := board.CurrentPuzzle - recordsWindow
	mine := map[int]float64{}
	quick := 0
	for _, r := range results {
		if r.PlayerID == p.ID && r.PuzzleNo > from {
			mine[r.PuzzleNo] = scoreOf(r)
			if r.Solved && r.Guesses <= 2 {
				quick++
			}
		}
	}
	rows = append(rows, recordRow{Icon: "bolt", Label: t.T("player.record.quick"),
		Value: t.Integer(quick), Sub: t.T("player.record.lastYear")})

	// Rivals: every other active player, by the share of the days both
	// played that this player won outright. A tie is neither.
	type rival struct {
		name      string
		won, lost int
	}
	byID := map[int64]*rival{}
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, o := range group {
			if o.ID != p.ID && o.Active {
				byID[o.ID] = &rival{name: o.Name}
			}
		}
	}
	for _, r := range results {
		o, ok := byID[r.PlayerID]
		if !ok || r.PuzzleNo <= from {
			continue
		}
		m, played := mine[r.PuzzleNo]
		if !played {
			continue
		}
		switch theirs := scoreOf(r); {
		case m < theirs:
			o.won++
		case theirs < m:
			o.lost++
		}
	}
	var rivals []rival
	for _, o := range byID {
		if o.won+o.lost > 0 {
			rivals = append(rivals, *o)
		}
	}
	share := func(r rival) float64 { return float64(r.won) / float64(r.won+r.lost) }
	sort.Slice(rivals, func(i, j int) bool {
		if a, b := share(rivals[i]), share(rivals[j]); a != b {
			return a < b
		}
		return rivals[i].name < rivals[j].name
	})
	if len(rivals) >= 2 {
		nem, fav := rivals[0], rivals[len(rivals)-1]
		rows = append(rows,
			recordRow{Icon: "swords", Label: t.T("player.record.nemesis"), Value: nem.name,
				Sub: t.T("player.record.nemesisSub", p.Name, nem.lost, nem.won)},
			recordRow{Icon: "sentiment_satisfied", Label: t.T("player.record.favourite"), Value: fav.name,
				Sub: t.T("player.record.favouriteSub", p.Name, fav.won, fav.lost)},
		)
	}
	return rows
}

// weekdayBar is one day of the week: the player's average on it, drawn as a
// bar, with the group's as a tick across it.
type weekdayBar struct {
	Day   string
	Value string
	Width int
	Tick  int
	// Mark is "hardest" or "easiest" for the player's worst and best day.
	Mark string
}

// playerWeekdays averages the last year by day of the week, Monday first,
// for the player and for the group. A miss counts as 7.
func playerWeekdays(t translator, p stats.Player, results []store.BoardResult, current int) ([]weekdayBar, string) {
	var mineSum, groupSum [7]float64
	var mineN, groupN [7]int
	from := current - recordsWindow
	for _, r := range results {
		if r.PuzzleNo <= from || r.PuzzleNo > current {
			continue
		}
		w := (int(r.Date.Weekday()) + 6) % 7
		groupSum[w] += scoreOf(r)
		groupN[w]++
		if r.PlayerID == p.ID {
			mineSum[w] += scoreOf(r)
			mineN[w]++
		}
	}
	var mine, group [7]float64
	lo, hi := 7.0, 1.0
	hardest, easiest := -1, -1
	for w := range 7 {
		if groupN[w] > 0 {
			group[w] = groupSum[w] / float64(groupN[w])
			lo, hi = math.Min(lo, group[w]), math.Max(hi, group[w])
		}
		if mineN[w] == 0 {
			continue
		}
		mine[w] = mineSum[w] / float64(mineN[w])
		lo, hi = math.Min(lo, mine[w]), math.Max(hi, mine[w])
		if hardest < 0 || mine[w] > mine[hardest] {
			hardest = w
		}
		if easiest < 0 || mine[w] < mine[easiest] {
			easiest = w
		}
	}
	if hardest < 0 {
		return nil, ""
	}
	// The scale starts a little under the best day, so the differences
	// between days show rather than seven bars all nearly full.
	lo, hi = math.Max(1, lo-0.6), hi+0.2
	pct := func(v float64) int { return max(4, min(100, int((v-lo)/(hi-lo)*100+0.5))) }

	bars := make([]weekdayBar, 7)
	for w := range 7 {
		// Monday is 1 in Go's numbering and 0 here.
		name := t.T("weekday.short." + strconv.Itoa((w+1)%7))
		bars[w] = weekdayBar{Day: name, Value: "—"}
		if mineN[w] > 0 {
			bars[w].Value = t.Decimal(mine[w], 2)
			bars[w].Width = pct(mine[w])
		}
		if groupN[w] > 0 {
			bars[w].Tick = pct(group[w])
		}
	}
	if hardest != easiest {
		bars[hardest].Mark, bars[easiest].Mark = "hardest", "easiest"
	}
	note := t.T("player.weekday.note", bars[hardest].Day, bars[easiest].Day)
	return bars, note
}

// heatLabel is a month's name over the first heatmap column it starts in.
type heatLabel struct {
	Label string
}

// buildHeatmap lays the last year out as weeks by weekdays, Monday at the
// top, ending with the week that holds today; days after today are padding.
func buildHeatmap(t translator, days dayTable, results []store.BoardResult, playerID int64,
	current int, prefix string) ([]calendarDay, []heatLabel) {

	today, err := wordle.DateForPuzzle(current)
	if err != nil {
		return nil, nil
	}
	byDay := map[string]store.BoardResult{}
	var first time.Time
	for _, r := range results {
		if r.PlayerID == playerID {
			byDay[r.Date.Format(time.DateOnly)] = r
			if first.IsZero() || r.Date.Before(first) {
				first = r.Date
			}
		}
	}
	offset := (int(today.Weekday()) + 6) % 7
	start := today.AddDate(0, 0, -offset-7*(heatWeeks-1))

	cells := make([]calendarDay, 0, heatWeeks*7)
	labels := make([]heatLabel, heatWeeks)
	lastMonth := time.Month(0)
	for w := range heatWeeks {
		monday := start.AddDate(0, 0, 7*w)
		if monday.Month() != lastMonth && w < heatWeeks-2 {
			labels[w].Label = shortMonthName(t, monday.Month())
		}
		lastMonth = monday.Month()
		for d := range 7 {
			day := monday.AddDate(0, 0, d)
			// Before their first result a day is not one they missed, and
			// after today there is no day yet: both are padding.
			cell := calendarDay{Filled: !day.After(today) && !day.Before(first)}
			if r, ok := byDay[day.Format(time.DateOnly)]; ok && cell.Filled {
				cell.Played = true
				cell.Tone = int(worstScore)
				if r.Solved {
					cell.Tone = r.Guesses
				}
				cell.Popup = popupFor(t, days, r, prefix)
				cell.Detail = cell.Popup.Line1
			} else if cell.Filled {
				cell.Title = longDate(t, day)
			}
			cells = append(cells, cell)
		}
	}
	// A month that starts a column or two before the next one's name would
	// print over it; the later, whole month keeps its name.
	for w := range labels {
		if labels[w].Label == "" {
			continue
		}
		for next := w + 1; next < min(w+3, len(labels)); next++ {
			if labels[next].Label != "" {
				labels[w].Label = ""
			}
		}
	}
	return cells, labels
}
