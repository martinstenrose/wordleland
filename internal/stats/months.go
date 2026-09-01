package stats

import (
	"sort"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// MonthPlayer is one player's month.
type MonthPlayer struct {
	store.Player

	Games int
	// Average is nil only when the selected scoring rules leave no result
	// with a value, for example a month containing only failures while
	// failures-as-seven is disabled.
	Average *float64

	// ThreeOrBetter counts solves in three guesses or fewer.
	ThreeOrBetter int
	Fails         int

	// BestRun is the longest unbroken run of solved puzzles inside the
	// month: a streak, bounded by the month rather than running past its
	// edges. A failure and a missed day both break it, as everywhere else.
	// It is bounded by the month, so a run spanning a month boundary is
	// reported in both rather than in neither.
	BestRun int

	Rank int
}

// Month is one calendar month of play.
type Month struct {
	Year  int
	Month time.Month

	First, Last int // puzzle numbers covered by the monthly scoring span
	Days        int // concluded calendar puzzles, plus today once played

	GroupAverage *float64

	// Ranked is ordered by average ascending; Thin holds those whose results
	// have no value under the selected scoring rules.
	Ranked []MonthPlayer
	Thin   []MonthPlayer

	// Winners is everyone tied at the lowest average. It is a slice because
	// a tie is a real outcome and picking one of them arbitrarily would
	// invent a result.
	Winners []MonthPlayer

	// Margin is the gap to the next distinct average, nil when there is
	// nobody to be ahead of.
	Margin *float64
}

// Complete reports whether the month is finished, which is what lets a view
// mark the current month as still running.
func (m Month) Complete(now time.Time) bool {
	return m.Year < now.Year() || (m.Year == now.Year() && m.Month < now.Month())
}

// ComputeMonths groups the history by calendar month, newest first.
//
// Options apply as they do on the board: the same toggles change these
// figures the same way, so a reader moving between the two views is not
// comparing numbers computed on different terms.
func ComputeMonths(players []store.Player, results []store.BoardResult, opts Options) []Month {
	byPlayer := make(map[int64]store.Player, len(players))
	for _, p := range players {
		byPlayer[p.ID] = p
	}
	if opts.HardModeOnly {
		results = filterHardMode(results)
	}

	type key struct {
		year  int
		month time.Month
	}
	grouped := make(map[key][]store.BoardResult)
	for _, r := range results {
		if _, ok := byPlayer[r.PlayerID]; !ok {
			continue
		}
		k := key{r.Date.Year(), r.Date.Month()}
		grouped[k] = append(grouped[k], r)
	}

	months := make([]Month, 0, len(grouped))
	for k, rows := range grouped {
		months = append(months, buildMonth(k.year, k.month, rows, byPlayer, opts))
	}
	sort.Slice(months, func(i, j int) bool {
		if months[i].Year != months[j].Year {
			return months[i].Year > months[j].Year
		}
		return months[i].Month > months[j].Month
	})
	return months
}

func buildMonth(year int, month time.Month, rows []store.BoardResult,
	byPlayer map[int64]store.Player, opts Options) Month {

	m := Month{Year: year, Month: month}

	// The monthly competition starts on the calendar month's first puzzle.
	// Today's puzzle is still in progress: a player who hasn't posted yet
	// may still play it correctly, so it cannot be scored as missed until
	// the day is over. A player who has already played it gets their real
	// score regardless, through the ordinary value(r, opts) path.
	// firstPuzzle and monthEndPuzzle bound the month in puzzle-number space,
	// the same space wordle.PuzzleForDate normalizes every other day into;
	// comparing puzzle numbers here (rather than a second, hand-built
	// today/monthStart pair of time.Time values) keeps this function from
	// growing its own, possibly divergent, notion of a calendar day.
	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, opts.Now.Location())
	monthEnd := monthStart.AddDate(0, 1, 0)
	firstPuzzle := wordle.PuzzleForDate(monthStart)
	monthEndPuzzle := wordle.PuzzleForDate(monthEnd)
	current := wordle.PuzzleForDate(opts.Now)

	concludedThrough := max(min(current, monthEndPuzzle), firstPuzzle)
	concludedPuzzles := concludedThrough - firstPuzzle

	perPlayer := make(map[int64][]store.BoardResult)
	puzzles := make(map[int]bool)
	var group scoreAccumulator
	for _, r := range rows {
		perPlayer[r.PlayerID] = append(perPlayer[r.PlayerID], r)
		puzzles[r.PuzzleNo] = true
		if m.First == 0 || r.PuzzleNo < m.First {
			m.First = r.PuzzleNo
		}
		if r.PuzzleNo > m.Last {
			m.Last = r.PuzzleNo
		}
		if v, ok := value(r, opts); ok {
			group.add(v)
		}
	}
	// Completed calendar days form the common denominator. Include today in
	// the displayed span only after somebody has posted it; unplayed today
	// is neither a score nor a miss yet.
	m.Days = concludedPuzzles
	if puzzles[current] && current >= firstPuzzle && current < monthEndPuzzle {
		m.Days++
	}
	if m.Days > 0 {
		m.First = firstPuzzle
		m.Last = firstPuzzle + m.Days - 1
	}
	if group.count > 0 {
		avg := group.mean()
		m.GroupAverage = &avg
	}

	for id, history := range perPlayer {
		sort.Slice(history, func(i, j int) bool { return history[i].PuzzleNo < history[j].PuzzleNo })

		mp := MonthPlayer{Player: byPlayer[id], Games: len(history)}
		var acc scoreAccumulator
		for _, r := range history {
			if v, ok := value(r, opts); ok {
				acc.add(v)
			}
			switch {
			case !r.Solved:
				mp.Fails++
			case r.Guesses <= 3:
				mp.ThreeOrBetter++
			}
		}

		// A month is a competition over a fixed set of days, so a day not
		// played counts as a failure rather than as nothing. Without this
		// the way to win a month is to play only your good days, and
		// somebody who turned up eleven times could beat somebody who
		// turned up thirty.
		//
		// Not the board's toggle: there the question is a career average
		// over a window each player defines by turning up, and counting
		// absences is a way of looking at it. Here the window is the month
		// and everybody had the same one.
		//
		// The denominator is every concluded calendar day in the month,
		// whether or not somebody else posted it. It follows CountXAsSeven,
		// because with a failure scored as nothing there is no number an
		// absence could take either.
		//
		// It is also bounded to concludedPuzzles, not puzzles: today isn't
		// missed until it's over, even if somebody else has already played
		// it.
		if opts.CountXAsSeven {
			playedConcluded := 0
			for _, r := range history {
				if r.PuzzleNo >= firstPuzzle && r.PuzzleNo < concludedThrough {
					playedConcluded++
				}
			}
			for missed := concludedPuzzles - playedConcluded; missed > 0; missed-- {
				acc.add(failedAsSeven)
			}
		}

		if acc.count > 0 {
			avg := acc.mean()
			mp.Average = &avg
		}
		mp.BestRun = bestRunWithin(history)

		if mp.Average != nil {
			m.Ranked = append(m.Ranked, mp)
		} else {
			m.Thin = append(m.Thin, mp)
		}
	}

	sort.Slice(m.Ranked, func(i, j int) bool {
		a, b := m.Ranked[i], m.Ranked[j]
		if *a.Average != *b.Average {
			return *a.Average < *b.Average
		}
		if a.Games != b.Games {
			return a.Games > b.Games
		}
		return a.Slug < b.Slug
	})
	sort.Slice(m.Thin, func(i, j int) bool {
		if m.Thin[i].Games != m.Thin[j].Games {
			return m.Thin[i].Games > m.Thin[j].Games
		}
		return m.Thin[i].Slug < m.Thin[j].Slug
	})

	for i := range m.Ranked {
		m.Ranked[i].Rank = i + 1
	}
	if len(m.Ranked) > 0 {
		lowest := *m.Ranked[0].Average
		for _, p := range m.Ranked {
			if *p.Average == lowest {
				m.Winners = append(m.Winners, p)
			}
		}
		// Competition ranking: players tied on an average share a rank, and
		// the next distinct average skips the places the tie used up.
		// Breaking a tie by sort order would name a winner the data does
		// not.
		place := 1
		for i := range m.Ranked {
			if i > 0 && *m.Ranked[i].Average != *m.Ranked[i-1].Average {
				place = i + 1
			}
			m.Ranked[i].Rank = place
		}
		if next := len(m.Winners); next < len(m.Ranked) {
			margin := *m.Ranked[next].Average - lowest
			m.Margin = &margin
		}
	}
	return m
}

// bestRunWithin is the longest streak of consecutive solved puzzles inside
// the slice. A missed day and a failure both break it, exactly as the
// board's streak does.
func bestRunWithin(history []store.BoardResult) int {
	best, run, prev := 0, 0, 0
	for _, r := range history {
		if !r.Solved {
			run = 0
			prev = r.PuzzleNo
			continue
		}
		if prev != 0 && r.PuzzleNo == prev+1 {
			run++
		} else {
			run = 1
		}
		prev = r.PuzzleNo
		if run > best {
			best = run
		}
	}
	return best
}

// SeasonMark is one player's finish in one month.
type SeasonMark struct {
	Year  int
	Month time.Month
	// Rank is 0 when they were not ranked that month, which is different
	// from finishing last and is shown as such.
	Rank int
	Won  bool
	// Running marks a month that has not finished, so a placing in it is
	// provisional rather than a result.
	Running bool
}

// SeasonRow is one player's season.
type SeasonRow struct {
	store.Player
	// Wins counts closed months only. A month still being played has no
	// winner yet, so counting it would hand somebody a title they might
	// not keep.
	Wins int

	// Podiums counts finishes in the top three, closed months and the one
	// in progress alike: a placing is a placing, where a win is a title.
	Podiums int

	// Best is the lowest month average they have managed, with the month it
	// came in. Nil when they have never been ranked.
	Best      *float64
	BestYear  int
	BestMonth time.Month

	// Ranked counts the months in which they had a rank, which breaks ties
	// between players with the same wins and podiums.
	Ranked int

	Marks []SeasonMark
}

// Season is the standing across every month, newest month last so the marks
// read left to right in the order they happened.
type Season struct {
	Rows   []SeasonRow
	Months []Month
}

// ComputeSeason totals the monthly results.
//
// Ordered by wins, then podiums, then by how many months they were ranked
// in at all, then by their best month average, then by name — so a player
// who won twice from three months sits above one who won twice from
// twelve, and ties do not shuffle between page loads.
func ComputeSeason(months []Month, now time.Time) Season {
	season := Season{Months: months}

	type acc struct {
		player  store.Player
		wins    int
		podiums int
		ranked  int
		best    *float64
		bestY   int
		bestM   time.Month
		marks   map[string]SeasonMark
	}
	byPlayer := map[int64]*acc{}

	for _, m := range months {
		closed := m.Complete(now)
		winners := map[int64]bool{}
		for _, w := range m.Winners {
			winners[w.ID] = true
		}

		for _, p := range append(append([]MonthPlayer{}, m.Ranked...), m.Thin...) {
			a := byPlayer[p.ID]
			if a == nil {
				a = &acc{player: p.Player, marks: map[string]SeasonMark{}}
				byPlayer[p.ID] = a
			}
			mark := SeasonMark{Year: m.Year, Month: m.Month, Rank: p.Rank, Running: !closed}
			if winners[p.ID] {
				mark.Won = true
				if closed {
					a.wins++
				}
			}
			if p.Rank > 0 {
				a.ranked++
				if p.Rank <= 3 {
					a.podiums++
				}
				if p.Average != nil && (a.best == nil || *p.Average < *a.best) {
					avg := *p.Average
					a.best, a.bestY, a.bestM = &avg, m.Year, m.Month
				}
			}
			a.marks[monthID(m)] = mark
		}
	}

	// Oldest first, so the marks read in the order the months happened.
	order := make([]Month, len(months))
	for i, m := range months {
		order[len(months)-1-i] = m
	}

	for _, a := range byPlayer {
		row := SeasonRow{
			Player: a.player, Wins: a.wins, Podiums: a.podiums,
			Ranked: a.ranked, Best: a.best, BestYear: a.bestY, BestMonth: a.bestM,
		}
		for _, m := range order {
			row.Marks = append(row.Marks, a.marks[monthID(m)])
		}
		season.Rows = append(season.Rows, row)
	}

	// Wins first, then podiums, then months ranked (the tiebreak
	// SeasonRow.Ranked documents), then the best month anybody managed. A
	// player who won twice from three months sits above one who won twice
	// from twelve, and ties do not shuffle between page loads.
	sort.Slice(season.Rows, func(i, j int) bool {
		a, b := season.Rows[i], season.Rows[j]
		switch {
		case a.Wins != b.Wins:
			return a.Wins > b.Wins
		case a.Podiums != b.Podiums:
			return a.Podiums > b.Podiums
		case a.Ranked != b.Ranked:
			return a.Ranked > b.Ranked
		case a.Best != nil && b.Best != nil && *a.Best != *b.Best:
			return *a.Best < *b.Best
		case (a.Best == nil) != (b.Best == nil):
			return b.Best == nil
		default:
			return a.Slug < b.Slug
		}
	})
	return season
}

func monthID(m Month) string {
	return strconv.Itoa(m.Year) + "-" + strconv.Itoa(int(m.Month))
}
