package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

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

// todayResultRow is one filed result, as the day's own table shows it.
//
// The day used to be a wrapping strip of name-and-score pairs, which said who
// had played and nothing else: not who was ahead, and not whether a 4 was a
// good day for that person or a bad one. Both questions are answered by
// figures the page already had.
type todayResultRow struct {
	// Pos is the standing so far, or an em dash for a player the board does
	// not rank — they are in the day like everybody else, but a position
	// among people who are ranked is not a thing they hold.
	Pos  string
	Name string
	Href string
	// Label is the guess count or "X", with a trailing * for hard mode,
	// matching the convention the player's recent-games strip uses.
	Label string
	Tone  int

	// AvgText is the all-time average the delta is measured against — or,
	// for a player the board does not rank, why there is none.
	AvgText string
	// DeltaText is today's score against this player's own average: the
	// figure that turns a 4 into a good or a bad day for them. Direction is
	// the shared "better"/"worse"/"level" vocabulary.
	DeltaText      string
	DeltaDirection string
}

type todayPage struct {
	chrome

	Prefix    string
	BoardPath string
	Query     boardQuery

	PuzzleNo int
	DateLong string

	Results []todayResultRow
	Missing []string

	HeadlineKey  string
	HeadlineArgs []any
	FiledCount   int
	Expected     int
	// FiledPercent fills the track beside the count. It is how far through
	// the day the group is, which is the one thing a glance at the top of
	// this page should answer.
	FiledPercent int

	Callouts []calloutView

	Form []todayFormRow

	// Benched is everyone the board does not rank, with the reason. Behind a
	// disclosure rather than on the page: the front page is about who is
	// playing, and the list grows forever as people drift away.
	Benched      []boardRow
	BenchedCount int
}

// todayFormRow distinguishes position by form from the all-time board rank.
type todayFormRow struct {
	boardRow
	FormRank int
	// FormRankText is FormRank as the table prints it, an em dash when
	// there is no form score, so the cell and its popup cannot disagree
	// about what an unranked player shows.
	FormRankText string
	// RankDetail is what the rank's popup spells out — see rankDetail.
	RankDetail []playerStat
}

// rankDetail names the two numbers in a rank cell's "1 (5)": the figure
// that explains the number in parentheses.
func rankDetail(t translator, row todayFormRow) []playerStat {
	return []playerStat{
		{Label: t.T("today.formRank"), Value: row.FormRankText},
		{Label: t.T("today.boardRank"), Value: t.Integer(row.Rank)},
	}
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

	if page.Expected > 0 {
		page.FiledPercent = percent(page.FiledCount, page.Expected)
	}
	page.Results = todayResults(ch.T, today, board, prefix)
	for _, p := range today.Missing {
		page.Missing = append(page.Missing, p.Name)
	}

	switch {
	case today.Best != nil && today.BestShared > 1:
		// "2 of 8", not "2 of them": the readers are the players, and the
		// count they want is how many filed today.
		page.HeadlineKey = "today.headline.shared"
		page.HeadlineArgs = []any{today.BestShared, today.FiledCount(), today.Best.Guesses}
	case today.Best != nil:
		page.HeadlineKey = "today.headline.best"
		page.HeadlineArgs = []any{today.Best.Name, today.Best.Guesses}
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
		row.Form, row.FormGames = form.Average, form.Games
		row.Delta = nil
		if baseline := stats.TodayBaseline(byPlayer[p.ID], board.Options); row.Form != nil && baseline != nil {
			delta := *row.Form - *baseline
			row.Delta = &delta
		}
		row.FormText = formatScore(ch.T, row.Form)
		row.DeltaText, row.DeltaDirection = formatDelta(ch.T, row.Delta)
		rows = append(rows, row)
	}
	sortRows(rows, boardSort{Column: sortForm})

	formRank := 0
	for i, row := range rows {
		view := todayFormRow{boardRow: row, FormRankText: "\u2014"}
		if row.Form != nil {
			// Equal form scores share a rank; missing form is unranked.
			if i == 0 || rows[i-1].Form == nil || *row.Form != *rows[i-1].Form {
				formRank = i + 1
			}
			view.FormRank = formRank
			view.FormRankText = ch.T.Integer(formRank)
		}
		view.RankDetail = rankDetail(ch.T, view)
		page.Form = append(page.Form, view)
	}

	page.BenchedCount = len(board.Unranked)
	for _, p := range board.Unranked {
		page.Benched = append(page.Benched, s.newBoardRow(p, prefix, ch.T, traits, results, board.CurrentPuzzle))
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
		view.Href = prefix + "/players/" + c.Slug
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
			view.Meta = t.T(key, t.Puzzle(c.PuzzleNo), date.Format(time.DateOnly))
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

// todayResults turns the day into the rows the page lists, best first.
//
// The order is stats.ComputeToday's — best score first, ties broken by name
// so the list is stable through the day rather than reshuffling as results
// arrive. Positions are counted over the ranked players only: an unranked
// player is in the day like everybody else, but a position among people who
// are ranked is not a thing they hold, and numbering them would push
// everyone below them down a place for the wrong reason.
func todayResults(t translator, today stats.Today, board stats.Board, prefix string) []todayResultRow {
	// Ranked is the board's own decision, not a property of the average: a
	// player below the threshold has an average and it is withheld, here as
	// everywhere else.
	ranked := make(map[int64]stats.Player, len(board.Ranked))
	games := make(map[int64]int, len(board.Ranked)+len(board.Unranked))
	for _, p := range board.Ranked {
		ranked[p.ID] = p
	}
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			games[p.ID] = p.Games
		}
	}

	out := make([]todayResultRow, 0, len(today.Filed))
	pos := 0
	for _, e := range today.Filed {
		row := todayResultRow{
			Name:  e.Name,
			Href:  prefix + "/players/" + e.Slug,
			Label: "X",
			Tone:  7,
		}
		if e.Solved {
			row.Label, row.Tone = strconv.Itoa(e.Guesses), e.Guesses
		}
		if e.HardMode {
			row.Label += "*"
		}

		p, isRanked := ranked[e.ID]
		if !isRanked {
			row.Pos, row.DeltaDirection = "\u2014", "level"
			row.AvgText = t.TN("today.benchedGames", games[e.ID])
			out = append(out, row)
			continue
		}

		pos++
		row.Pos = t.Integer(pos)
		row.AvgText = formatScore(t, p.Average)
		switch {
		case !e.Solved:
			// A miss has no distance from an average: it is off the scale
			// the average is measured on, and "▲ 2.61" would invent one.
			row.DeltaText, row.DeltaDirection = t.T("today.missed"), "worse"
		case p.Average != nil:
			delta := float64(e.Guesses) - *p.Average
			row.DeltaText, row.DeltaDirection = formatDelta(t, &delta)
		}
		out = append(out, row)
	}
	return out
}
