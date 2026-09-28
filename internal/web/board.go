package web

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// boardPage is what the board template renders.
type boardPage struct {
	chrome

	// Toast says what a change to the ranking just did, with its way back;
	// nil when the page was not reached by one. See boardChange.
	Toast *boardToast

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

	// Eyebrow names the range the board covers; Ranges switch it.
	Eyebrow string
	Ranges  []chromeOpt
	Recent  bool

	// Cells are the column heads, in the design's order, a sortable one
	// carrying its link.
	Cells []boardHead

	// H2H is the head-to-head card, nil until a row's ⇄ is pressed.
	H2H *headToHead
}

// boardHead is one column head.
type boardHead struct {
	Class string
	Label string
	// Sort is the column's sort link, nil for a column that does not sort.
	Sort *sortHeader
}

// headToHead compares two players over the days both played: who needed
// fewer guesses, how often. One picked is a hint to pick another.
type headToHead struct {
	Hint         string
	A, B         string
	AWins, BWins string
	// Ahead says which side won more days, or both on a level count.
	AAhead, BAhead bool
	Sub            string
	ClearHref      string
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

	// Move is the change in rank against a week ago, "↑2" or "↓1", and
	// MoveUp which way; empty when it has not moved or was not ranked then.
	Move   string
	MoveUp bool
	// Gap is the line under the name: how far behind the player above, or
	// by how much the leader leads.
	Gap string
	// Compare is on while this row is in the head-to-head, and CompareHref
	// adds or takes it out.
	Compare      bool
	CompareHref  string
	LastSeenText string

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

// IsDefault reports whether the board is ranked the way it is out of the box:
// every game counted, a failure worth 7, a missed day worth nothing.
func (q boardQuery) IsDefault() bool {
	return !q.HardModeOnly && q.CountXAsSeven && !q.CountMissed
}

// rankingRow is one line of the ranking menu.
type rankingRow struct {
	Label string
	// Hint says what the rule does, under its name.
	Hint string
	Href string
	On   bool
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
	// Custom is set when any rule is off its default, and ResetHref then
	// puts them all back.
	Custom    bool
	ResetHref string
}

func rankingMenuFor(t translator, q boardQuery, boardPath string) rankingMenu {
	state := t.T("board.ranking.custom")
	switch {
	case q.IsDefault():
		state = t.T("board.ranking.standard")
	case q.HardModeOnly && q.CountXAsSeven && !q.CountMissed:
		state = t.T("board.ranking.hardOnly")
	}

	menu := rankingMenu{
		State:  state,
		Custom: !q.IsDefault(),
		Groups: []rankingGroup{{
			Kicker: t.T("board.ranking.games"),
			Rows: []rankingRow{{
				Label: t.T("board.ranking.hardOnly"),
				Hint:  t.T("board.ranking.hardOnlyHint"),
				Href:  changeLink(boardPath+q.HardModeHref(), "hard", q.Href()),
				On:    q.HardModeOnly,
			}},
		}, {
			Kicker: t.T("board.ranking.scoring"),
			Rows: []rankingRow{{
				Label: t.T("board.toggle.countX"),
				Hint:  t.T("board.ranking.countXHint"),
				Href:  changeLink(boardPath+q.CountXHref(), "failed", q.Href()),
				On:    q.CountXAsSeven,
			}, {
				Label: t.T("board.toggle.countMissed"),
				Hint:  t.T("board.ranking.countMissedHint"),
				Href:  changeLink(boardPath+q.CountMissedHref(), "missed", q.Href()),
				On:    q.CountMissed,
			}},
		}},
	}
	if menu.Custom {
		menu.ResetHref = boardPath + q.with(func(n *boardQuery) {
			n.HardModeOnly, n.CountXAsSeven, n.CountMissed = false, true, false
		})
		menu.ResetHref = changeLink(menu.ResetHref, "reset", q.Href())
	}
	return menu
}

// boardToast is the note a change to the ranking leaves: what it did, and
// a link back to how it was. Close is the same board without the note.
type boardToast struct {
	Text, Undo, Close string
}

// changeLink marks a control's link as a change, carrying the query it
// changes from (back, "" or "?…") so the page it lands on can offer Undo.
// The design has a toast with Undo after every ranking change; there is
// no script to remember the earlier state, so the link does.
func changeLink(href, code, back string) string {
	v := url.Values{}
	v.Set("changed", code)
	v.Set("undo", back)
	switch {
	case strings.HasSuffix(href, "?"):
		return href + v.Encode()
	case strings.Contains(href, "?"):
		return href + "&" + v.Encode()
	}
	return href + "?" + v.Encode()
}

// boardChange words the toast for a change that has just been made, from
// the state it produced. undo is only ever a query string: anything else in
// it is ignored rather than followed.
func boardChange(t translator, changed, undo string, q boardQuery, boardPath string, recent bool) *boardToast {
	var key string
	switch changed {
	case "hard":
		key = map[bool]string{true: "board.toast.hardOn", false: "board.toast.hardOff"}[q.HardModeOnly]
	case "failed":
		key = map[bool]string{true: "board.toast.failedOn", false: "board.toast.failedOff"}[q.CountXAsSeven]
	case "missed":
		key = map[bool]string{true: "board.toast.missedOn", false: "board.toast.missedOff"}[q.CountMissed]
	case "reset":
		key = "board.toast.reset"
	case "range":
		if recent {
			return &boardToast{Text: t.T("board.toast.recent", boardRecent), Undo: undoHref(boardPath, undo), Close: boardPath + q.Href()}
		}
		key = "board.toast.allTime"
	default:
		return nil
	}
	return &boardToast{Text: t.T(key), Undo: undoHref(boardPath, undo), Close: boardPath + q.Href()}
}

// undoHref is the board at an earlier query, or "" when undo is not one.
func undoHref(boardPath, undo string) string {
	if undo == "" {
		return boardPath
	}
	if !strings.HasPrefix(undo, "?") {
		return ""
	}
	if _, err := url.ParseQuery(undo[1:]); err != nil {
		return ""
	}
	return boardPath + undo
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

// boardRecent is the board's short range, in puzzles.
const boardRecent = 90

// moveWindow is how far back a row's rank arrow looks: a week, rolling.
const moveWindow = 7

// handleBoard renders the board, authenticated or shared.
//
// All time by default, the last 90 puzzles on request. The arrows beside a
// rank compare with the same board a week ago, rolling. The streak is the
// whole history's whatever the range, since a range that cut a streak short
// would report a streak nobody has.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request, prefix, boardPath string, readOnly bool) {
	changed, undo := r.URL.Query().Get("changed"), r.URL.Query().Get("undo")
	if changed != "" || undo != "" {
		// Read once, then gone from every link this page builds: the toast
		// belongs to the change that led here, not to the next one.
		clean := r.URL.Query()
		clean.Del("changed")
		clean.Del("undo")
		r = r.Clone(r.Context())
		r.URL.RawQuery = clean.Encode()
	}
	full, players, results, query, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	now := time.Now()
	opts := stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           now,
	}
	recent := r.URL.Query().Get("range") == strconv.Itoa(boardRecent)
	span := 0
	if recent {
		span = boardRecent
	}
	inRange := results
	board := full
	if recent {
		inRange = stats.GridWindow(results, opts, span)
		board = stats.Compute(players, inRange, opts)
	}

	// The same board a week ago: the history up to then, over the same
	// range ending then.
	weekAgo := opts
	weekAgo.Now = now.AddDate(0, 0, -moveWindow)
	var before []store.BoardResult
	for _, res := range results {
		if res.PuzzleNo <= full.CurrentPuzzle-moveWindow {
			before = append(before, res)
		}
	}
	if recent {
		before = stats.GridWindow(before, weekAgo, span)
	}
	then := map[int64]int{}
	for _, p := range stats.Compute(players, before, weekAgo).Ranked {
		then[p.ID] = p.Rank
	}
	streaks := map[int64]stats.Player{}
	for _, group := range [][]stats.Player{full.Ranked, full.Unranked} {
		for _, p := range group {
			streaks[p.ID] = p
		}
	}

	ch := s.newChrome(w, r, prefix, viewBoard, readOnly)
	t := ch.T
	page := boardPage{
		chrome:     ch,
		Board:      board,
		Prefix:     prefix,
		BoardPath:  boardPath,
		Query:      query,
		Ranking:    rankingMenuFor(t, query, boardPath),
		Toast:      boardChange(t, changed, undo, query, boardPath, r.URL.Query().Get("range") == strconv.Itoa(boardRecent)),
		GroupPath:  template.HTML(sparkPath(full.GroupSeries, sparkWidth, sparkHeight, 0)),
		MinGames:   stats.MinGames,
		FormWindow: stats.FormWindow,
		Recent:     recent,
	}

	puzzles := map[int]bool{}
	for _, res := range inRange {
		puzzles[res.PuzzleNo] = true
	}
	page.Eyebrow = t.TN("board.range.allPuzzles", len(puzzles))
	if recent {
		page.Eyebrow = t.T("board.range.recentLong", boardRecent)
	}
	back := query.Href()
	page.Ranges = []chromeOpt{
		{Label: t.T("board.range.all"), Href: changeLink(withoutParam(r, "range"), "range", back), On: !recent},
		{Label: t.T("board.range.recent", boardRecent), Href: changeLink(urlWith(r, "range", strconv.Itoa(boardRecent)), "range", back), On: recent},
	}
	// The one already in force goes nowhere new, so it says nothing either.
	for i := range page.Ranges {
		if page.Ranges[i].On {
			page.Ranges[i].Href = boardPath + back
		}
	}

	// Who is in the head-to-head: up to two slugs, ranked players only.
	ranked := map[string]stats.Player{}
	for _, p := range board.Ranked {
		ranked[p.Slug] = p
	}
	var cmp []string
	for _, slug := range strings.Split(r.URL.Query().Get("cmp"), ",") {
		if _, ok := ranked[slug]; ok && !slices.Contains(cmp, slug) {
			cmp = append(cmp, slug)
		}
	}
	if len(cmp) > 2 {
		cmp = cmp[len(cmp)-2:]
	}

	traits := stats.NewTraiter(board)
	build := func(p stats.Player) boardRow {
		row := s.newBoardRow(p, prefix, t, traits, results, board.CurrentPuzzle)
		if whole, ok := streaks[p.ID]; ok {
			row.PlayStreak = whole.PlayStreak
			row.StreakText = "—"
			if whole.PlayStreak > 0 {
				row.StreakText = t.Integer(whole.PlayStreak)
			}
		}
		return row
	}

	var rows, unranked []boardRow
	for i, p := range board.Ranked {
		row := build(p)
		if was, ok := then[p.ID]; ok && was != p.Rank {
			row.MoveUp = was > p.Rank
			if row.MoveUp {
				row.Move = "↑" + t.Integer(was-p.Rank)
			} else {
				row.Move = "↓" + t.Integer(p.Rank-was)
			}
		}
		row.Gap = gapLine(t, board.Ranked, i)
		row.Compare = slices.Contains(cmp, p.Slug)
		row.CompareHref = compareHref(r, cmp, p.Slug)
		rows = append(rows, row)
	}
	for _, p := range board.Unranked {
		unranked = append(unranked, build(p))
	}
	page.Sort = parseBoardSort(r)
	sortRows(rows, page.Sort)
	sortRows(unranked, page.Sort)
	page.Rows = append(rows, unranked...)
	page.Cells = boardHeads(t, s.headersFor(r, page.Sort, t))

	if len(cmp) > 0 {
		page.H2H = headToHeadFor(t, ranked, cmp, inRange, recent, withoutParam(r, "cmp"))
	}

	if !readOnly {
		token, err := s.issueCSRFToken(w, r)
		if err != nil {
			s.logger.Error("issue csrf token", "error", err)
			s.renderError(w, r, http.StatusInternalServerError)
			return
		}
		page.CSRFToken = token
	}
	page.Live = s.liveViewFor(r, prefix)

	s.render(w, r, http.StatusOK, "board.html", page)
}

// boardHeads lays the column heads out in the design's order, taking the
// sort links for the columns that sort.
func boardHeads(t translator, headers []sortHeader) []boardHead {
	sortable := map[string]*sortHeader{}
	for i := range headers {
		sortable[headers[i].Column] = &headers[i]
	}
	head := func(class, column, key string) boardHead {
		h := boardHead{Class: class, Label: t.T(key)}
		if column != "" {
			h.Sort = sortable[column]
		}
		return h
	}
	return []boardHead{
		head("b-rank", sortRank, "board.column.rank"),
		head("b-id", sortPlayer, "board.column.player"),
		head("b-avg right", sortAverage, "board.column.average"),
		head("b-form right", sortForm, "board.column.form"),
		head("b-spark", "", "board.column.last30"),
		head("b-games right", sortGames, "board.column.games"),
		head("b-five", "", "board.column.lastFive"),
		head("b-streak right", sortStreak, "board.column.streak"),
	}
}

// gapLine is what a ranked row says under its name: how far behind the
// player above it, level with them, or — for the leader — by how much it
// leads. Measured on the averages as printed, so the line never
// contradicts the two figures a reader can see.
func gapLine(t translator, ranked []stats.Player, i int) string {
	shown := func(p stats.Player) float64 {
		if p.Average == nil {
			return 0
		}
		return math.Round(*p.Average*100) / 100
	}
	me := shown(ranked[i])
	if i > 0 {
		above := ranked[i-1]
		if d := me - shown(above); d > 0.004 {
			return t.T("board.gap.behind", t.Decimal(d, 2), above.Name)
		}
		return t.T("board.gap.level", above.Name)
	}
	if len(ranked) > 1 {
		below := ranked[1]
		if d := shown(below) - me; d > 0.004 {
			return t.T("board.gap.leads", t.Decimal(d, 2))
		}
		return t.T("board.gap.level", below.Name)
	}
	return ""
}

// compareHref adds a player to the head-to-head, or takes them out; a third
// pick drops the older of the two.
func compareHref(r *http.Request, cmp []string, slug string) string {
	var next []string
	if slices.Contains(cmp, slug) {
		for _, c := range cmp {
			if c != slug {
				next = append(next, c)
			}
		}
	} else {
		next = append(append(next, cmp...), slug)
		if len(next) > 2 {
			next = next[len(next)-2:]
		}
	}
	if len(next) == 0 {
		return withoutParam(r, "cmp")
	}
	return urlWith(r, "cmp", strings.Join(next, ","))
}

// withoutParam is the current URL with one parameter taken out.
func withoutParam(r *http.Request, key string) string {
	q := r.URL.Query()
	q.Del(key)
	q.Del("partial")
	path := r.URL.EscapedPath()
	if encoded := q.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

// headToHeadFor counts the days two players both played in the range: who
// needed fewer guesses, a miss as 7, and how many were level.
func headToHeadFor(t translator, ranked map[string]stats.Player, cmp []string,
	results []store.BoardResult, recent bool, clearHref string) *headToHead {

	h := &headToHead{ClearHref: clearHref}
	if len(cmp) == 1 {
		h.Hint = t.T("board.h2h.pick", ranked[cmp[0]].Name)
		return h
	}
	a, b := ranked[cmp[0]], ranked[cmp[1]]
	scores := map[int64]map[int]float64{a.ID: {}, b.ID: {}}
	for _, r := range results {
		if m, ok := scores[r.PlayerID]; ok {
			m[r.PuzzleNo] = scoreOf(r)
		}
	}
	var aw, bw, level, n int
	var diff float64
	for puzzle, av := range scores[a.ID] {
		bv, ok := scores[b.ID][puzzle]
		if !ok {
			continue
		}
		n++
		diff += av - bv
		switch {
		case av < bv:
			aw++
		case bv < av:
			bw++
		default:
			level++
		}
	}
	h.A, h.B = a.Name, b.Name
	h.AWins, h.BWins = t.Integer(aw), t.Integer(bw)
	h.AAhead, h.BAhead = aw >= bw, bw >= aw
	key := "board.h2h.sub"
	if recent {
		key = "board.h2h.subRecent"
	}
	lead := a.Name
	if diff > 0 {
		lead = b.Name
	}
	h.Sub = t.TP(key, n, n, level, lead, t.Decimal(math.Abs(diff)/float64(max(1, n)), 2))
	return h
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
		Href:        prefix + "/players/" + p.Slug,
		LastFive:    cells[len(cells)-5:],
	}

	if p.LastPlayed != nil {
		row.LastPuzzle = wordle.PuzzleForDate(*p.LastPlayed)
		row.LastPuzzleDate = p.LastPlayed.Format(time.DateOnly)
	}

	// Days in a row with a result, a failure included: the streak the
	// board shows is turning up. The solving kind is on a player's page.
	if p.PlayStreak > 0 {
		row.StreakText = t.Integer(p.PlayStreak)
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
