package web

import (
	"html/template"
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
	// Href is the day's own page, which the date links to.
	Href  string
	Cells []gridCellView
}

type gridPage struct {
	chrome

	Prefix    string
	BoardPath string
	Query     boardQuery

	// Columns are the players in finishing order for the window shown, each
	// heading carrying its average: the header is the standings, and the
	// grid under it is how they got there. It was alphabetical beside a
	// separate standings rail; with the average in the heading, one order
	// does both jobs and the rail went.
	Columns []gridColumn
	Rows    []gridRowView

	// Legend names a few tiles of the ramp, the miss and the day not played,
	// so the colours are readable without guessing.
	Legend []gridLegend

	Total int
	Shown int

	Spans []chromeOpt

	// Eyebrow names the window the grid covers.
	Eyebrow string

	Inactive     bool
	InactiveHref string
	Hidden       int

	// InPlace is the attributes the grid's controls carry — the window and
	// the inactive players' switch — which change what the grid shows
	// rather than going to another page. See gridInPlace.
	InPlace template.HTMLAttr

	Empty bool
}

// gridInPlace has a grid control redraw the grid where it is, as the
// board's do (boardInPlace): the grid below the head from the page that
// comes back, the eyebrow (the window) and the head's controls (each
// carries the other's setting) by id, and the address in the bar.
const gridInPlace template.HTMLAttr = `hx-target="#grid-view" hx-select="#grid-view"` +
	` hx-swap="outerHTML show:none transition:false"` +
	` hx-select-oob="#grid-eyebrow,#grid-tools"` +
	` hx-push-url="true"`

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
		Legend:   gridLegendFor(ch.T),
		InPlace:  gridInPlace,
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
			{Code: "recent", Label: ch.T.T("grid.span.recent", stats.GridSpan),
				Href: urlWith(r, "span", strconv.Itoa(stats.GridSpan)), On: span > 0},
			{Code: "all", Label: ch.T.T("grid.span.all", grid.Total),
				Href: urlWith(r, "span", "all"), On: span == 0},
		}
	}

	// The eyebrow says which window the grid and its averages describe.
	page.Eyebrow = ch.T.TN("grid.days", grid.Total)
	for _, opt := range page.Spans {
		if opt.On {
			page.Eyebrow = opt.Label
		}
	}

	if len(grid.Rows) == 0 {
		page.Empty = true
		if !readOnly && !s.issueChromeToken(w, r, &page.chrome) {
			return
		}
		s.render(w, r, http.StatusOK, "grid.html", page)
		return
	}

	// The columns in finishing order, and each row's cells put in the same
	// order: the grid's own is alphabetical.
	index := make(map[int64]int, len(grid.Players))
	for i, p := range grid.Players {
		index[p.ID] = i
	}
	var order []int
	for _, p := range stats.GridRanking(grid.Players) {
		page.Columns = append(page.Columns, gridColumnFor(prefix, p, ch.T))
		order = append(order, index[p.ID])
	}

	for _, row := range grid.Rows {
		date := strconv.Itoa(row.Date.Day()) + " " + shortMonthName(ch.T, row.Date.Month())
		view := gridRowView{PuzzleNo: row.PuzzleNo, Date: date, Href: puzzlePath(prefix, row.PuzzleNo)}
		for _, i := range order {
			c := row.Cells[i]
			cell := gridCellView{Played: c.Played}
			if c.Played {
				cell.Label, cell.Tone = "X", int(worstScore)
				if c.Solved {
					cell.Label, cell.Tone = strconv.Itoa(c.Guesses), c.Guesses
				}
				if c.HardMode {
					cell.Label += "*"
				}
				cell.Title = grid.Players[i].Name + " · " + puzzleDate(ch.T, row.PuzzleNo, row.Date.Format(time.DateOnly))
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

// gridColumnFor is one player as a heading.
func gridColumnFor(prefix string, p stats.Player, t translator) gridColumn {
	col := gridColumn{
		Name: p.Name, Short: shortName(p.Name),
		Href: prefix + "/players/" + p.Slug, Form: formatScore(t, p.Average),
	}
	if p.Ranked() {
		col.Rank = t.Integer(p.Rank)
	}
	return col
}

// gridLegend is one tile in the legend and what it stands for. Tone 0 with
// no label is a day not played.
type gridLegend struct {
	Label string
	Tone  int
	Text  string
}

// gridLegendFor names the tiles a reader has to tell apart: the strong end
// of the ramp, the middle, the weak end, a miss and a day not played.
func gridLegendFor(t translator) []gridLegend {
	return []gridLegend{
		{Label: "2", Tone: 2, Text: t.T("grid.legend.oneTwo")},
		{Label: "4", Tone: 4, Text: t.T("grid.legend.four")},
		{Label: "6", Tone: 6, Text: t.T("grid.legend.six")},
		{Label: "X", Tone: int(worstScore), Text: t.T("grid.legend.failed")},
		{Text: t.T("grid.legend.notPlayed")},
		{Label: "4*", Tone: 4, Text: t.T("grid.legend.hard")},
	}
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
