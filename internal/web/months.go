package web

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// monthRow is one player's month, pre-formatted.
type monthRow struct {
	Rank    int
	Name    string
	Href    string
	Average string
	// Behind is the gap to the front, "+0.12", or an em dash for whoever is
	// at it.
	Behind string
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

// leadDay is one day of the month in the who-led strip: Tone picks the
// leader's colour, 1 to 3 for the three who led longest and 4 for anybody
// else, and 0 is a day still to come.
type leadDay struct {
	Tone  int
	Title string
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

	// The page head: the year, and the rule a month is won by.
	Year string
	Rule string

	// The month's own card: which month and how far through it, who is
	// ahead or won, and by how much.
	Eyebrow  string
	Running  bool
	Headline string
	Sub      string
	Meta     string

	// Who led at the end of each day, as a strip of the month's days, and a
	// legend for the three who led longest. On a phone a running month shows
	// how far through it is instead: DayBar is 2 for today, 1 for a day
	// gone, 0 for one to come.
	LeadDays   []leadDay
	LeadNote   string
	LeadLegend []leadLegend
	DayBar     []int
	DayLabel   string

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
	page := monthsPage{chrome: ch, Prefix: prefix, BoardPath: boardPath, Query: query}

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
	page.Meta = ch.T.TN("months.fullMonth", m.Days)
	if page.Running {
		page.Meta = ch.T.TN("months.partialMonth", m.Days)
	}
	page.Meta += " · " + ch.T.T("months.groupAverage") + " " + formatScore(ch.T, m.GroupAverage)

	// Who led after each day, coloured by the three who led longest.
	opts := stats.Options{
		CountXAsSeven: query.CountXAsSeven,
		CountMissed:   query.CountMissed,
		HardModeOnly:  query.HardModeOnly,
		Now:           now,
	}
	leaders := stats.DayLeaders(players, results, opts, m)
	daysLed := map[string]int{}
	var order []string
	for _, day := range leaders {
		if len(day) == 0 {
			continue
		}
		name := joinNames(ch.T, day)
		if daysLed[name] == 0 {
			order = append(order, name)
		}
		daysLed[name]++
	}
	sort.SliceStable(order, func(i, j int) bool { return daysLed[order[i]] > daysLed[order[j]] })
	tone := map[string]int{}
	for i, name := range order {
		tone[name] = min(i+1, 4)
		if i < 3 {
			page.LeadLegend = append(page.LeadLegend, leadLegend{Tone: i + 1, Name: name, Days: ch.T.TN("months.days", daysLed[name])})
		}
	}
	length := m.Days
	if page.Running {
		length = daysInMonth
	}
	for d := 0; d < length; d++ {
		day := leadDay{Title: ch.T.T("months.day", d+1)}
		if d < len(leaders) && len(leaders[d]) > 0 {
			name := joinNames(ch.T, leaders[d])
			day.Tone = tone[name]
			day.Title += ": " + name
		}
		page.LeadDays = append(page.LeadDays, day)
	}
	if page.Running {
		page.LeadNote = ch.T.T("months.progress", len(leaders), length)
		page.DayLabel = ch.T.T("months.dayOf", now.Day(), daysInMonth)
		for d := 1; d <= daysInMonth; d++ {
			switch {
			case d == now.Day():
				page.DayBar = append(page.DayBar, 2)
			case d < now.Day():
				page.DayBar = append(page.DayBar, 1)
			default:
				page.DayBar = append(page.DayBar, 0)
			}
		}
	} else {
		page.LeadNote = ch.T.TN("months.days", m.Days)
	}

	for _, p := range m.Ranked {
		row := monthRowFor(p, prefix, m.Winners, m.Days, ch.T)
		row.Behind = "—"
		if !row.Winner && len(m.Winners) > 0 {
			row.Behind = "+" + ch.T.Decimal(*p.Average-*m.Winners[0].Average, 2)
		}
		page.Rows = append(page.Rows, row)
	}
	for _, p := range m.Thin {
		page.Thin = append(page.Thin, monthRowFor(p, prefix, nil, m.Days, ch.T))
	}

	page.Awards = monthAwards(ch.T, months, selected, page.Running, prefix)

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

// monthAwards names the month's most improved, fewest fails, most ones and
// twos, and longest solving run, among the players it ranks. A title shared
// by more than two goes to "N players" rather than a list that will not fit.
func monthAwards(t translator, months []stats.Month, selected int, running bool, prefix string) []monthAward {
	m := months[selected]
	if len(m.Ranked) == 0 {
		return nil
	}
	// who names the holders, each a link to their page; more than two is a
	// count, which has nobody to link to.
	who := func(holders []stats.MonthPlayer) []awardPart {
		if len(holders) > 2 {
			return []awardPart{{Text: t.TN("months.award.players", len(holders))}}
		}
		var parts []awardPart
		for i, p := range holders {
			if i > 0 {
				parts = append(parts, awardPart{Text: " " + t.T("list.and") + " "})
			}
			parts = append(parts, awardPart{Text: p.Name, Href: prefix + "/players/" + p.Slug})
		}
		return parts
	}
	nobody := []awardPart{{Text: "—"}}
	// top finds the players holding the best value of a figure, higher or
	// lower being better as asked.
	top := func(value func(stats.MonthPlayer) int, higher bool) (int, []stats.MonthPlayer) {
		best, holders := 0, []stats.MonthPlayer(nil)
		for i, p := range m.Ranked {
			v := value(p)
			if i == 0 || (higher && v > best) || (!higher && v < best) {
				best, holders = v, []stats.MonthPlayer{p}
			} else if v == best {
				holders = append(holders, p)
			}
		}
		return best, holders
	}

	var awards []monthAward

	improved := monthAward{Icon: "trending_up", Title: t.T("months.award.improved"), Who: nobody, What: t.T("months.award.noClimbers")}
	if running {
		improved.Title = t.T("months.award.improvedSoFar")
	}
	if selected+1 < len(months) {
		prev := months[selected+1]
		before := map[int64]int{}
		for _, p := range prev.Ranked {
			before[p.ID] = p.Rank
		}
		climb, climbers := 0, []stats.MonthPlayer(nil)
		for _, p := range m.Ranked {
			r, ok := before[p.ID]
			if !ok {
				continue
			}
			switch up := r - p.Rank; {
			case up > climb:
				climb, climbers = up, []stats.MonthPlayer{p}
			case up == climb && up > 0:
				climbers = append(climbers, p)
			}
		}
		if climb > 0 {
			improved.Who = who(climbers)
			improved.What = t.TP("months.award.climbed", climb, climb, t.T("month."+strconv.Itoa(int(prev.Month))))
		}
	}
	awards = append(awards, improved)

	fails, names := top(func(p stats.MonthPlayer) int { return p.Fails }, false)
	fewest := monthAward{Icon: "verified", Title: t.T("months.award.fewestFails"), Who: who(names), What: t.TP("months.award.fails", fails, fails)}
	if fails == 0 {
		fewest.What = t.T("months.award.noFails")
	}
	awards = append(awards, fewest)

	quick, names := top(func(p stats.MonthPlayer) int { return p.TwoOrBetter }, true)
	awards = append(awards, monthAward{Icon: "bolt", Title: t.T("months.award.quick"), Who: who(names), What: t.T("months.award.quickWhat", quick)})

	run, names := top(func(p stats.MonthPlayer) int { return p.BestRun }, true)
	awards = append(awards, monthAward{Icon: "local_fire_department", Title: t.T("months.award.run"), Who: who(names), What: t.TP("months.award.runWhat", run, run)})
	return awards
}
