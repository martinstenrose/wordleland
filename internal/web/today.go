package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
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

// todayEntryView is one filed result.
type todayEntryView struct {
	Name     string
	Href     string
	Label    string
	Tone     int
	HardMode bool
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
	Leaders []boardRow
	Rest    []boardRow

	// Benched is everyone the board does not rank, with the reason. Shown
	// on request rather than by default: the front page is about who is
	// playing, and the list would otherwise grow forever as people drift
	// away.
	Benched      []boardRow
	ShowBenched  bool
	BenchedHref  string
	BenchedCount int
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
			Name: e.Name, Href: prefix + "/p/" + e.Slug, HardMode: e.HardMode,
			Label: "X", Tone: 7,
		}
		if e.Solved {
			view.Label, view.Tone = strconv.Itoa(e.Guesses), e.Guesses
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

	// Ordered by form, not by the board's own ranking. The section is headed
	// "form, last 30 days" and prints the form figure, so ordering it by
	// all-time average put a 3.90 above a 3.59 and made the heading a lie.
	// The board's rank is still shown against each name, which is now a
	// second piece of information rather than a restatement of the order.
	var rows []boardRow
	for _, p := range board.Ranked {
		rows = append(rows, s.newBoardRow(p, prefix, ch.T, traits, results, board.CurrentPuzzle))
	}
	sortRows(rows, boardSort{Column: sortForm})

	for i, row := range rows {
		if i < 3 {
			page.Leaders = append(page.Leaders, row)
		} else {
			page.Rest = append(page.Rest, row)
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
		// One solve names its holder; more than one cannot, so the copy
		// differs rather than claiming "the only" when it was not.
		if c.Slug == "" {
			view.Key = "callout.oneAndDone.several"
			view.Args = []any{c.Count}
		} else {
			view.Args = []any{c.Name}
		}
	case stats.CalloutOnForm, stats.CalloutOffForm:
		view.Args = []any{c.Name, c.Value}
		view.Meta = t.T("callout.meta.window", stats.FormWindow)
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
