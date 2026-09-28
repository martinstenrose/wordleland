package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// hardestWindow is how many puzzles back a day is ranked for difficulty
// against: "the 3rd hardest of the last 90 days".
const hardestWindow = 90

// puzzlePage is one day: everyone's score and the squares they posted, with
// the day before and after a press away. Today is this for the current
// puzzle and more; this is the plain record, for any day.
type puzzlePage struct {
	chrome

	PuzzleNo int
	Eyebrow  string
	Title    string
	Sub      string

	PrevHref, NextHref   string
	PrevLabel, NextLabel string

	// GridNote says why a day shows scores without squares: it predates the
	// first grid the board kept, or none has been kept yet.
	GridNote string

	Rows []puzzleRow
}

// puzzleRow is one player's result for the day.
type puzzleRow struct {
	Rank  string
	Name  string
	Href  string
	Label string
	Tone  int
	// Sub is the score against the player's own average, the same figure
	// Today's rows carry.
	Sub       string
	Direction string
	Grid      string
}

// handlePuzzle renders /puzzle/{no}, or the current puzzle for a bare
// /puzzle.
func (s *Server) handlePuzzle(w http.ResponseWriter, r *http.Request, number, prefix string, readOnly bool) {
	board, players, results, _, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	current := board.CurrentPuzzle
	n := current
	if number != "" {
		parsed, err := strconv.Atoi(number)
		// A future puzzle has no results and no date anybody has played; a
		// number from before the game is not a puzzle.
		if err != nil || parsed < 1 || parsed > current {
			s.renderError(w, r, http.StatusNotFound)
			return
		}
		n = parsed
	}
	date, err := wordle.DateForPuzzle(n)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound)
		return
	}

	ch := s.newChrome(w, r, prefix, "", readOnly)
	t := ch.T
	ch.Page = chromeOpt{Code: "puzzle", Label: t.T("puzzle.title"), On: true}

	day := stats.ComputeToday(players, results, n)
	page := puzzlePage{
		chrome:   ch,
		PuzzleNo: n,
		Eyebrow:  longDate(t, date),
		Title:    t.T("player.puzzle", t.Puzzle(n)),
	}

	first, earliest := 0, 0
	for _, res := range results {
		if earliest == 0 || res.PuzzleNo < earliest {
			earliest = res.PuzzleNo
		}
		if res.Grid != "" && (first == 0 || res.PuzzleNo < first) {
			first = res.PuzzleNo
		}
	}
	if n > 1 && (earliest == 0 || n > earliest) {
		page.PrevHref = puzzlePath(prefix, n-1)
		page.PrevLabel = t.T("puzzle.prev", t.Puzzle(n-1))
	}
	if n < current {
		page.NextHref = puzzlePath(prefix, n+1)
		page.NextLabel = t.T("puzzle.next", t.Puzzle(n+1))
	}

	switch {
	case first == 0:
		page.GridNote = t.T("puzzle.noGridsYet")
	case n < first:
		firstDate, _ := wordle.DateForPuzzle(first)
		page.GridNote = t.T("puzzle.gridsFrom", t.Puzzle(first), dayMonthYear(t, firstDate))
	}

	averages := make(map[int64]*float64, len(board.Ranked))
	for _, p := range board.Ranked {
		averages[p.ID] = p.Average
	}

	for i, e := range day.Filed {
		row := puzzleRow{
			Rank: t.Integer(i + 1), Name: e.Name, Href: prefix + "/players/" + e.Slug,
			Label: "X", Tone: 7, Direction: "level", Grid: e.Grid,
		}
		if e.Solved {
			row.Label, row.Tone = strconv.Itoa(e.Guesses), e.Guesses
		}
		if e.HardMode {
			row.Label += "*"
		}
		avg, ranked := averages[e.ID]
		switch {
		case !e.Solved:
			row.Sub, row.Direction = t.T("today.missed"), "worse"
		case ranked && avg != nil:
			delta := float64(e.Guesses) - *avg
			text, dir := formatDelta(t, &delta)
			row.Sub, row.Direction = t.T("puzzle.against", text), dir
		default:
			row.Sub = t.T("puzzle.unranked")
		}
		page.Rows = append(page.Rows, row)
	}

	page.Sub = puzzleSub(t, day, results, n, current)

	if !readOnly {
		if !s.issueChromeToken(w, r, &page.chrome) {
			return
		}
	}
	s.render(w, r, http.StatusOK, "puzzle.html", page)
}

// puzzlePath is a puzzle's address under a prefix.
func puzzlePath(prefix string, n int) string {
	return prefix + "/puzzle/" + strconv.Itoa(n)
}

// puzzleSub is the line under a puzzle's number: the group's average that
// day, how many played, how many missed, and — for a day recent enough to
// compare — where it ranks for difficulty among the last 90. A miss counts
// as 7 throughout, as on Today.
func puzzleSub(t translator, day stats.Today, results []store.BoardResult, n, current int) string {
	if len(day.Filed) == 0 {
		return t.T("puzzle.nobody")
	}
	score := func(solved bool, guesses int) float64 {
		if !solved {
			return 7
		}
		return float64(guesses)
	}
	var sum float64
	fails := 0
	for _, e := range day.Filed {
		sum += score(e.Solved, e.Guesses)
		if !e.Solved {
			fails++
		}
	}
	avg := sum / float64(len(day.Filed))
	parts := []string{
		t.T("puzzle.average", t.Decimal(avg, 2)),
		t.T("puzzle.played", day.FiledCount(), day.Expected()),
		t.TP("puzzle.fails", fails, fails),
	}

	if n > current-hardestWindow {
		sums := map[int]float64{}
		counts := map[int]int{}
		for _, r := range results {
			if r.PuzzleNo > current-hardestWindow && r.PuzzleNo <= current {
				sums[r.PuzzleNo] += score(r.Solved, r.Guesses)
				counts[r.PuzzleNo]++
			}
		}
		rank := 1
		for p, c := range counts {
			if p != n && sums[p]/float64(c) > avg {
				rank++
			}
		}
		if rank == 1 {
			parts = append(parts, t.T("puzzle.hardest"))
		} else {
			parts = append(parts, t.T("puzzle.hardestRank", t.Ordinal(rank)))
		}
	}
	return strings.Join(parts, " · ")
}

// dayMonthYear is "21 September 2026", from the catalogue's month names.
func dayMonthYear(t translator, date time.Time) string {
	return strconv.Itoa(date.Day()) + " " + t.T("month."+strconv.Itoa(int(date.Month()))) + " " + strconv.Itoa(date.Year())
}
