package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Chart geometry for the player page. Wider and taller than the ledger
// sparkline, because here the shape is the point rather than a hint.
const (
	chartWidth  = 560
	chartHeight = 140

	// recentResults bounds the strip of individual scores. It matches the
	// form window, so the strip and the form figure describe the same games.
	recentResults = stats.FormWindow
)

// scoreCell is one game in the recent strip.
type scoreCell struct {
	PuzzleNo int
	Date     string
	// PuzzleDate is PuzzleNo and Date pre-formatted for the popup — see
	// puzzleDate. Empty on a day not played; the popup does not open then.
	PuzzleDate string
	// Label is the guess count, "X" for a failure, or empty for a day the
	// player did not play — which is the absence of a result, not a zero.
	// A trailing * marks hard mode: the popup already repeats nothing else
	// the box shows, so hard mode has no row of its own there either.
	Label    string
	Played   bool
	Solved   bool
	HardMode bool
	// Tone drives the colour ramp: 1 is the strongest, 6 and X the faintest.
	Tone int
}

// distributionBar is one bucket of the guess distribution.
type distributionBar struct {
	Label string
	Count int
	// Percent of the bar's width against the largest bucket, so the tallest
	// bar always fills the row and the shape stays readable on any roster.
	Percent int
	// Share of this player's games, for the figure printed beside the bar.
	Share string
}

type playerPage struct {
	chrome

	Player stats.Player
	Board  stats.Board

	Prefix    string
	BoardPath string

	Query boardQuery

	// Picker is every player on the board, so the panel can be swapped
	// without going back to it — which is what the design's tab strip does.
	Picker []playerTab

	// Charts. FormPath and GroupPath share one coordinate space so the two
	// lines can be read against each other.
	FormPath  template.HTML
	GroupPath template.HTML
	HasChart  bool
	Gridlines []chartGridline

	Distribution []distributionBar
	Recent       []scoreCell

	// Stats are the four figures beside the name, as the design places
	// them: the same shape the month view uses for its winner.
	Stats []playerStat

	Calendar     []calendarDay
	CalendarRows int

	MonthRanks []monthRank
	RankPath   template.HTML

	// Figures pre-formatted the same way the board formats them.
	AverageText    string
	FormText       string
	DeltaText      string
	DeltaClass     string
	HardModeShare  string
	LastPlayedText string
	ReasonKey      string

	// ChartNoteKey explains why the charts are absent, when they are. It is
	// empty for a player whose charts render.
	ChartNoteKey string

	Trait string
	Why   string
}

// playerStat is one headline figure.
type playerStat struct {
	Label string
	Value string
}

// playerTab is one name in the picker.
type playerTab struct {
	Name string
	Href string
	On   bool
}

// chartGridline is one horizontal rule with the score it marks.
type chartGridline struct {
	Y     string
	Score int
}

// handlePlayer renders one player's detail page, authenticated or shared.
//
// It is served under both prefixes for the same reason the board is: the
// share link is what people are sent, and a board nobody can click into is
// half the feature.
// handlePlayers renders the players view with nobody named, which means the
// player at the top of the board. It exists so the nav has somewhere to
// point: the detail panel is the view, and the picker swaps who is in it.
func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request, prefix, boardPath string, readOnly bool) {
	board, _, _, _, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	slug := ""
	switch {
	case len(board.Ranked) > 0:
		slug = board.Ranked[0].Slug
	case len(board.Unranked) > 0:
		slug = board.Unranked[0].Slug
	default:
		// Nobody to show. The board's own empty state says why, so send the
		// reader there rather than inventing a second one.
		http.Redirect(w, r, boardPath, http.StatusSeeOther)
		return
	}
	s.handlePlayer(w, r, slug, prefix, boardPath, readOnly)
}

func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request, slug, prefix, boardPath string, readOnly bool) {
	board, players, results, query, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	player, ok := findPlayer(board, slug)
	if !ok {
		// Either no such player, or one the current filter excludes. Both
		// are a 404 for this URL: under mode=hard a player with no hard-mode
		// games has no page to show, and inventing an empty one would
		// contradict the board that just left them out.
		s.renderError(w, r, http.StatusNotFound)
		return
	}

	ch := s.newChrome(w, r, prefix, viewPlayers, readOnly)
	t := ch.T
	page := playerPage{
		chrome:    ch,
		Player:    player,
		Board:     board,
		Prefix:    prefix,
		BoardPath: boardPath,
		Query:     query,

		Trait: traitOf(player, board, t),
		Why:   traitWhy(player, board, t),

		AverageText: formatScore(player.Average),
		FormText:    formatScore(player.Form),
		ReasonKey:   reasonKey(player.Reason),

		FormPath:  template.HTML(sparkPath(player.Series, chartWidth, chartHeight)),
		GroupPath: template.HTML(sparkPath(board.GroupSeries, chartWidth, chartHeight)),
		HasChart:  hasSparkline(player.Series),
		Gridlines: chartGridlines(),

		Distribution: distributionBars(player, t),
		Recent:       recentCells(player, results, board.CurrentPuzzle),
		Calendar:     buildCalendar(results, player.ID),
		CalendarRows: weekdays,
	}

	// Ranked against the whole roster, not against themselves: a month
	// computed over one player would place them first every time.
	months := stats.ComputeMonths(players, results, stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           time.Now(),
	})
	ranks, path := buildMonthRanks(months, player.ID, t)
	page.MonthRanks = ranks
	page.RankPath = template.HTML(path)

	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			page.Picker = append(page.Picker, playerTab{
				Name: p.Name, Href: prefix + "/p/" + p.Slug, On: p.ID == player.ID,
			})
		}
	}

	page.DeltaText, page.DeltaClass = formatDelta(player.Delta)

	rank := "—"
	if player.Ranked() {
		rank = "#" + strconv.Itoa(player.Rank)
	}
	streak := "—"
	if player.CurrentStreak > 0 {
		streak = strconv.Itoa(player.CurrentStreak)
	}
	page.Stats = []playerStat{
		{Label: t.T("board.column.form"), Value: formatScore(player.Form)},
		{Label: t.T("board.column.average"), Value: formatScore(player.Average)},
		{Label: t.T("player.currentStreak"), Value: streak},
		{Label: t.T("board.column.rank"), Value: rank},
	}

	if player.LastPlayed != nil {
		page.LastPlayedText = player.LastPlayed.Format(time.DateOnly)
	}

	// The derived figures are withheld below the ranking threshold, on this
	// page for the same reason as on the board.
	if !player.Ranked() {
		page.AverageText, page.FormText = "—", "—"
		page.DeltaText, page.DeltaClass = "", "level"
		page.Stats[0].Value, page.Stats[1].Value = "—", "—"
	}

	// The chart is suppressed for unranked players, but why differs and the
	// page has to say the right one: someone with sixty games who stopped in
	// June is not short of history, they are short of recent history.
	if !player.Ranked() && player.Games > 0 {
		switch player.Reason {
		case stats.ReasonNoRecentGames:
			page.ChartNoteKey = "player.noRecent"
		default:
			page.ChartNoteKey = "player.thin"
		}
	}

	if player.Games > 0 {
		page.HardModeShare = strconv.Itoa(percent(player.HardModeGames, player.Games)) + "%"
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

	s.render(w, r, http.StatusOK, "player.html", page)
}

// findPlayer looks the player up in the computed board rather than the
// database, so the page and the board always agree about them.
func findPlayer(board stats.Board, slug string) (stats.Player, bool) {
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			if p.Slug == slug {
				return p, true
			}
		}
	}
	return stats.Player{}, false
}

// chartGridlines marks scores 1 through 7 on the form chart.
func chartGridlines() []chartGridline {
	var lines []chartGridline
	for score := int(bestScore); score <= int(worstScore); score++ {
		y := (float64(score) - bestScore) / (worstScore - bestScore) * chartHeight
		lines = append(lines, chartGridline{Y: strconv.FormatFloat(y, 'f', 1, 64), Score: score})
	}
	return lines
}

// distributionBars scales each bucket against the largest one, so the shape
// is readable whether a player has forty games or four hundred.
func distributionBars(p stats.Player, t translator) []distributionBar {
	var largest int
	for _, n := range p.Distribution {
		if n > largest {
			largest = n
		}
	}

	bars := make([]distributionBar, 0, len(p.Distribution))
	for i, n := range p.Distribution {
		label := strconv.Itoa(i + 1)
		if i == len(p.Distribution)-1 {
			label = t.T("player.failed")
		}
		bar := distributionBar{Label: label, Count: n}
		if largest > 0 {
			bar.Percent = percent(n, largest)
		}
		if p.Games > 0 {
			bar.Share = strconv.Itoa(percent(n, p.Games)) + "%"
		}
		bars = append(bars, bar)
	}
	return bars
}

// recentCells builds the strip of individual results for the form window.
//
// Days the player did not play are included as empty cells rather than
// omitted: the gaps are the point of the strip, and a run of results with
// the absences squeezed out would read as an unbroken streak.
func recentCells(p stats.Player, results []store.BoardResult, currentPuzzle int) []scoreCell {
	byPuzzle := make(map[int]store.BoardResult)
	for _, res := range results {
		if res.PlayerID == p.ID {
			byPuzzle[res.PuzzleNo] = res
		}
	}

	first := currentPuzzle - recentResults + 1
	cells := make([]scoreCell, 0, recentResults)
	for puzzle := first; puzzle <= currentPuzzle; puzzle++ {
		cell := scoreCell{PuzzleNo: puzzle}
		if date, err := wordle.DateForPuzzle(puzzle); err == nil {
			cell.Date = date.Format(time.DateOnly)
		}

		res, played := byPuzzle[puzzle]
		if !played {
			cells = append(cells, cell)
			continue
		}

		cell.Played = true
		cell.Solved = res.Solved
		cell.HardMode = res.HardMode
		cell.PuzzleDate = puzzleDate(puzzle, cell.Date)
		if res.Solved {
			cell.Label = strconv.Itoa(res.Guesses)
			cell.Tone = res.Guesses
		} else {
			cell.Label = "X"
			cell.Tone = int(worstScore)
		}
		if res.HardMode {
			cell.Label += "*"
		}

		cells = append(cells, cell)
	}
	return cells
}

// percent rounds to the nearest whole percent, guarding the zero
// denominator so an empty roster cannot panic the page.
func percent(part, whole int) int {
	if whole == 0 {
		return 0
	}
	return int(float64(part)/float64(whole)*100 + 0.5)
}

// Calendar geometry: a week per column, a weekday per row, as the design
// draws it.
const (
	calendarCell = 13
	calendarGap  = 3
	weekdays     = 7
)

// calendarDay is one square in the heat grid.
type calendarDay struct {
	// Filled is false for the padding squares before the first day and
	// after the last, which keep the weeks aligned.
	Filled bool
	Played bool
	Tone   int
	Title  string
}

// buildCalendar lays a player's history out as weeks by weekdays.
//
// The grid starts on the Monday of the first week played, so every column is
// a whole week and the rows line up as weekdays throughout. Days before that
// Monday and after the last result are padding rather than absences.
func buildCalendar(results []store.BoardResult, playerID int64) []calendarDay {
	byDay := make(map[string]store.BoardResult)
	var first, last time.Time
	for _, r := range results {
		if r.PlayerID != playerID {
			continue
		}
		day := r.Date.Format(time.DateOnly)
		byDay[day] = r
		if first.IsZero() || r.Date.Before(first) {
			first = r.Date
		}
		if r.Date.After(last) {
			last = r.Date
		}
	}
	if first.IsZero() {
		return nil
	}

	// Monday is weekday 1 in Go's numbering, with Sunday at 0.
	offset := (int(first.Weekday()) + 6) % 7
	start := first.AddDate(0, 0, -offset)

	var days []calendarDay
	for d := start; !d.After(last); d = d.AddDate(0, 0, 1) {
		day := calendarDay{Filled: !d.Before(first)}
		if r, ok := byDay[d.Format(time.DateOnly)]; ok {
			day.Played = true
			day.Tone = int(worstScore)
			if r.Solved {
				day.Tone = r.Guesses
			}
			day.Title = d.Format(time.DateOnly)
		} else if day.Filled {
			day.Title = d.Format(time.DateOnly)
		}
		days = append(days, day)
	}
	return days
}

// monthRank is one point on the rank-by-month chart.
type monthRank struct {
	Label  string
	Rank   int
	Of     int
	X      string
	Y      string
	LabelY string
}

// rankChartHeight bounds the plot; ranks are drawn best at the top.
const (
	rankChartWidth  = 300.0
	rankChartHeight = 96.0
)

// buildMonthRanks finds a player's placing in each month they were ranked.
//
// Months where they did not reach the ten-game minimum are skipped rather
// than plotted as a gap: there was no rank to have, and drawing one would
// invent a placing.
func buildMonthRanks(months []stats.Month, playerID int64, t translator) ([]monthRank, string) {
	var points []monthRank
	for i := len(months) - 1; i >= 0; i-- {
		m := months[i]
		for _, p := range m.Ranked {
			if p.ID != playerID {
				continue
			}
			points = append(points, monthRank{
				Label: t.T("month." + strconv.Itoa(int(m.Month)))[:3],
				Rank:  p.Rank,
				Of:    len(m.Ranked),
			})
		}
	}
	if len(points) < 2 {
		return nil, ""
	}

	worst := 1
	for _, p := range points {
		if p.Of > worst {
			worst = p.Of
		}
	}

	var path strings.Builder
	for i := range points {
		x := 0.0
		if len(points) > 1 {
			x = float64(i) / float64(len(points)-1) * rankChartWidth
		}
		// Rank 1 sits at the top; the scale runs to the largest field size
		// any of these months had.
		y := float64(points[i].Rank-1) / float64(worst) * rankChartHeight
		points[i].X = strconv.FormatFloat(x, 'f', 1, 64)
		points[i].Y = strconv.FormatFloat(y, 'f', 1, 64)
		points[i].LabelY = strconv.FormatFloat(y-9, 'f', 1, 64)

		if i == 0 {
			path.WriteString("M")
		} else {
			path.WriteString(" L")
		}
		fmt.Fprintf(&path, "%.1f %.1f", x, y)
	}
	return points, path.String()
}

// traitOf is the localised label a player has earned, or "".
func traitOf(p stats.Player, board stats.Board, t translator) string {
	if key := stats.NewTraiter(board).For(p); key != "" {
		return t.T("trait." + key)
	}
	return ""
}

// traitWhy explains it, so a label is something a player can look into
// rather than a word that appeared next to their name.
func traitWhy(p stats.Player, board stats.Board, t translator) string {
	if key := stats.NewTraiter(board).For(p); key != "" {
		return t.T("trait." + key + ".why")
	}
	return ""
}
