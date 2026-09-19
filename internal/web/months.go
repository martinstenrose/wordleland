package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// monthRow is one player's month, pre-formatted.
type monthRow struct {
	Rank  int
	Name  string
	Medal string
	// MedalTone is "gold", "silver" or "bronze" for the top three, empty
	// below them. A phone has no room for the chip beside a name, so it
	// writes the name in that colour instead — see medalTone.
	MedalTone string
	Href      string
	Average   string
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
	Progress     string
	WinnerLabel  string
	WinnerStats  []monthStat
	WinnerNames  string
	WinnerLine   string
	GroupAverage string
	Days         int

	Rows []monthRow
	Thin []monthRow

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
	page := monthsPage{chrome: ch, Prefix: prefix, BoardPath: boardPath, Query: query}

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
			chip.Winners = joinNames(ch.T, m.Winners)
			chip.Average = formatScore(ch.T, m.Winners[0].Average)
		} else {
			chip.Winners = "—"
		}
		page.Chips = append(page.Chips, chip)
	}

	m := months[selected]
	page.Label = monthLabel(ch.T, m)
	page.Running = !m.Complete(now)
	if m.Year == now.Year() && m.Month == now.Month() {
		daysInMonth := time.Date(m.Year, m.Month+1, 0, 0, 0, 0, 0, now.Location()).Day()
		page.Progress = ch.T.T("months.progress", now.Day(), daysInMonth)
	}
	page.Range = ch.T.T("months.range", ch.T.Puzzle(m.First), ch.T.Puzzle(m.Last))
	page.PartialNote = ch.T.TN("months.fullMonth", m.Days)
	if page.Running {
		page.PartialNote = ch.T.TN("months.partialMonth", m.Days)
	}
	page.Days = m.Days
	page.GroupAverage = formatScore(ch.T, m.GroupAverage)

	// A month still being played has a leader, not a winner. Calling it a
	// win would hand somebody a title they might yet lose.
	page.WinnerLabel = ch.T.T("months.winner")
	if page.Running {
		page.WinnerLabel = ch.T.T("months.leading")
	}

	if len(m.Winners) > 0 {
		w := m.Winners[0]
		page.WinnerNames = joinNames(ch.T, m.Winners)

		// A month still being played is described in the present tense: it
		// has a leader, not a winner, and "took August" reads as settled
		// when there are days left in it.
		prefix := "months.line."
		if page.Running {
			prefix = "months.running."
		}

		switch {
		case len(m.Winners) > 1:
			page.WinnerLine = ch.T.T(prefix+"tie", formatScore(ch.T, w.Average))
		case m.Margin != nil:
			page.WinnerLine = ch.T.T(prefix+"margin", page.WinnerNames, page.Label,
				ch.T.Decimal(*m.Margin, 2), m.Days)
		default:
			page.WinnerLine = ch.T.T(prefix+"alone", page.WinnerNames, m.Days)
		}

		// The four figures the design puts beside the name, in its order.
		page.WinnerStats = []monthStat{
			{Label: ch.T.T("board.column.average"), Value: formatScore(ch.T, w.Average)},
			{Label: ch.T.T("board.column.games"), Value: ch.T.Integer(w.Games) + "/" + ch.T.Integer(m.Days)},
			{Label: ch.T.T("months.threeOrBetter"), Value: ch.T.Integer(w.ThreeOrBetter)},
			{Label: ch.T.T("months.bestRun"), Value: ch.T.Integer(w.BestRun)},
		}
	} else {
		page.WinnerLine = ch.T.T("months.line.nobody")
	}

	for _, p := range m.Ranked {
		row := monthRowFor(p, prefix, m.Winners, ch.T)
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
		row.MedalTone = medalTone(p.Rank)
		page.Rows = append(page.Rows, row)
	}
	for _, p := range m.Thin {
		page.Thin = append(page.Thin, monthRowFor(p, prefix, nil, ch.T))
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
		if row.Best != nil {
			view.Best = ch.T.Decimal(*row.Best, 2) + " · " +
				shortMonthName(ch.T, row.BestMonth)
		}
		for _, mark := range row.Marks {
			cell := seasonMark{Label: "·", Won: mark.Won, Running: mark.Running}
			switch {
			case mark.Won && !mark.Running:
				// A star for a title, the placing for everything else.
				cell.Label = "★"
			case mark.Rank > 0:
				cell.Label = ch.T.Integer(mark.Rank)
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

// medalTone is the colour a phone writes a top-three name in, in place of the
// chip it has no room for.
//
// It reads the rank rather than the medal above, which is what gives a shared
// win the shape a podium has: two golds, and then a bronze. Competition
// ranking already numbers a tie 1, 1, 3 (see stats/months.go), so there is no
// second place for a silver to go to — and inventing one would name a
// runner-up the data does not.
func medalTone(rank int) string {
	switch rank {
	case 1:
		return "gold"
	case 2:
		return "silver"
	case 3:
		return "bronze"
	}
	return ""
}

func monthRowFor(p stats.MonthPlayer, prefix string, winners []stats.MonthPlayer, t translator) monthRow {
	row := monthRow{
		Rank: p.Rank, Name: p.Name, Href: prefix + "/p/" + p.Slug,
		Average: formatScore(t, p.Average), Games: p.Games,
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

// monthShort names a chip: the abbreviated month, and no year. The chips are
// a row of small boxes and the one chosen is named in full directly beneath
// them, so a chip wide enough for "September 2026" spends the row's width on
// what the next line already says.
func monthShort(t translator, m stats.Month) string {
	return t.T("month.short." + strconv.Itoa(int(m.Month)))
}

func monthLabel(t translator, m stats.Month) string {
	return t.T("month."+strconv.Itoa(int(m.Month))) + " " + strconv.Itoa(m.Year)
}

// longDate names a full date in the reader's language, the way today's
// headline does: "Monday 2 January 2006" in English, "måndag 2 januari
// 2006" in Swedish. Neither a weekday's name nor a month's is locale-aware
// on its own — time.Weekday.String() and time.Month.String() are always
// English — so both come from the catalogue instead.
func longDate(t translator, date time.Time) string {
	weekday := t.T("weekday." + strconv.Itoa(int(date.Weekday())))
	month := t.T("month." + strconv.Itoa(int(date.Month())))
	return weekday + " " + strconv.Itoa(date.Day()) + " " + month + " " + strconv.Itoa(date.Year())
}

// joinNames renders a tie as every name, because a tie is the result.
func joinNames(t translator, ps []stats.MonthPlayer) string {
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
			out += " " + t.T("list.and") + " " + n
		default:
			out += ", " + n
		}
	}
	return out
}
