package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// gridColumn is one player heading.
type gridColumn struct {
	Name  string
	Short string
	Href  string
	Rank  string
	Form  string
}

// gridCellView is one cell, pre-formatted.
type gridCellView struct {
	// Label carries a trailing * for hard mode, the same convention the
	// player page's recent strip uses.
	Label  string
	Tone   int
	Played bool
	// Title names the player and the date, for the popup: the column
	// heading it would otherwise repeat can be scrolled out of view on a
	// wide grid, and the row's own date column is not always in view
	// either once a reader has scrolled sideways.
	Title string
}

type gridRowView struct {
	Date     string
	PuzzleNo int
	Cells    []gridCellView
}

type gridPage struct {
	chrome

	Prefix    string
	BoardPath string
	Query     boardQuery

	Columns []gridColumn
	// Rail is the same players in finishing order for the window shown.
	// The grid keeps them alphabetical so a reader can find somebody; the
	// rail is a standings table and has to be in order to be one.
	Rail []gridColumn
	Rows []gridRowView

	// Legend is the score ramp, 1 through X, so the colours are readable
	// without guessing.
	Legend []scoreCell

	Total int
	Shown int

	Spans []chromeOpt

	// FormLabel names the window the column figures are computed over.
	FormLabel string

	Inactive     bool
	InactiveHref string
	Hidden       int

	Empty bool
}

// handleGrid renders every score as days by players.
func (s *Server) handleGrid(w http.ResponseWriter, r *http.Request, prefix, boardPath string, readOnly bool) {
	_, players, results, query, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	showInactive := r.URL.Query().Get("inactive") == "1"

	// Two ranges, as the design has them: the recent stretch by default and
	// the whole history on request.
	span := stats.GridSpan
	if r.URL.Query().Get("span") == "all" {
		span = 0
	}

	opts := stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           time.Now(),
	}

	// The columns are ranked over the window the cells come from. Ranking a
	// whole history by a 30-day figure, or a 90-day view by an all-time
	// one, describes something the reader is not looking at.
	windowed := stats.GridWindow(results, opts, span)
	windowBoard := stats.Compute(players, windowed, opts)

	grid := stats.ComputeGrid(windowBoard, results, opts, showInactive, span)

	ch := s.newChrome(w, r, prefix, viewGrid, readOnly)
	page := gridPage{
		chrome: ch, Prefix: prefix, BoardPath: boardPath, Query: query,
		Total: grid.Total, Shown: len(grid.Rows), Hidden: grid.Hidden,
		Inactive: showInactive,
		Legend:   legendCells(),
	}

	if showInactive {
		page.InactiveHref = urlWith(r, "inactive", "0")
	} else {
		page.InactiveHref = urlWith(r, "inactive", "1")
	}

	// A control that cannot change anything is worse than no control: it
	// invites a click and answers with the same page.
	if grid.Total > stats.GridSpan {
		page.Spans = []chromeOpt{
			{Code: "90", Label: ch.T.T("grid.span.recent", stats.GridSpan),
				Href: urlWith(r, "span", "90"), On: span > 0},
			{Code: "all", Label: ch.T.T("grid.span.all", grid.Total),
				Href: urlWith(r, "span", "all"), On: span == 0},
		}
	}

	// The heading says which window the figures describe.
	page.FormLabel = ch.T.T("grid.form.all", grid.Total)
	if span > 0 && grid.Total > stats.GridSpan {
		page.FormLabel = ch.T.T("grid.form.recent", stats.GridSpan)
	}

	if len(grid.Rows) == 0 {
		page.Empty = true
		if !readOnly && !s.issueChromeToken(w, r, &page.chrome) {
			return
		}
		s.render(w, r, http.StatusOK, "grid.html", page)
		return
	}

	for _, p := range grid.Players {
		page.Columns = append(page.Columns, gridColumnFor(prefix, p))
	}
	for _, p := range stats.GridRanking(grid.Players) {
		page.Rail = append(page.Rail, gridColumnFor(prefix, p))
	}

	for _, row := range grid.Rows {
		view := gridRowView{PuzzleNo: row.PuzzleNo, Date: row.Date.Format("2 Jan")}
		for i, c := range row.Cells {
			cell := gridCellView{Played: c.Played}
			if c.Played {
				cell.Label, cell.Tone = "X", int(worstScore)
				if c.Solved {
					cell.Label, cell.Tone = strconv.Itoa(c.Guesses), c.Guesses
				}
				if c.HardMode {
					cell.Label += "*"
				}
				cell.Title = grid.Players[i].Name + " · " + puzzleDate(row.PuzzleNo, row.Date.Format(time.DateOnly))
			}
			view.Cells = append(view.Cells, cell)
		}
		page.Rows = append(page.Rows, view)
	}

	if !readOnly && !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, http.StatusOK, "grid.html", page)
}

// gridColumnFor is one player as a heading, shared by the grid and the
// rail so the two orders cannot drift into showing different figures.
func gridColumnFor(prefix string, p stats.Player) gridColumn {
	col := gridColumn{
		Name: p.Name, Short: shortName(p.Name),
		Href: prefix + "/p/" + p.Slug, Form: formatScore(p.Average),
	}
	if p.Ranked() {
		col.Rank = strconv.Itoa(p.Rank)
	}
	return col
}

// legendCells is the ramp from a one-guess solve to a failure.
//
// The swatches carry no labels: the row is bookended by "1" and "X", and
// numbering every square as well just reads as "1 1 2 3 4 5 6 X X".
func legendCells() []scoreCell {
	cells := make([]scoreCell, 0, 7)
	for i := 1; i <= int(worstScore); i++ {
		cells = append(cells, scoreCell{Played: true, Solved: i < int(worstScore), Tone: i})
	}
	return cells
}

// shortName abbreviates a column heading, since a grid column is barely
// wider than the score in it.
func shortName(name string) string {
	runes := []rune(name)
	if len(runes) <= 3 {
		return name
	}
	return string(runes[:3])
}
