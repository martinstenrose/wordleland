package web

import (
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// heroTiles is how many cells the day's result is drawn as: a Wordle row.
const heroTiles = 6

// calloutView is one generated observation, ready to render.
type calloutView struct {
	Kind string
	// Key and Args are the localised template and its values: the
	// stats package produces figures, never sentences.
	Key  string
	Args []any
	Meta string
	Href string
}

// todayEntryView is one filed result. A trailing * on Label marks hard
// mode, matching the convention used by the player's recent-games strip.
type todayEntryView struct {
	Name  string
	Href  string
	Label string
	Tone  int
}

type todayPage struct {
	chrome

	Prefix    string
	BoardPath string
	Query     boardQuery

	PuzzleNo int
	DateLong string

	Filed   []todayEntryView
	Missing []string

	// Hero is the day's best result drawn as a row of tiles, nil when
	// nobody has solved it yet.
	Hero []scoreCell

	HeadlineKey  string
	HeadlineArgs []any
	FiledCount   int
	Expected     int

	Callouts []calloutView

	// Leaders is the top of the board, and Rest the remainder, so the front
	// page can give the first three the space the design gives them.
	Leaders []todayFormRow
	Rest    []todayFormRow

	// Benched is everyone the board does not rank, with the reason. Shown
	// on request rather than by default: the front page is about who is
	// playing, and the list would otherwise grow forever as people drift
	// away.
	Benched      []boardRow
	ShowBenched  bool
	BenchedHref  string
	BenchedCount int
}

// todayFormRow distinguishes position by form from the all-time board rank.
type todayFormRow struct {
	boardRow
	FormRank int
}

// handleToday renders the front page: the current puzzle, the generated
// callouts, and the form table beneath them.
func (s *Server) handleToday(w http.ResponseWriter, r *http.Request, prefix, boardPath string, readOnly bool) {
	board, players, results, query, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	now := time.Now()
	today := stats.ComputeToday(players, results, board.CurrentPuzzle)

	ch := s.newChrome(w, r, prefix, viewToday, readOnly)
	page := todayPage{
		chrome:     ch,
		Prefix:     prefix,
		BoardPath:  boardPath,
		Query:      query,
		PuzzleNo:   today.PuzzleNo,
		FiledCount: today.FiledCount(),
		Expected:   today.Expected(),
	}
	if date, err := wordle.DateForPuzzle(today.PuzzleNo); err == nil {
		page.DateLong = longDate(ch.T, date)
	}

	for _, e := range today.Filed {
		view := todayEntryView{
			Name: e.Name, Href: prefix + "/p/" + e.Slug,
			Label: "X", Tone: 7,
		}
		if e.Solved {
			view.Label, view.Tone = strconv.Itoa(e.Guesses), e.Guesses
		}
		if e.HardMode {
			view.Label += "*"
		}
		page.Filed = append(page.Filed, view)
	}
	for _, p := range today.Missing {
		page.Missing = append(page.Missing, p.Name)
	}

	switch {
	case today.Best != nil && today.BestShared > 1:
		// "2 of 8", not "2 of them": the readers are the players, and the
		// count they want is how many filed today.
		page.HeadlineKey = "today.headline.shared"
		page.HeadlineArgs = []any{today.BestShared, today.FiledCount(), today.Best.Guesses}
		page.Hero = heroRow(today.Best.Guesses)
	case today.Best != nil:
		page.HeadlineKey = "today.headline.best"
		page.HeadlineArgs = []any{today.Best.Name, today.Best.Guesses}
		page.Hero = heroRow(today.Best.Guesses)
	case page.FiledCount > 0:
		// Everyone who has filed today failed it, which is a result in its
		// own right rather than an absence of one.
		page.HeadlineKey = "today.headline.noneSolved"
	default:
		page.HeadlineKey = "today.headline.empty"
	}

	for _, c := range stats.ComputeCallouts(board, results, now) {
		page.Callouts = append(page.Callouts, s.calloutFor(c, prefix, ch.T))
	}

	traits := stats.NewTraiter(board)

	// Sort 30-day form while keeping the board rank beside
	// each name. The toggle changes this table, not eligibility or banter.
	byPlayer := make(map[int64][]store.BoardResult)
	for _, result := range results {
		byPlayer[result.PlayerID] = append(byPlayer[result.PlayerID], result)
	}
	var rows []boardRow
	for _, p := range board.Ranked {
		row := s.newBoardRow(p, prefix, ch.T, traits, results, board.CurrentPuzzle)
		form := stats.ComputeTodayForm(byPlayer[p.ID], board.Options)
		row.Form, row.FormGames, row.Series = form.Average, form.Games, form.Series
		row.Delta = nil
		if baseline := stats.TodayBaseline(byPlayer[p.ID], board.Options); row.Form != nil && baseline != nil {
			delta := *row.Form - *baseline
			row.Delta = &delta
		}
		row.FormText = formatScore(ch.T, row.Form)
		row.DeltaText, row.DeltaDirection = formatDelta(ch.T, row.Delta)
		row.SparkPath = template.HTML(sparkPath(row.Series, sparkWidth, sparkHeight))
		row.HasSpark = hasSparkline(row.Series)
		rows = append(rows, row)
	}
	sortRows(rows, boardSort{Column: sortForm})

	formRank := 0
	for i, row := range rows {
		view := todayFormRow{boardRow: row}
		if row.Form != nil {
			// Equal form scores share a rank; missing form is unranked.
			if i == 0 || rows[i-1].Form == nil || *row.Form != *rows[i-1].Form {
				formRank = i + 1
			}
			view.FormRank = formRank
		}
		if i < 3 {
			page.Leaders = append(page.Leaders, view)
		} else {
			page.Rest = append(page.Rest, view)
		}
	}

	page.BenchedCount = len(board.Unranked)
	page.ShowBenched = r.URL.Query().Get("benched") == "1"
	if page.ShowBenched {
		page.BenchedHref = urlWith(r, "benched", "0")
		for _, p := range board.Unranked {
			page.Benched = append(page.Benched, s.newBoardRow(p, prefix, ch.T, traits, results, board.CurrentPuzzle))
		}
	} else {
		page.BenchedHref = urlWith(r, "benched", "1")
	}

	if !readOnly {
		if !s.issueChromeToken(w, r, &page.chrome) {
			return
		}
	}
	s.render(w, r, http.StatusOK, "today.html", page)
}

// calloutFor turns a computed observation into a localised line.
func (s *Server) calloutFor(c stats.Callout, prefix string, t translator) calloutView {
	view := calloutView{Kind: c.Kind, Key: "callout." + c.Kind}
	if c.Slug != "" {
		view.Href = prefix + "/p/" + c.Slug
	}

	switch c.Kind {
	case stats.CalloutUnbroken:
		view.Args = []any{c.Name, c.Count}
		view.Meta = t.T("callout.meta.streak", c.Count)
	case stats.CalloutOneAndDone:
		// A single holder can be named, even with several first-guess solves.
		// Otherwise the headline reports the group count.
		if c.Slug == "" {
			view.Key = "callout.oneAndDone.several"
			view.Args = []any{c.Count}
		} else {
			view.Args = []any{c.Name}
		}
		if date, err := wordle.DateForPuzzle(c.PuzzleNo); err == nil {
			key := "callout.meta.puzzle"
			if c.Count > 1 {
				key = "callout.meta.latestPuzzle"
			}
			view.Meta = t.T(key, c.PuzzleNo, date.Format(time.DateOnly))
		}
	case stats.CalloutOnForm, stats.CalloutOffForm:
		view.Args = []any{c.Name, c.Value}
		view.Meta = t.T("callout.meta.window", stats.FormWindow)
	case stats.CalloutQuickSolves, stats.CalloutCloseShaves, stats.CalloutStumped, stats.CalloutHardMode:
		view.Key += ".other"
		if c.Count == 1 {
			view.Key = "callout." + c.Kind + ".one"
		}
		view.Args = []any{c.Count}
		view.Meta = t.T("callout.meta.recent", stats.FormWindow)
	case stats.CalloutMissing:
		view.Args = []any{c.Name, c.Count}
		view.Meta = t.T("callout.meta.lastPlayed", c.Since.Format(time.DateOnly))
	}
	return view
}

// heroRow draws a solved result as a Wordle row: the guesses used, filled.
func heroRow(guesses int) []scoreCell {
	row := make([]scoreCell, 0, heroTiles)
	for i := 1; i <= heroTiles; i++ {
		cell := scoreCell{}
		if i <= guesses {
			cell.Played, cell.Solved, cell.Tone = true, true, guesses
		}
		row = append(row, cell)
	}
	return row
}
