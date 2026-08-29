package web

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
)

// boardPage is what the board template renders.
type boardPage struct {
	chrome

	Board stats.Board
	Rows  []boardRow

	// Prefix is "" for the authenticated board and "/share/<slug>" for the
	// shared one. Every link is built from it, so the same template serves
	// both without an anonymous visitor being sent into authenticated
	// routing.
	Prefix string
	// BoardPath is the board's own URL, which is not Prefix+"/": the
	// authenticated board lives at /leaderboard while its prefix is empty, so
	// building control links from the prefix pointed them all at "/" — the
	// login route — and every toggle silently did nothing.
	BoardPath string
	// GroupPath is the dashed comparison line.
	GroupPath template.HTML

	// Query rebuilds the current URL with one control changed.
	Query boardQuery

	// Sort is the display ordering, and Headers carries the column links.
	Sort    boardSort
	Headers []sortHeader
}

// boardRow is one player, with everything the template needs pre-formatted
// so the template itself stays free of arithmetic.
type boardRow struct {
	stats.Player

	AverageText string
	FormText    string
	DeltaText   string
	// DeltaDirection is "better", "worse" or "level", for styling.
	DeltaDirection string
	StreakText     string
	ReasonKey      string
	LastSeenText   string

	SparkPath template.HTML
	HasSpark  bool
	Href      string

	// Trait is earned from the figures, empty when nothing was. Why
	// carries the reason, so a player can find out rather than guess.
	Trait string
	Why   string
}

// boardQuery is the board's controls, as query parameters.
type boardQuery struct {
	HardModeOnly  bool
	CountXAsSeven bool
	CountMissed   bool

	// raw is the request's whole query string. The control links are built
	// from it rather than from scratch, so switching a filter keeps the
	// sort, the language and the theme — anything a link rebuilt from three
	// fields would silently drop.
	raw url.Values
}

// with returns the query string for the same board with one control changed.
func (q boardQuery) with(mutate func(*boardQuery)) string {
	next := q
	mutate(&next)

	values := url.Values{}
	for k, v := range q.raw {
		values[k] = v
	}

	// Only non-default values appear, so a plain board has a clean URL.
	set := func(key, value string, keep bool) {
		if keep {
			values.Set(key, value)
		} else {
			values.Del(key)
		}
	}
	set("mode", "hard", next.HardModeOnly)
	set("x7", "0", !next.CountXAsSeven)
	set("missed", "1", next.CountMissed)

	encoded := values.Encode()
	if encoded == "" {
		return ""
	}
	return "?" + encoded
}

// Href is the current query unchanged, for links that go elsewhere and back
// without altering what the reader is looking at.
func (q boardQuery) Href() string { return q.with(func(*boardQuery) {}) }

func (q boardQuery) ModeAllHref() string {
	return q.with(func(n *boardQuery) { n.HardModeOnly = false })
}

func (q boardQuery) ModeHardHref() string {
	return q.with(func(n *boardQuery) { n.HardModeOnly = true })
}

func (q boardQuery) CountXHref() string {
	return q.with(func(n *boardQuery) { n.CountXAsSeven = !n.CountXAsSeven })
}

func (q boardQuery) CountMissedHref() string {
	return q.with(func(n *boardQuery) { n.CountMissed = !n.CountMissed })
}

// CountMissedMoot reports whether "count missed as 7" currently has no
// effect on the averages: without a failure worth 7, a missed day has no
// number to take either. The two toggles still turn independently — this
// only marks the state on the page so a reader is not left wondering why
// selecting it changed nothing.
func (q boardQuery) CountMissedMoot() bool { return !q.CountXAsSeven }

// parseBoardQuery reads the controls, defaulting: count X as 7 on,
// count missed days off, no filter.
func parseBoardQuery(r *http.Request) boardQuery {
	q := boardQuery{CountXAsSeven: true, raw: r.URL.Query()}

	if r.URL.Query().Get("mode") == "hard" {
		q.HardModeOnly = true
	}
	if raw := r.URL.Query().Get("x7"); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			q.CountXAsSeven = v
		}
	}
	if raw := r.URL.Query().Get("missed"); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			q.CountMissed = v
		}
	}
	return q
}

// boardData loads the roster and the whole history and reduces them under
// the controls in the request.
//
// The board and the player page both go through it, so a figure shown in one
// place can never disagree with the same figure in the other — which it would
// if the player page recomputed anything on its own terms.
func (s *Server) boardData(r *http.Request) (stats.Board, []store.Player, []store.BoardResult, boardQuery, error) {
	query := parseBoardQuery(r)

	players, err := store.ListPlayers(r.Context(), s.db)
	if err != nil {
		return stats.Board{}, nil, nil, query, fmt.Errorf("list players: %w", err)
	}
	results, err := store.ResultsForBoard(r.Context(), s.db)
	if err != nil {
		return stats.Board{}, nil, nil, query, fmt.Errorf("read results: %w", err)
	}

	board := stats.Compute(players, results, stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           time.Now(),
	})
	return board, players, results, query, nil
}

// handleBoard renders the board, authenticated or shared.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request, prefix, boardPath string, readOnly bool) {
	board, _, _, query, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	page := boardPage{
		chrome:    s.newChrome(w, r, prefix, viewBoard, readOnly),
		Board:     board,
		Prefix:    prefix,
		BoardPath: boardPath,
		Query:     query,
		GroupPath: template.HTML(sparkPath(board.GroupSeries, sparkWidth, sparkHeight)),
	}
	// Ranked and unranked are ordered as separate groups, so the divider
	// between them holds under every sort.
	traits := stats.NewTraiter(board)
	var ranked, unranked []boardRow
	for _, p := range board.Ranked {
		ranked = append(ranked, s.newBoardRow(p, prefix, page.T, traits))
	}
	for _, p := range board.Unranked {
		unranked = append(unranked, s.newBoardRow(p, prefix, page.T, traits))
	}
	page.Sort = parseBoardSort(r)
	sortRows(ranked, page.Sort)
	sortRows(unranked, page.Sort)
	page.Rows = append(ranked, unranked...)
	page.Headers = s.headersFor(r, page.Sort, page.T)

	if !readOnly {
		token, err := s.issueCSRFToken(w, r)
		if err != nil {
			s.logger.Error("issue csrf token", "error", err)
			s.renderError(w, r, http.StatusInternalServerError)
			return
		}
		page.CSRFToken = token
	}

	s.render(w, r, http.StatusOK, "board.html", page)
}

// newBoardRow pre-formats one player.
func (s *Server) newBoardRow(p stats.Player, prefix string, t translator, traits stats.Traiter) boardRow {
	row := boardRow{
		Player:      p,
		AverageText: formatScore(p.Average),
		FormText:    formatScore(p.Form),
		StreakText:  "—",
		SparkPath:   template.HTML(sparkPath(p.Series, sparkWidth, sparkHeight)),
		HasSpark:    hasSparkline(p.Series),
		Href:        prefix + "/p/" + p.Slug,
	}

	if p.CurrentStreak > 0 {
		row.StreakText = strconv.Itoa(p.CurrentStreak)
	}
	row.DeltaText, row.DeltaDirection = formatDelta(p.Delta)
	row.ReasonKey = reasonKey(p.Reason)

	if key := traits.For(p); key != "" {
		row.Trait = t.T("trait." + key)
		row.Why = t.T("trait." + key + ".why")
	}

	// An unranked player's raw scores stay visible, but the derived
	// figures are withheld — printing an average over three games invites
	// exactly the comparison that separating them off exists to prevent.
	// Form and delta are already undefined for everyone unranked; saying so
	// here keeps the rule in one place rather than resting on that.
	if !p.Ranked() {
		row.AverageText, row.FormText = "—", "—"
		row.DeltaText, row.DeltaDirection = "", "level"
	}

	switch {
	case p.LastPlayed == nil:
		row.LastSeenText = t.T("board.neverPlayed")
	case p.Reason != "":
		row.LastSeenText = t.T("board.lastPlayed", p.LastPlayed.Format(time.DateOnly))
	}
	return row
}

// formatScore renders an average or form figure, or an em dash when it is
// undefined — a suppressed figure shows as a dash rather
// than as a number computed from too little.
func formatScore(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'f', 2, 64)
}

// deltaDeadZone is the band within which a delta is not worth colouring. It
// is a display nicety and unrelated to the significance floor that gates
// callouts.
const deltaDeadZone = 0.04

// formatDelta renders the gap between form and average, signed, using a
// true minus rather than a hyphen.
func formatDelta(delta *float64) (text, direction string) {
	if delta == nil {
		return "", "level"
	}
	d := *delta

	// A delta inside the dead zone is still a delta: printing nothing left
	// a gap where every other row has a figure, which reads as missing data
	// rather than as "no change". It shows as ±0.00 in the muted tone.
	if d > -deltaDeadZone && d < deltaDeadZone {
		return "+0.00", "level"
	}
	if d < 0 {
		// A true minus rather than a hyphen.
		return "−" + strconv.FormatFloat(-d, 'f', 2, 64), "better"
	}
	return "+" + strconv.FormatFloat(d, 'f', 2, 64), "worse"
}

// reasonKey maps a reason to its localised key, so the copy lives in the
// catalogue rather than in the statistics package.
func reasonKey(reason string) string {
	switch reason {
	case stats.ReasonInactive:
		return "board.reason.inactive"
	case stats.ReasonNoRecentGames:
		return "board.reason.noRecentGames"
	case stats.ReasonLowData:
		return "board.reason.lowData"
	default:
		return ""
	}
}
