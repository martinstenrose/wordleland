package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// monthRow is one player's month, pre-formatted.
type monthRow struct {
	Rank    int
	Name    string
	Trait   string
	Why     string
	Medal   string
	Href    string
	Average string
	// BarPercent scales the average against the worst on the board, so the
	// bars compare players within the month rather than against a fixed
	// scale that would leave them all nearly full.
	BarPercent    int
	Games         int
	ThreeOrBetter int
	Fails         int
	BestRun       int
	Winner        bool
}

// monthChip is one month in the selector.
type monthChip struct {
	Key string
	// Short is the abbreviated month, so a row of chips stays on one line.
	Short   string
	Note    string
	Href    string
	On      bool
	Winners string
	Average string
}

// monthStat is one of the figures beside the winner.
type monthStat struct {
	Label string
	Value string
}

type monthsPage struct {
	chrome

	Prefix    string
	BoardPath string
	Query     boardQuery

	Chips []monthChip

	Label        string
	Running      bool
	WinnerLabel  string
	WinnerStats  []monthStat
	WinnerNames  string
	WinnerLine   string
	GroupAverage string
	Days         int

	Rows []monthRow
	Thin []monthRow

	// MinGames is the ranking threshold, carried into the page so the rule
	// the kicker states cannot drift from the one the code applies.
	MinGames int

	// Range names the puzzles the history covers, which the design puts
	// opposite the heading. PartialNote says whether the chosen month is
	// complete.
	Range       string
	PartialNote string

	Season       []seasonRow
	SeasonMonths []string
	SeasonDays   int

	Empty bool
}

// seasonRow is one player's season, pre-formatted.
type seasonRow struct {
	Name    string
	Trait   string
	Why     string
	Href    string
	Wins    int
	Podiums int
	Best    string
	Marks   []seasonMark
}

type seasonMark struct {
	// Label is a star for a win, the finishing place otherwise, or a dot
	// where they were not ranked — which is not the same as finishing last.
	Label   string
	Won     bool
	Podium  bool
	Running bool
	Title   string
}

// handleMonths renders the month-by-month view.
func (s *Server) handleMonths(w http.ResponseWriter, r *http.Request, prefix, boardPath string, readOnly bool) {
	board, players, results, query, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	months := stats.ComputeMonths(players, results, stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           time.Now(),
	})

	ch := s.newChrome(w, r, prefix, viewMonths, readOnly)
	page := monthsPage{
		chrome: ch, Prefix: prefix, BoardPath: boardPath, Query: query,
		MinGames: stats.MinGames,
	}

	if len(months) == 0 {
		page.Empty = true
		if !readOnly && !s.issueChromeToken(w, r, &page.chrome) {
			return
		}
		s.render(w, r, http.StatusOK, "months.html", page)
		return
	}

	// The newest month is the default, which is the one people are actually
	// in. An explicit ?month= selects another.
	selected := 0
	wanted := r.URL.Query().Get("month")
	for i, m := range months {
		if monthKey(m) == wanted {
			selected = i
		}
	}
	now := time.Now()

	for i, m := range months {
		chip := monthChip{
			Key:   monthKey(m),
			Short: monthShort(ch.T, m),
			Href:  urlWith(r, "month", monthKey(m)),
			On:    i == selected,
			Note:  ch.T.TN("months.days", m.Days),
		}
		if !m.Complete(now) {
			chip.Note = ch.T.T("months.running")
		}
		if len(m.Winners) > 0 {
			chip.Winners = joinNames(m.Winners)
			chip.Average = formatScore(m.Winners[0].Average)
		} else {
			chip.Winners = "—"
		}
		page.Chips = append(page.Chips, chip)
	}

	// Traits are looked up for both the month table and the season block, so
	// they are gathered once before either is built.
	traits := stats.NewTraiter(board)
	figures := map[int64]stats.Player{}
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			figures[p.ID] = p
		}
	}

	m := months[selected]
	page.Label = monthLabel(ch.T, m)
	page.Running = !m.Complete(now)
	page.Range = ch.T.T("months.range", m.First, m.Last)
	page.PartialNote = ch.T.TN("months.fullMonth", m.Days)
	if page.Running {
		page.PartialNote = ch.T.TN("months.partialMonth", m.Days)
	}
	page.Days = m.Days
	page.GroupAverage = formatScore(m.GroupAverage)

	// A month still being played has a leader, not a winner. Calling it a
	// win would hand somebody a title they might yet lose.
	page.WinnerLabel = ch.T.T("months.winner")
	if page.Running {
		page.WinnerLabel = ch.T.T("months.leading")
	}

	if len(m.Winners) > 0 {
		w := m.Winners[0]
		page.WinnerNames = joinNames(m.Winners)

		// A month still being played is described in the present tense: it
		// has a leader, not a winner, and "took August" reads as settled
		// when there are days left in it.
		prefix := "months.line."
		if page.Running {
			prefix = "months.running."
		}

		switch {
		case len(m.Winners) > 1:
			page.WinnerLine = ch.T.T(prefix+"tie", formatScore(w.Average))
		case m.Margin != nil:
			page.WinnerLine = ch.T.T(prefix+"margin", page.WinnerNames, page.Label,
				strconv.FormatFloat(*m.Margin, 'f', 2, 64), w.Games)
		default:
			page.WinnerLine = ch.T.T(prefix+"alone", page.WinnerNames, w.Games)
		}

		// The four figures the design puts beside the name, in its order.
		page.WinnerStats = []monthStat{
			{Label: ch.T.T("board.column.average"), Value: formatScore(w.Average)},
			{Label: ch.T.T("board.column.games"), Value: strconv.Itoa(w.Games) + "/" + strconv.Itoa(m.Days)},
			{Label: ch.T.T("months.threeOrBetter"), Value: strconv.Itoa(w.ThreeOrBetter)},
			{Label: ch.T.T("months.bestRun"), Value: strconv.Itoa(w.BestRun)},
		}
	} else {
		page.WinnerLine = ch.T.T("months.line.nobody", 10)
	}

	for _, p := range m.Ranked {
		row := monthRowFor(p, prefix, m.Winners)
		// A month still being played has a leader rather than a winner, so
		// nobody is labelled one until it is over.
		switch {
		case row.Winner && page.Running:
			row.Medal = ch.T.T("months.medal.leading")
		case row.Winner && len(m.Winners) > 1:
			row.Medal = ch.T.T("months.medal.joint")
		case row.Winner:
			row.Medal = ch.T.T("months.medal.winner")
		case p.Rank == 2 && !page.Running:
			row.Medal = ch.T.T("months.medal.runnerUp")
		}
		if f, ok := figures[p.ID]; ok {
			if key := traits.For(f); key != "" {
				row.Trait = ch.T.T("trait." + key)
				row.Why = ch.T.T("trait." + key + ".why")
			}
		}
		page.Rows = append(page.Rows, row)
	}
	for _, p := range m.Thin {
		page.Thin = append(page.Thin, monthRowFor(p, prefix, nil))
	}

	season := stats.ComputeSeason(months, now)
	for i := len(months) - 1; i >= 0; i-- {
		page.SeasonMonths = append(page.SeasonMonths, shortMonthName(ch.T, months[i].Month))
	}
	for _, row := range season.Rows {
		view := seasonRow{
			Name: row.Name, Href: prefix + "/p/" + row.Slug,
			Wins: row.Wins, Podiums: row.Podiums, Best: "—",
		}
		if f, ok := figures[row.ID]; ok {
			if key := traits.For(f); key != "" {
				view.Trait = ch.T.T("trait." + key)
				view.Why = ch.T.T("trait." + key + ".why")
			}
		}
		if row.Best != nil {
			view.Best = strconv.FormatFloat(*row.Best, 'f', 2, 64) + " · " +
				shortMonthName(ch.T, row.BestMonth)
		}
		for _, mark := range row.Marks {
			cell := seasonMark{Label: "·", Won: mark.Won, Running: mark.Running}
			switch {
			case mark.Won && !mark.Running:
				// A star for a title, the placing for everything else.
				cell.Label = "★"
			case mark.Rank > 0:
				cell.Label = strconv.Itoa(mark.Rank)
				cell.Podium = mark.Rank <= 3
			}
			cell.Title = ch.T.T("month."+strconv.Itoa(int(mark.Month))) + " " + strconv.Itoa(mark.Year)
			view.Marks = append(view.Marks, cell)
		}
		page.Season = append(page.Season, view)
	}
	page.SeasonDays = board.Days

	if !readOnly {
		if !s.issueChromeToken(w, r, &page.chrome) {
			return
		}
	}
	s.render(w, r, http.StatusOK, "months.html", page)
}

// barFloor and barCeiling bound the bar's scale. Fixed rather than relative
// to the month, so the same average draws the same bar in every month —
// scaling to the field would make an ordinary score look poor in a good
// month and good in a poor one.
const (
	barFloor   = 3.0
	barCeiling = 5.2
)

func monthRowFor(p stats.MonthPlayer, prefix string, winners []stats.MonthPlayer) monthRow {
	row := monthRow{
		Rank: p.Rank, Name: p.Name, Href: prefix + "/p/" + p.Slug,
		Average: formatScore(p.Average), Games: p.Games,
		ThreeOrBetter: p.ThreeOrBetter, Fails: p.Fails, BestRun: p.BestRun,
	}
	if p.Average != nil {
		// Lower is better, so a good average gets a longer bar.
		pct := (barCeiling - *p.Average) / (barCeiling - barFloor) * 100
		switch {
		case pct < 4:
			pct = 4
		case pct > 100:
			pct = 100
		}
		row.BarPercent = int(pct + 0.5)
	}
	for _, wn := range winners {
		if wn.ID == p.ID {
			row.Winner = true
		}
	}
	return row
}

func monthKey(m stats.Month) string {
	return strconv.Itoa(m.Year) + "-" + strconv.Itoa(int(m.Month))
}

// monthLabel names the month in the reader's language. time.Month.String()
// is always English, so the name comes from the catalogue.
// shortMonthName abbreviates a month for the season marks and chips.
func shortMonthName(t translator, m time.Month) string {
	name := t.T("month." + strconv.Itoa(int(m)))
	if r := []rune(name); len(r) > 3 {
		return string(r[:3])
	}
	return name
}

// monthShort names a chip. The full month, not an abbreviation: the chips
// scroll rather than shrink, so there is room for the word.
func monthShort(t translator, m stats.Month) string {
	return monthLabel(t, m)
}

func monthLabel(t translator, m stats.Month) string {
	return t.T("month."+strconv.Itoa(int(m.Month))) + " " + strconv.Itoa(m.Year)
}

// joinNames renders a tie as every name, because a tie is the result.
func joinNames(ps []stats.MonthPlayer) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	out := ""
	for i, n := range names {
		switch {
		case i == 0:
			out = n
		case i == len(names)-1:
			out += " & " + n
		default:
			out += ", " + n
		}
	}
	return out
}
