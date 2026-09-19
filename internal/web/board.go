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
	"github.com/martinstenrose/wordleland/internal/wordle"
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

	// Ranking is those controls collected into one menu.
	Ranking rankingMenu

	// Sort is the display ordering, and Headers carries the column links.
	Sort    boardSort
	Headers []sortHeader

	// MinGames and FormWindow explain the ranking rule in the footer, so
	// the thresholds shown to the reader can never drift from the ones
	// stats actually applies.
	MinGames   int
	FormWindow int
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

	// LastFive is the most recent five puzzles, oldest first, including
	// today — a missed or not-yet-played one is unplayed rather than a
	// score of its own.
	LastFive []scoreCell

	// LastPuzzle is the puzzle number of the player's most recent result,
	// 0 when they have never played. Unlike LastFive it looks at the whole
	// history, not just the last five days, so a lapsed player still shows
	// their real last game rather than nothing.
	LastPuzzle     int
	LastPuzzleDate string

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
	// See urlWith: "partial=1" is how a request was made rather than part of
	// what is being looked at, and a control link carrying it would hand a
	// reader a bare card the moment they followed it without a script.
	values.Del("partial")

	// Only non-default values appear, so a plain board has a clean URL.
	set := func(key, value string, keep bool) {
		if keep {
			values.Set(key, value)
		} else {
			values.Del(key)
		}
	}
	set("mode", "hard", next.HardModeOnly)
	set("failed", "0", !next.CountXAsSeven)
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

// HardModeHref flips the filter rather than setting it, because the control
// it feeds is one row that is either in force or not — the segmented pair of
// "All" and "Hard mode" it replaces needed one href that meant each.
func (q boardQuery) HardModeHref() string {
	return q.with(func(n *boardQuery) { n.HardModeOnly = !n.HardModeOnly })
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

// IsDefault reports whether the board is ranked the way it is out of the box:
// every game counted, a failure worth 7, a missed day worth nothing.
func (q boardQuery) IsDefault() bool {
	return !q.HardModeOnly && q.CountXAsSeven && !q.CountMissed
}

// rankingRow is one line of the ranking menu.
type rankingRow struct {
	Label string
	Href  string
	On    bool
	// Why is shown under the label when the row is in force but currently
	// changes nothing. It used to be a title= on the chip this replaces,
	// which is a hover — and a phone has none, so on the one screen where
	// these controls are most crowded the explanation did not exist.
	Why string
}

// rankingGroup is a headed set of rows. There are two, because the controls
// are two different kinds of thing: one decides which games are counted at
// all, the others decide what a result is worth once it is.
type rankingGroup struct {
	Kicker string
	Rows   []rankingRow
}

// rankingMenu is the board's controls, collected into one.
//
// Three chips in a row is three things to fit, and on a phone they wrapped
// — which put "count missed as 7" alone on a line away from the toggle it
// depends on. One control with the rules inside it fits at every width, and
// gives the dependency somewhere to be stated.
type rankingMenu struct {
	// State is "Standard" or "Custom" rather than a list of what is on. A
	// label built from the selection grows with it and has to be truncated
	// on the width where this matters most; the card's own footer already
	// states the rules in prose, so this says only whether they are the
	// usual ones.
	State  string
	Groups []rankingGroup
}

func rankingMenuFor(t translator, q boardQuery, boardPath string) rankingMenu {
	state := t.T("board.ranking.custom")
	if q.IsDefault() {
		state = t.T("board.ranking.standard")
	}

	missed := rankingRow{
		Label: t.T("board.toggle.countMissed"),
		Href:  boardPath + q.CountMissedHref(),
		On:    q.CountMissed,
	}
	if q.CountMissedMoot() {
		missed.Why = t.T("board.toggle.countMissed.moot")
	}

	return rankingMenu{
		State: state,
		Groups: []rankingGroup{{
			Kicker: t.T("board.ranking.games"),
			Rows: []rankingRow{{
				Label: t.T("board.ranking.hardOnly"),
				Href:  boardPath + q.HardModeHref(),
				On:    q.HardModeOnly,
			}},
		}, {
			Kicker: t.T("board.ranking.scoring"),
			Rows: []rankingRow{{
				Label: t.T("board.toggle.countX"),
				Href:  boardPath + q.CountXHref(),
				On:    q.CountXAsSeven,
			}, missed},
		}},
	}
}

// parseBoardQuery reads the controls, defaulting: count failed as 7 on,
// count missed off, no filter.
func parseBoardQuery(r *http.Request) boardQuery {
	q := boardQuery{CountXAsSeven: true, raw: r.URL.Query()}

	if r.URL.Query().Get("mode") == "hard" {
		q.HardModeOnly = true
	}
	if raw := r.URL.Query().Get("failed"); raw != "" {
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
	board, _, results, query, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	ch := s.newChrome(w, r, prefix, viewBoard, readOnly)
	page := boardPage{
		chrome:     ch,
		Board:      board,
		Prefix:     prefix,
		BoardPath:  boardPath,
		Query:      query,
		Ranking:    rankingMenuFor(ch.T, query, boardPath),
		GroupPath:  template.HTML(sparkPath(board.GroupSeries, sparkWidth, sparkHeight, 0)),
		MinGames:   stats.MinGames,
		FormWindow: stats.FormWindow,
	}
	// Ranked and unranked are ordered as separate groups, so the divider
	// between them holds under every sort.
	traits := stats.NewTraiter(board)
	var ranked, unranked []boardRow
	for _, p := range board.Ranked {
		ranked = append(ranked, s.newBoardRow(p, prefix, page.T, traits, results, board.CurrentPuzzle))
	}
	for _, p := range board.Unranked {
		unranked = append(unranked, s.newBoardRow(p, prefix, page.T, traits, results, board.CurrentPuzzle))
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

	// "?partial=1" asks for just the card, the same way today.go's bench
	// toggle and search.go's overlay reuse their own pages — see app.js — so
	// choosing a ranking rule can swap the board in without a full reload
	// while still working from a plain link when script is absent. This
	// page's "content" block is the card, so there is no second template to
	// keep in step with the first.
	if r.URL.Query().Get("partial") == "1" {
		s.renderBlock(w, r, http.StatusOK, "board.html", "content", page)
		return
	}
	s.render(w, r, http.StatusOK, "board.html", page)
}

// newBoardRow pre-formats one player.
func (s *Server) newBoardRow(p stats.Player, prefix string, t translator, traits stats.Traiter,
	results []store.BoardResult, currentPuzzle int) boardRow {

	cells := recentCells(p, results, currentPuzzle, t)
	row := boardRow{
		Player:      p,
		AverageText: formatScore(t, p.Average),
		FormText:    formatScore(t, p.Form),
		StreakText:  "—",
		SparkPath:   template.HTML(sparkPath(p.Series, sparkWidth, sparkHeight, 0)),
		HasSpark:    hasSparkline(p.Series),
		Href:        prefix + "/p/" + p.Slug,
		LastFive:    cells[len(cells)-5:],
	}

	if p.LastPlayed != nil {
		row.LastPuzzle = wordle.PuzzleForDate(*p.LastPlayed)
		row.LastPuzzleDate = p.LastPlayed.Format(time.DateOnly)
	}

	if p.CurrentStreak > 0 {
		row.StreakText = t.Integer(p.CurrentStreak)
	}
	row.DeltaText, row.DeltaDirection = formatDelta(t, p.Delta)
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
func formatScore(t translator, v *float64) string {
	if v == nil {
		return "—"
	}
	return t.Decimal(*v, 2)
}

// puzzleDate names a puzzle and when it fell, the way every popup that
// names one does: "#1869 (2026-08-01)". One function rather than the
// string built again at each call site, so they cannot drift apart.
func puzzleDate(t translator, puzzleNo int, date string) string {
	return "#" + t.Puzzle(puzzleNo) + " (" + date + ")"
}

// deltaDeadZone is the band within which a delta is not worth colouring. It
// is a display nicety and unrelated to the significance floor that gates
// callouts.
const deltaDeadZone = 0.04

// formatDelta renders the gap between form and average as an arrow and a
// figure. The arrow follows the score, not the standing: a Wordle average
// is better the lower it is, so form pulling away downwards is ▼ and
// green, and drifting upwards is ▲ and red. A signed number said the
// same thing, but the sign that means "improving" there is the minus, and
// that is the one readers took the other way round.
func formatDelta(t translator, delta *float64) (text, direction string) {
	if delta == nil {
		return "", "level"
	}
	d := *delta

	// A delta inside the dead zone is still a delta: printing nothing left
	// a gap where every other row has a figure, which reads as missing data
	// rather than as "no change". It shows as ±0.00 in the muted tone —
	// no arrow, because there is no direction to point in.
	if d > -deltaDeadZone && d < deltaDeadZone {
		return "±" + t.Decimal(0, 2), "level"
	}
	if d < 0 {
		return "▼ " + t.Decimal(-d, 2), "better"
	}
	return "▲ " + t.Decimal(d, 2), "worse"
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
