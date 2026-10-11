package web

import (
	"html/template"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// monthsInPlace has a control on the months page redraw the page where it
// is, as the board's do (boardInPlace). Nearly all of the page is the month
// chosen — the year in the head, the month's card, its table, the season
// with that month outlined — so the view is all of it, and nothing else
// needs taking by id. No cross-fade and no scroll, and the address goes
// in the bar.
const monthsInPlace template.HTMLAttr = `hx-target="#months-view" hx-select="#months-view"` +
	` hx-swap="outerHTML show:none transition:false"` +
	` hx-push-url="true"`

// monthRow is one player's month, pre-formatted.
type monthRow struct {
	Rank    int
	Name    string
	Href    string
	Average string
	// Behind is the gap to the front, "0.12", or an em dash for whoever is
	// at it. BehindPhone says the same in words for a phone's row, which has
	// no column heading to say what the figure is: "0.12 behind", or
	// "leader" ("winner" once the month is over) for whoever is at the front.
	Behind      string
	BehindPhone string
	// Vs is how the player's average moved from their own month before:
	// a trend, as the group's has, or "new" for a player who was not
	// ranked in it. Empty on the board's oldest month, which has none.
	Vs trend
	// Played is the puzzles played against the days the month has had.
	Played string
	Games  int
	Fails  int
	Winner bool
}

// monthChip is one month in the selector.
type monthChip struct {
	Key string
	// Short is the abbreviated month, so a row of chips stays on one line.
	Short   string
	Href    string
	On      bool
	Running bool
	Winners string
	Average string
}

// leadDay is one day of the month in the who-led strip. Tones holds a
// colour for each player who led at the end of it, in the order the month
// ranks them: 1 to 3 for the three who led longest and 4 for anybody else.
// A shared day has a band for each; a day still to come has none. Note is
// what the strip's head says while the day is picked out.
type leadDay struct {
	Tones []int
	Title string
	Note  string
}

// leadLegend names one of the colours in the strip.
type leadLegend struct {
	Tone int
	Name string
	Days string
}

type monthsPage struct {
	chrome

	Prefix    string
	BoardPath string
	Query     boardQuery

	Chips []monthChip

	// InPlace is the attributes the page's controls carry — a month's pill,
	// the season's years, a month's column and a tile in the season — which
	// change which month the page shows rather than going to another page.
	// See monthsInPlace.
	InPlace template.HTMLAttr

	// The page head: the year, and the rule a month is won by.
	Year string
	Rule string

	// The month's own card: which month and how far through it, who is
	// ahead or won, and by how much.
	Eyebrow  string
	Running  bool
	Headline string
	Sub      string

	// The group's average for the month, and how it moved from the month
	// before when there is one, set against it as GroupVs says.
	Group      string
	GroupTrend trend
	GroupVs    string
	// VsHead heads the table's column of moves, "vs Aug", the month before
	// abbreviated as a column heading must be; empty when there is none.
	VsHead string

	// Who led at the end of each day, as a strip of the month's days, and a
	// legend for the three who led longest.
	LeadDays   []leadDay
	LeadNote   string
	LeadLegend []leadLegend

	Rows []monthRow
	Thin []monthRow

	Season     []seasonRow
	SeasonCols []seasonCol
	// SeasonYears switches the season between years, nil when the board
	// has only the one.
	SeasonYears []chromeOpt
	SeasonTitle string
	SeasonHint  string

	// Awards are the month's four smaller titles, under its headline.
	Awards []monthAward
	// SeasonLine says who leads the season, when anybody has won a month.
	SeasonLine string

	Empty bool
}

// seasonCol heads one month of the season grid, and is the way to it.
type seasonCol struct {
	Label string
	Href  string
	On    bool
}

// seasonRow is one player's season, pre-formatted.
type seasonRow struct {
	Name    string
	Href    string
	Wins    int
	Podiums int
	// Summary is the phone's line beside the name: times in the top three
	// and average place — two figures, so it fits beside a long name.
	Summary string
	Marks   []seasonMark
}

type seasonMark struct {
	// Label is the finishing place, or a dot where they were not ranked —
	// which is not the same as finishing last. A win carries the trophy
	// instead of a 1.
	Label   string
	Won     bool
	Podium  bool
	Running bool
	// On is the month the page has open.
	On    bool
	Href  string
	Title string
}

// handleMonths renders the month-by-month view.
func (s *Server) handleMonths(w http.ResponseWriter, r *http.Request, prefix, boardPath string, readOnly bool) {
	_, players, results, query, err := s.boardData(r)
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
	page := monthsPage{chrome: ch, Prefix: prefix, BoardPath: boardPath, Query: query, InPlace: monthsInPlace}

	// The missed-day clause goes with the scoring it describes, as the kicker
	// it replaces had it.
	page.Rule = ch.T.T("months.rule.headPlain")
	if query.CountXAsSeven {
		page.Rule = ch.T.T("months.rule.head")
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
			Key:     monthKey(m),
			Short:   monthShort(ch.T, m),
			Href:    urlWith(r, "month", monthKey(m)),
			On:      i == selected,
			Running: !m.Complete(now),
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
	label := monthLabel(ch.T, m)
	monthName := ch.T.T("month." + strconv.Itoa(int(m.Month)))
	page.Year = strconv.Itoa(m.Year)
	page.Running = !m.Complete(now)
	daysInMonth := time.Date(m.Year, m.Month+1, 0, 0, 0, 0, 0, now.Location()).Day()

	if page.Running {
		page.Eyebrow = label + " · " + ch.T.T("months.running")
	} else {
		page.Eyebrow = label + " · " + ch.T.TN("months.days", m.Days)
	}

	// The next distinct average behind the front, for "is 0.12 behind".
	var second *stats.MonthPlayer
	for i := range m.Ranked {
		if len(m.Winners) > 0 && *m.Ranked[i].Average != *m.Winners[0].Average {
			second = &m.Ranked[i]
			break
		}
	}
	switch {
	case len(m.Winners) == 0:
		page.Headline = ch.T.T("months.noWinner")
	case page.Running && len(m.Winners) > 1:
		page.Headline = ch.T.T("months.hero.level", joinNames(ch.T, m.Winners), formatScore(ch.T, m.Winners[0].Average))
	case page.Running:
		page.Headline = ch.T.T("months.hero.leads", m.Winners[0].Name, formatScore(ch.T, m.Winners[0].Average))
	case len(m.Winners) > 1:
		page.Headline = ch.T.T("months.hero.shared", joinNames(ch.T, m.Winners), monthName, formatScore(ch.T, m.Winners[0].Average))
	default:
		page.Headline = ch.T.T("months.hero.won", m.Winners[0].Name, monthName, formatScore(ch.T, m.Winners[0].Average))
	}
	if page.Running && m.Year == now.Year() && m.Month == now.Month() {
		left := daysInMonth - now.Day()
		switch left {
		case 0:
			page.Sub = ch.T.T("months.hero.lastDay")
		case 1:
			page.Sub = ch.T.T("months.hero.daysLeft.one", left)
		default:
			page.Sub = ch.T.T("months.hero.daysLeft.other", left)
		}
		if second != nil && m.Margin != nil {
			page.Sub += " " + ch.T.T("months.hero.behind", second.Name, ch.T.Decimal(*m.Margin, 2))
		}
	} else if second != nil && m.Margin != nil {
		page.Sub = ch.T.T("months.hero.ahead", ch.T.Decimal(*m.Margin, 2), second.Name)
	}
	page.Group = formatScore(ch.T, m.GroupAverage)
	var prev *stats.Month
	if selected+1 < len(months) {
		prev = &months[selected+1]
		name := ch.T.T("month." + strconv.Itoa(int(prev.Month)))
		page.GroupTrend = trendOf(ch.T, m.GroupAverage, prev.GroupAverage, name)
		if page.GroupTrend.Dir != "" {
			page.GroupVs = ch.T.T("months.group.vs", name)
		}
		page.VsHead = ch.T.T("months.vsHead", monthShort(ch.T, *prev))
	}

	// Who led after each day, coloured by the three who led longest.
	opts := stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           now,
	}
	leaders := stats.DayLeaders(players, results, opts, m)
	length := m.Days
	if page.Running {
		length = daysInMonth
	}
	page.LeadDays, page.LeadLegend = leadStrip(ch.T, leaders, length)
	if page.Running {
		page.LeadNote = ch.T.T("months.progress", len(leaders), length)
	} else {
		page.LeadNote = ch.T.TN("months.days", m.Days)
	}

	for _, p := range m.Ranked {
		row := monthRowFor(p, prefix, m.Winners, m.Days, ch.T)
		row.Behind = "—"
		row.BehindPhone = ch.T.T("months.row.winner")
		if page.Running {
			row.BehindPhone = ch.T.T("months.row.leader")
		}
		if !row.Winner && len(m.Winners) > 0 {
			row.Behind = ch.T.Decimal(*p.Average-*m.Winners[0].Average, 2)
			row.BehindPhone = ch.T.T("months.row.behind", row.Behind)
		}
		if prev != nil {
			row.Vs = playerTrend(ch.T, p, *prev)
		}
		page.Rows = append(page.Rows, row)
	}
	for _, p := range m.Thin {
		page.Thin = append(page.Thin, monthRowFor(p, prefix, nil, m.Days, ch.T))
	}

	page.Awards = monthAwards(ch.T, months, selected, page.Running, prefix,
		stats.MonthAwardFigures(results, opts, m), stats.SeasonAverages(months, m.Year))

	// The season is the selected month's year. A board that spans more
	// than one has a switch between them, each year opening on its latest
	// month; one that does not has nothing to switch.
	var yearMonths []stats.Month
	for _, ym := range months {
		if ym.Year == m.Year {
			yearMonths = append(yearMonths, ym)
		}
	}
	for _, ym := range months {
		if n := len(page.SeasonYears); n == 0 || page.SeasonYears[n-1].Label != strconv.Itoa(ym.Year) {
			page.SeasonYears = append(page.SeasonYears, chromeOpt{
				Label: strconv.Itoa(ym.Year), Href: urlWith(r, "month", monthKey(ym)), On: ym.Year == m.Year,
			})
		}
	}
	if len(page.SeasonYears) < 2 {
		page.SeasonYears = nil
	}
	page.SeasonTitle = ch.T.T("months.season")
	if m.Year != now.Year() {
		page.SeasonTitle = ch.T.T("months.seasonYear", strconv.Itoa(m.Year))
	}
	page.SeasonHint = ch.T.T("months.seasonPlaces", strconv.Itoa(m.Year))
	months = yearMonths

	season := stats.ComputeSeason(months, now)
	for i := len(months) - 1; i >= 0; i-- {
		page.SeasonCols = append(page.SeasonCols, seasonCol{
			Label: shortMonthName(ch.T, months[i].Month),
			Href:  urlWith(r, "month", monthKey(months[i])),
			On:    monthKey(months[i]) == monthKey(m),
		})
	}
	mostWins := 0
	var leadersOfSeason []string
	for _, row := range season.Rows {
		view := seasonRow{Name: row.Name, Href: prefix + "/players/" + row.Slug, Wins: row.Wins, Podiums: row.Podiums}
		places, sum := 0, 0
		for mi, mark := range row.Marks {
			// Marks run oldest first, as the columns do.
			key := ""
			if idx := len(months) - 1 - mi; idx >= 0 && idx < len(months) {
				key = monthKey(months[idx])
			}
			cell := seasonMark{Label: "·", Won: mark.Won, Running: mark.Running, Href: urlWith(r, "month", key), On: key == monthKey(m)}
			if mark.Rank > 0 {
				cell.Label = ch.T.Integer(mark.Rank)
				cell.Podium = mark.Rank <= 3
				if !mark.Running {
					places++
					sum += mark.Rank
				}
			}
			cell.Title = ch.T.T("month."+strconv.Itoa(int(mark.Month))) + " " + strconv.Itoa(mark.Year)
			view.Marks = append(view.Marks, cell)
		}
		if places > 0 {
			view.Summary = ch.T.T("months.seasonSummary", ch.T.Integer(row.Podiums), ch.T.Decimal(float64(sum)/float64(places), 1))
		}
		switch {
		case row.Wins > mostWins:
			mostWins, leadersOfSeason = row.Wins, []string{row.Name}
		case row.Wins == mostWins && row.Wins > 0:
			leadersOfSeason = append(leadersOfSeason, row.Name)
		}
		page.Season = append(page.Season, view)
	}
	switch {
	case mostWins == 0:
	case len(leadersOfSeason) == 1:
		page.SeasonLine = ch.T.TP("months.seasonLeader", mostWins, leadersOfSeason[0], mostWins)
	default:
		page.SeasonLine = ch.T.TP("months.seasonShared", mostWins, joinList(ch.T, leadersOfSeason), mostWins)
	}
	// The design ends the line with what is left of the year, while it is
	// this year: the months after the running one.
	if left := 12 - int(now.Month()); page.SeasonLine != "" && m.Year == now.Year() && left > 0 {
		page.SeasonLine += " " + ch.T.TN("months.seasonToGo", left)
	}

	if !readOnly {
		if !s.issueChromeToken(w, r, &page.chrome) {
			return
		}
	}
	s.render(w, r, http.StatusOK, "months.html", page)
}

func monthRowFor(p stats.MonthPlayer, prefix string, winners []stats.MonthPlayer, days int, t translator) monthRow {
	row := monthRow{
		Rank: p.Rank, Name: p.Name, Href: prefix + "/players/" + p.Slug,
		Average: formatScore(t, p.Average), Games: p.Games, Fails: p.Fails,
		Played: t.Integer(p.Games) + "/" + t.Integer(days),
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
// Month names: the catalogue's abbreviation, "Aug", where a row of
// months has to fit — the month pills, the season's columns, a chart's or
// a heat map's labels, the table's "vs Aug" — and the name in full
// wherever a date is read, "27 September". In full, because an
// abbreviation can be a word of its own: "jul" is Christmas in Swedish.

// shortMonthName abbreviates a month for a row of months.
func shortMonthName(t translator, m time.Month) string {
	return t.T("month.short." + strconv.Itoa(int(m)))
}

// dayMonth is a date as it is read, "27 September", in the local zone.
func dayMonth(t translator, at time.Time) string {
	at = at.In(time.Local)
	return strconv.Itoa(at.Day()) + " " + t.T("month."+strconv.Itoa(int(at.Month())))
}

// monthShort names a chip: the abbreviated month, and no year. The chips are
// a row of small boxes and the one chosen is named in full directly beneath
// them, so a chip wide enough for "September 2026" spends the row's width on
// what the next line already says.
func monthShort(t translator, m stats.Month) string {
	return shortMonthName(t, m.Month)
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
// trend is how an average moved from one month to the next, ready to show:
// Dir is "down", "up" or "level", or "new" for somebody with nothing to
// set against, and empty when there is no comparison at all. Delta is the
// size of the move and Said the comparison in words for a screen reader,
// which the arrow is not.
type trend struct {
	Dir   string
	Delta string
	Said  string
}

// trendOf sets now against before, the average of the month called name.
// The move is rounded to the hundredths it is shown in, as the weekly
// announcement rounds its own, so a move too small to show is level
// rather than "▼0.00". name is the month in full — "jul" alone is
// Christmas in Swedish.
func trendOf(t translator, now, before *float64, name string) trend {
	if now == nil || before == nil {
		return trend{}
	}
	delta := math.Round((*now-*before)*100) / 100
	tr := trend{Delta: t.Decimal(math.Abs(delta), 2)}
	switch {
	case delta < 0:
		tr.Dir, tr.Said = "down", t.T("months.group.lower", tr.Delta, name)
	case delta > 0:
		tr.Dir, tr.Said = "up", t.T("months.group.higher", tr.Delta, name)
	default:
		tr.Dir, tr.Said = "level", t.T("months.group.same", name)
	}
	return tr
}

// playerTrend sets a player's month against their own in prev, or marks
// them new to it when prev did not rank them.
func playerTrend(t translator, p stats.MonthPlayer, prev stats.Month) trend {
	name := t.T("month." + strconv.Itoa(int(prev.Month)))
	for _, q := range prev.Ranked {
		if q.ID == p.ID {
			return trendOf(t, p.Average, q.Average, name)
		}
	}
	return trend{Dir: "new", Said: t.T("months.vs.newSaid", name)}
}

// leadStrip draws who led at the end of each of length days, and the
// legend for its colours. A colour is a player's, not a set of players':
// a day two shared counts for both of them and shows a band of each, so a
// tie at the front never takes a colour of its own away from a player.
// The three who led on most days have a colour each, and everybody else
// shares the fourth, totalled in the legend as Others.
func leadStrip(t translator, leaders [][]stats.MonthPlayer, length int) ([]leadDay, []leadLegend) {
	daysLed := map[int64]int{}
	var order []stats.MonthPlayer
	for _, day := range leaders {
		for _, p := range day {
			if daysLed[p.ID] == 0 {
				order = append(order, p)
			}
			daysLed[p.ID]++
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return daysLed[order[i].ID] > daysLed[order[j].ID] })

	tone := map[int64]int{}
	var legend []leadLegend
	others := 0
	for i, p := range order {
		tone[p.ID] = min(i+1, 4)
		if i < 3 {
			legend = append(legend, leadLegend{Tone: i + 1, Name: p.Name, Days: t.TN("months.days", daysLed[p.ID])})
		} else {
			others += daysLed[p.ID]
		}
	}
	if others > 0 {
		legend = append(legend, leadLegend{Tone: 4, Name: t.T("months.others"), Days: t.TN("months.days", others)})
	}

	days := make([]leadDay, 0, length)
	for d := 0; d < length; d++ {
		day := leadDay{Title: t.T("months.day", d+1)}
		if d < len(leaders) && len(leaders[d]) > 0 {
			for _, p := range leaders[d] {
				day.Tones = append(day.Tones, tone[p.ID])
			}
			who := t.T("months.led", joinNames(t, leaders[d]))
			day.Note = day.Title + " · " + who
			day.Title += ": " + who
		}
		days = append(days, day)
	}
	return days, legend
}

func joinNames(t translator, ps []stats.MonthPlayer) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return joinList(t, names)
}

// joinList joins names the way a sentence does: "A, B and C".
func joinList(t translator, names []string) string {
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

// monthAward is one of the month's smaller titles: what it is for, who holds
// it, and by how much.
type monthAward struct {
	Icon  string
	Title string
	Who   []awardPart
	What  string
}

// awardPart is a piece of who holds an award: a player's name, linked to
// their page, or the words between names — "and", "3 players", "—".
type awardPart struct {
	Text, Href string
}

// Day minimums for the awards that need a run of play to mean anything:
// a spread over three games says little, and a best week needs a week.
const (
	steadiestMinDays = 5
	bestWeekMinDays  = 7
)

// awardCandidate is one award the month could show: Ready when it has a
// holder at all, which a month too young or a board too new may not, and
// Holders how many share it.
type awardCandidate struct {
	monthAward
	Ready   bool
	Holders int
}

// monthAwards picks the month's four awards from six candidates, in this
// order: most improved, most 1s and 2s, steadiest, best week, biggest
// single-day beat and above their own level. A candidate is passed over
// when it has no holder yet or more than two share it, since a title
// half the group holds says nothing about anybody; when that leaves fewer
// than four, the first passed over fill the gap, so the month always has
// four, kept in that order. A running month's titles say "so far".
func monthAwards(t translator, months []stats.Month, selected int, running bool, prefix string,
	figs map[int64]stats.MonthFigures, season map[int64]float64) []monthAward {

	m := months[selected]
	if len(m.Ranked) == 0 {
		return nil
	}
	monthName := t.T("month." + strconv.Itoa(int(m.Month)))
	title := func(key string) string {
		if running {
			return t.T(key + "SoFar")
		}
		return t.T(key)
	}
	// who names the holders, each a link to their page: one or two by
	// name, more as the first of them and how many others — "Pia +2" —
	// rather than a count with nobody in it.
	who := func(holders []stats.MonthPlayer) []awardPart {
		link := func(p stats.MonthPlayer) awardPart {
			return awardPart{Text: p.Name, Href: prefix + "/players/" + p.Slug}
		}
		switch {
		case len(holders) == 0:
			return []awardPart{{Text: "—"}}
		case len(holders) > 2:
			return []awardPart{link(holders[0]), {Text: " +" + t.Integer(len(holders)-1)}}
		case len(holders) == 2:
			return []awardPart{link(holders[0]), {Text: " " + t.T("list.and") + " "}, link(holders[1])}
		}
		return []awardPart{link(holders[0])}
	}
	// best finds the players with the best of a figure, in the month's
	// order, compared in the hundredths it is shown in so that two who
	// read the same share it. ok says who has the figure at all.
	best := func(figure func(stats.MonthPlayer) (float64, bool), lower bool) (float64, []stats.MonthPlayer) {
		var top float64
		var holders []stats.MonthPlayer
		for _, p := range m.Ranked {
			v, ok := figure(p)
			if !ok {
				continue
			}
			v = math.Round(v*100) / 100
			switch {
			case holders == nil || (lower && v < top) || (!lower && v > top):
				top, holders = v, []stats.MonthPlayer{p}
			case v == top:
				holders = append(holders, p)
			}
		}
		return top, holders
	}
	candidate := func(icon, key string, holders []stats.MonthPlayer, what, none string) awardCandidate {
		c := awardCandidate{monthAward: monthAward{Icon: icon, Title: title(key), Who: who(holders), What: what}, Ready: len(holders) > 0, Holders: len(holders)}
		if !c.Ready {
			c.What = none
		}
		return c
	}

	var candidates []awardCandidate

	// Most improved: places climbed from the month before.
	var climbers []stats.MonthPlayer
	climbWhat := ""
	if selected+1 < len(months) {
		prev := months[selected+1]
		before := map[int64]int{}
		for _, p := range prev.Ranked {
			before[p.ID] = p.Rank
		}
		climb, holders := best(func(p stats.MonthPlayer) (float64, bool) {
			r, ok := before[p.ID]
			return float64(r - p.Rank), ok && r > p.Rank
		}, false)
		climbers = holders
		n := int(climb)
		climbWhat = t.TP("months.award.climbed", n, n, t.T("month."+strconv.Itoa(int(prev.Month))))
	}
	candidates = append(candidates, candidate("trending_up", "months.award.improved", climbers, climbWhat, t.T("months.award.noClimbers")))

	// Most 1s and 2s.
	quick, holders := best(func(p stats.MonthPlayer) (float64, bool) {
		return float64(p.TwoOrBetter), p.TwoOrBetter > 0
	}, false)
	candidates = append(candidates, candidate("bolt", "months.award.quick", holders,
		t.T("months.award.quickWhat", int(quick)), t.T("months.award.quickNone")))

	// Steadiest: the smallest spread, over enough games to have one.
	steady, holders := best(func(p stats.MonthPlayer) (float64, bool) {
		f := figs[p.ID]
		return derefOr(f.Spread), p.Games >= steadiestMinDays && f.Spread != nil
	}, true)
	candidates = append(candidates, candidate("straighten", "months.award.steady", holders,
		t.T("months.award.steadyWhat", t.Decimal(steady, 2)), t.T("months.award.minDays", steadiestMinDays)))

	// Best week: the lowest seven days running.
	week, holders := best(func(p stats.MonthPlayer) (float64, bool) {
		f := figs[p.ID]
		if f.BestWeek == nil || p.Games < bestWeekMinDays {
			return 0, false
		}
		return f.BestWeek.Average, true
	}, true)
	weekWhat := ""
	if len(holders) > 0 {
		w := figs[holders[0].ID].BestWeek
		last := w.First.AddDate(0, 0, w.Days-1)
		weekWhat = t.T("months.award.weekWhat", t.Decimal(week, 2), w.First.Day(), last.Day(), monthName)
	}
	candidates = append(candidates, candidate("date_range", "months.award.week", holders,
		weekWhat, t.T("months.award.minDays", bestWeekMinDays)))

	// Biggest single-day beat: furthest under the group on one day.
	beat, holders := best(func(p stats.MonthPlayer) (float64, bool) {
		f := figs[p.ID]
		if f.BestBeat == nil {
			return 0, false
		}
		return f.BestBeat.Margin, true
	}, false)
	beatWhat := ""
	if len(holders) > 0 {
		beatWhat = t.T("months.award.beatWhat", t.Decimal(beat, 2), figs[holders[0].ID].BestBeat.Date.Day(), monthName)
	}
	candidates = append(candidates, candidate("rocket_launch", "months.award.beat", holders,
		beatWhat, t.T("months.award.beatNone")))

	// Above their own level: how far the month beat the player's season.
	level, holders := best(func(p stats.MonthPlayer) (float64, bool) {
		s, ok := season[p.ID]
		if !ok {
			return 0, false
		}
		d := s - *p.Average
		return d, math.Round(d*100) > 0
	}, false)
	candidates = append(candidates, candidate("north_east", "months.award.level", holders,
		t.T("months.award.levelWhat", t.Decimal(level, 2)), t.T("months.award.levelNone")))

	return pickAwards(candidates, 4)
}

// pickAwards takes up to n candidates in order, passing over any not ready
// or shared by more than two, and fills from those passed over, first
// first, when that leaves fewer than n. The result keeps the candidates'
// order.
func pickAwards(candidates []awardCandidate, n int) []monthAward {
	take := make([]bool, len(candidates))
	taken := 0
	for i, c := range candidates {
		if taken < n && c.Ready && c.Holders <= 2 {
			take[i] = true
			taken++
		}
	}
	for i := range candidates {
		if taken < n && !take[i] {
			take[i] = true
			taken++
		}
	}
	var out []monthAward
	for i, c := range candidates {
		if take[i] {
			out = append(out, c.monthAward)
		}
	}
	return out
}

func derefOr(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
