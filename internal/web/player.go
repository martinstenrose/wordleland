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

	// How far the scale is held off the top and bottom edges. This chart
	// rules every score, and without the inset the rules for 1 and 7 sit
	// flush against the box and read as its border rather than as the scale.
	chartInset = 6

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
	// Popup is what the day opens: see popupFor. Filled in on the player's
	// page only, where the strip's days open one.
	Popup dayPopup
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

	// Charts. FormPath and GroupPath share one coordinate space so the two
	// lines can be read against each other.
	FormPath  template.HTML
	GroupPath template.HTML
	HasChart  bool
	Gridlines []chartGridline

	// FormXLabels marks how far back the chart reaches. Without them the
	// horizontal axis says nothing at all: the shape is legible, the span
	// it covers is a guess.
	FormXLabels []string

	Distribution []distributionBar
	Recent       []scoreCell

	// Stats are the four figures beside the name, as the design places
	// them: the same shape the month view uses for its winner.
	Stats []playerStat

	// Heat is the last year, a week per column; HeatLabels names the month
	// each column starts, where one does.
	Heat       []calendarDay
	HeatLabels []heatLabel

	Records     []recordRow
	Weekdays    []weekdayBar
	WeekdayNote string

	MonthRanks     []monthRank
	RankRules      []rankRule
	WinsNote       string
	RankPath       template.HTML
	RankPathDashed template.HTML

	// Figures pre-formatted the same way the board formats them.
	AverageText   string
	FormText      string
	DeltaText     string
	DeltaClass    string
	HardModeShare string
	ReasonKey     string

	// ChartNoteKey explains why the charts are absent, when they are. It is
	// empty for a player whose charts render.
	ChartNoteKey string

	Trait string
	Why   string

	// Badges is every badge there is, the player's earned ones marked;
	// Openings how many of the possible first rows they have opened with.
	// BadgesNote stands in for both while the player has no grid.
	Badges     []badgeRow
	Openings   string
	BadgesNote string
}

// playerStat is one headline figure.
type playerStat struct {
	Label string
	Value string
}

// chartGridline is one horizontal rule, the score it marks, and where that
// label sits as a share of the chart's height — the stylesheet places the
// label from Top so that it lands on the rule rather than near it.
type chartGridline struct {
	Y     string
	Top   string
	Label string
}

// handlePlayer renders one player's detail page, authenticated or shared.
//
// It is served under both prefixes for the same reason the board is: the
// share link is what people are sent, and a board nobody can click into is
// half the feature.
// handlePlayers renders the players view with nobody named, which means the
// player at the top of the board. It exists so the nav has somewhere to
// point: the detail panel is the view, and the picker swaps who is in it.
func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request, prefix string, readOnly bool) {
	board, _, results, _, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	if len(board.Ranked) == 0 && len(board.Unranked) == 0 {
		// Nobody to open on, so the view says so where it stands. It used to
		// redirect to the view's own address, which is itself, until the
		// browser gave up.
		ch := s.newChrome(w, r, prefix, viewPlayers, readOnly)
		ch.Section = ch.playerSwitcher(board, results, prefix, nil)
		s.render(w, r, http.StatusOK, "players.html", ch)
		return
	}

	// Open on whoever leads the board rather than on an empty page asking
	// which player to show. That question had one answer nearly every time,
	// and it cost a tap to give it; the roster is one press away in the bar
	// either way, and the bar now shows which player is up rather than
	// looking like a control that has not been used yet.
	first := board.Ranked
	if len(first) == 0 {
		first = board.Unranked
	}
	http.Redirect(w, r, prefix+"/players/"+first[0].Slug, http.StatusSeeOther)
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

		AverageText: formatScore(t, player.Average),
		FormText:    formatScore(t, player.Form),
		ReasonKey:   reasonKey(player.Reason),

		FormPath:    template.HTML(sparkPath(player.Series, chartWidth, chartHeight, chartInset)),
		GroupPath:   template.HTML(sparkPath(board.GroupSeries, chartWidth, chartHeight, chartInset)),
		HasChart:    hasSparkline(player.Series),
		Gridlines:   chartGridlines(ch.T),
		FormXLabels: formXLabels(ch.T),

		Distribution: distributionBars(player, t),
		Recent:       recentCells(player, results, board.CurrentPuzzle, t),
	}
	days := newDayTable(results)
	byPuzzle := map[int]store.BoardResult{}
	for _, res := range results {
		if res.PlayerID == player.ID {
			byPuzzle[res.PuzzleNo] = res
		}
	}
	for i, c := range page.Recent {
		if res, ok := byPuzzle[c.PuzzleNo]; ok {
			page.Recent[i].Popup = popupFor(t, days, res, prefix)
		}
	}
	page.Badges = playerBadges(t, results, player.ID, prefix)
	if n := stats.Openings(results, player.ID); n > 0 {
		page.Openings = t.T("player.badges.openings", n, stats.PossibleOpenings)
	} else {
		page.BadgesNote = t.T("player.badges.noGrids")
	}
	page.Heat, page.HeatLabels = buildHeatmap(t, days, results, player.ID, board.CurrentPuzzle, prefix)
	// Averages by weekday are averages, withheld below the ranking threshold
	// like every other one on the page.
	if player.Ranked() {
		page.Weekdays, page.WeekdayNote = playerWeekdays(t, player, results, board.CurrentPuzzle)
	}

	// Ranked against the whole roster, not against themselves: a month
	// computed over one player would place them first every time.
	now := time.Now()
	months := stats.ComputeMonths(players, results, stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           now,
	})
	page.Records = playerRecords(t, player, board, months, results, now)
	ranks, rules, path, dashedPath, won := buildMonthRanks(months, player.ID, now, t)
	page.MonthRanks = ranks
	page.RankRules = rules
	page.WinsNote = t.T("player.noWins")
	if won > 0 {
		page.WinsNote = t.TN("player.monthsWon", won)
	}
	page.RankPath = template.HTML(path)
	page.RankPathDashed = template.HTML(dashedPath)

	page.Section = ch.playerSwitcher(board, results, prefix, &player)

	page.DeltaText, page.DeltaClass = formatDelta(t, player.Delta)

	rank := "—"
	if player.Ranked() {
		rank = "#" + t.Integer(player.Rank)
	}
	// Three, as the design has it: the streak moved to Records and rivals,
	// where it stands beside the longest and the solving kind.
	page.Stats = []playerStat{
		{Label: t.T("board.column.form"), Value: formatScore(t, player.Form)},
		{Label: t.T("board.column.average"), Value: formatScore(t, player.Average)},
		{Label: t.T("player.stat.rank"), Value: rank},
	}

	// The derived figures are withheld below the ranking threshold, on this
	// page for the same reason as on the board.
	if !player.Ranked() {
		page.AverageText, page.FormText = "—", "—"
		page.DeltaText, page.DeltaClass = "", "level"
		page.Stats[0].Value, page.Stats[1].Value = "—", "—"
	}

	// The figures sit in the head, beside the name, for a player with any
	// games to have figures of.
	if player.Games > 0 {
		page.Section.Stats = page.Stats
	}
	// The trait stands beside the name, as a chip.
	page.Section.Trait, page.Section.Why = page.Trait, page.Why

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

// formXLabels are the three marks under the form chart: the far end of the
// window, its midpoint, and today. The series is one point per puzzle and one
// puzzle per day, so a count of puzzles is a count of days.
func formXLabels(t translator) []string {
	return []string{
		t.T("player.formChart.ago", stats.FormWindow),
		t.T("player.formChart.ago", stats.FormWindow/2),
		t.T("player.formChart.now"),
	}
}

// chartGridlines marks every score on the form chart, from a 1 down to a
// miss. Each carries the offset of its own rule rather than being spread
// evenly by the stylesheet, so a label cannot drift off the line it names.
func chartGridlines(t translator) []chartGridline {
	var lines []chartGridline
	for score := int(bestScore); score <= int(worstScore); score++ {
		y := scoreY(float64(score), chartHeight, chartInset)
		label := strconv.Itoa(score)
		if float64(score) == worstScore {
			// The bottom rule is not a seventh guess; it is the other
			// outcome, and the strip and the ramp both call it X.
			label = t.T("player.failed")
		}
		lines = append(lines, chartGridline{
			Y:     strconv.FormatFloat(y, 'f', 1, 64),
			Top:   strconv.FormatFloat(y/chartHeight*100, 'f', 2, 64) + "%",
			Label: label,
		})
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
func recentCells(p stats.Player, results []store.BoardResult, currentPuzzle int, t translator) []scoreCell {
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
		cell.PuzzleDate = puzzleDate(t, puzzle, cell.Date)
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

// calendarDay is one square in the heat grid.
type calendarDay struct {
	// Filled is false for the padding squares before the first day and
	// after the last, which keep the weeks aligned.
	Filled bool
	Played bool
	Tone   int
	// Title is the hover text for a day with nothing to open: the padding
	// squares have none, and a day not played has only its date to give.
	Title string
	// Detail is the popup body for a day that was played — which puzzle,
	// when, and what it took. The square itself is blank, so unlike the
	// recent strip's cells this popup carries the result as well.
	Detail string
	// Popup is what a played day opens.
	Popup dayPopup
}

// monthRank is one point on the rank-by-month chart.
type monthRank struct {
	Label string
	Rank  int
	Of    int
	// X and Y place the point in the plot's units, for the line; Left and
	// Top in shares of its box, for the dot and the month's name.
	X, Y      string
	Left, Top string
}

// buildMonthRanks finds a player's placing in each of the last months they
// were ranked in, laid out as the design draws it: a rule per place down the
// left, best at the top, a dot per month and the month's name under it.
//
// Months with no score under the selected rules are skipped rather than
// plotted as a gap: there was no rank to have, and drawing one would invent
// a placing.
//
// Positions are shares of the plot's box, so the dots and labels, which are
// markup, sit on the line, which is a stretched SVG. path covers every
// segment up to the most recent completed month; dashedPath, when non-empty,
// is the one trailing segment into a month still being played, whose rank
// can still move.
func buildMonthRanks(months []stats.Month, playerID int64, now time.Time, t translator) (points []monthRank, rules []rankRule, path, dashedPath string, won int) {
	lastComplete := true
	for i := len(months) - 1; i >= 0; i-- {
		m := months[i]
		for _, p := range m.Ranked {
			if p.ID != playerID {
				continue
			}
			points = append(points, monthRank{
				Label: shortMonthName(t, m.Month),
				Rank:  p.Rank,
				Of:    len(m.Ranked),
			})
			lastComplete = m.Complete(now)
			if p.Rank == 1 && lastComplete {
				won++
			}
		}
	}
	if len(points) > rankMonths {
		points = points[len(points)-rankMonths:]
	}
	if len(points) < 2 {
		return nil, nil, "", "", won
	}

	worst := 2
	for _, p := range points {
		worst = max(worst, p.Of)
	}
	// In the plot's own units, the design's: 320 wide, 160 tall, the rules
	// from 8 to 128 and the dots from 36 across.
	yOf := func(rank int) float64 { return 8 + float64(rank-1)*120/float64(worst-1) }
	for r := 1; r <= worst; r++ {
		rules = append(rules, rankRule{Label: t.Integer(r), Y: fmtF(yOf(r)), Top: fmtPct(yOf(r) / 160)})
	}
	for i := range points {
		x := 36 + float64(i)*276/float64(len(points)-1)
		y := yOf(points[i].Rank)
		points[i].X, points[i].Y = fmtF(x), fmtF(y)
		points[i].Left, points[i].Top = fmtPct(x/320), fmtPct(y/160)
	}

	splitAt := len(points) - 1
	if !lastComplete {
		splitAt = len(points) - 2
	}
	path = rankPath(points[:splitAt+1])
	if splitAt < len(points)-1 {
		dashedPath = rankPath(points[splitAt:])
	}
	return points, rules, path, dashedPath, won
}

// rankMonths is how many months the chart shows at most.
const rankMonths = 9

// rankRule is one place's rule on the rank chart.
type rankRule struct {
	Label string
	Y     string
	Top   string
}

func fmtF(v float64) string   { return strconv.FormatFloat(v, 'f', 1, 64) }
func fmtPct(v float64) string { return strconv.FormatFloat(v*100, 'f', 2, 64) + "%" }

// rankPath renders a sequence of already-positioned points as a single SVG
// path: one straight segment per consecutive pair.
func rankPath(points []monthRank) string {
	var b strings.Builder
	for i, p := range points {
		if i == 0 {
			b.WriteString("M")
		} else {
			b.WriteString(" L")
		}
		fmt.Fprintf(&b, "%s %s", p.X, p.Y)
	}
	return b.String()
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
