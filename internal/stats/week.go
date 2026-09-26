package stats

import "github.com/martinstenrose/wordleland/internal/store"

// Week is one Monday-to-Sunday week, scored exactly as a month is: a day
// not played counts as a failure, and the figures are the same kind a
// month's are. Its players are MonthPlayers for that reason — the type
// describes one player's span, and a month was the first span there was.
type Week struct {
	// First and Last are the Monday's and the Sunday's puzzle numbers.
	First, Last int

	GroupAverage *float64
	Ranked       []MonthPlayer
	Thin         []MonthPlayer
	Winners      []MonthPlayer
	Margin       *float64
}

// ComputeWeek scores the week whose Monday is puzzle first. Results after the
// Sunday are left out, so a result for the following Monday cannot leak in.
//
// A week is scored as over whatever opts.Now says: every day in it before
// now is concluded, and a day not played on one of those is a miss. The
// weekly recap passes a now after the Sunday, since it only runs on a week
// that has ended or on a Sunday every active player has already played.
func ComputeWeek(players []store.Player, results []store.BoardResult, first int, opts Options) Week {
	byPlayer := make(map[int64]store.Player, len(players))
	for _, p := range players {
		byPlayer[p.ID] = p
	}
	if opts.HardModeOnly {
		results = filterHardMode(results)
	}
	end := first + 7
	var rows []store.BoardResult
	for _, r := range results {
		if _, ok := byPlayer[r.PlayerID]; ok && r.PuzzleNo >= first && r.PuzzleNo < end {
			rows = append(rows, r)
		}
	}
	m := scoreSpan(first, end, rows, byPlayer, opts)
	return Week{
		First: first, Last: end - 1,
		GroupAverage: m.GroupAverage,
		Ranked:       m.Ranked,
		Thin:         m.Thin,
		Winners:      m.Winners,
		Margin:       m.Margin,
	}
}

// firstMonday is the first Monday's puzzle number: puzzle 0 fell on a
// Saturday. Every puzzle is one day, so every Monday since is this plus a
// multiple of seven, whatever the clocks did in between.
const firstMonday = 2

// WeekOf is the Monday puzzle of the week puzzle belongs to.
func WeekOf(puzzle int) int {
	return puzzle - ((puzzle-firstMonday)%7+7)%7
}
