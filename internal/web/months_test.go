package web

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// A day two shared counts for both and shows both, in their own colours —
// a tie never takes a colour of its own — and whoever is outside the three
// who led longest is totalled as Others.
func TestLeadStripGivesEachLeaderTheirOwnColour(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	tr := translator{locale: "en", strings: srv.catalogues["en"], fallback: srv.catalogues["en"]}
	p := func(id int64, name string) stats.MonthPlayer {
		return stats.MonthPlayer{Player: store.Player{ID: id, Name: name}}
	}
	ana, bo, cy, di, ed := p(1, "Ana"), p(2, "Bo"), p(3, "Cy"), p(4, "Di"), p(5, "Ed")
	leaders := [][]stats.MonthPlayer{{ana}, {ana, bo}, {bo}, {bo}, {cy}, {di}, {ed}, {ana}}

	days, legend := leadStrip(tr, leaders, 10)

	wantLegend := []leadLegend{
		{Tone: 1, Name: "Ana", Days: "3 days"},
		{Tone: 2, Name: "Bo", Days: "3 days"},
		{Tone: 3, Name: "Cy", Days: "1 day"},
		{Tone: 4, Name: "Others", Days: "2 days"},
	}
	if !reflect.DeepEqual(legend, wantLegend) {
		t.Errorf("legend = %+v, want %+v", legend, wantLegend)
	}

	if len(days) != 10 {
		t.Fatalf("%d days in the strip, want 10", len(days))
	}
	for i, want := range []leadDay{
		{Tones: []int{1}, Title: "Day 1: Ana led", Note: "Day 1 · Ana led"},
		{Tones: []int{1, 2}, Title: "Day 2: Ana and Bo led", Note: "Day 2 · Ana and Bo led"},
		{Tones: []int{4}, Title: "Day 6: Di led", Note: "Day 6 · Di led"},
		{Tones: nil, Title: "Day 9"},
	} {
		got := days[[]int{0, 1, 5, 8}[i]]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("day = %+v, want %+v", got, want)
		}
	}
}

// An average is set against the month before: down is better and up is
// worse, a move that rounds to nothing in hundredths is level, and with no
// average on either side there is nothing to compare. A player the month
// before did not rank is new to it.
func TestTrendSetsAMonthAgainstTheOneBefore(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	en := translator{locale: "en", strings: srv.catalogues["en"], fallback: srv.catalogues["en"]}
	sv := translator{locale: "sv", strings: srv.catalogues["sv"], fallback: srv.catalogues["en"]}
	avg := func(v float64) *float64 { return &v }

	for _, c := range []struct {
		name        string
		tr          translator
		now, before *float64
		want        trend
	}{
		{"better", en, avg(3.83), avg(3.95), trend{"down", "0.12", "0.12 lower than July"}},
		{"worse", en, avg(4.10), avg(3.95), trend{"up", "0.15", "0.15 higher than July"}},
		{"too small to show", en, avg(3.951), avg(3.948), trend{"level", "0.00", "the same as July"}},
		{"in Swedish, the month in full", sv, avg(3.83), avg(3.95), trend{"down", "0,12", "0,12 lägre än i juli"}},
		{"no average before", en, avg(3.83), nil, trend{}},
	} {
		name := c.tr.T("month.7")
		if got := trendOf(c.tr, c.now, c.before, name); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}

	p := func(id int64, a float64) stats.MonthPlayer {
		return stats.MonthPlayer{Player: store.Player{ID: id}, Average: avg(a)}
	}
	july := stats.Month{Year: 2026, Month: time.July, Ranked: []stats.MonthPlayer{p(1, 4.00)}}
	if got, want := playerTrend(en, p(1, 3.50), july), (trend{"down", "0.50", "0.50 lower than July"}); got != want {
		t.Errorf("a player ranked in July: got %+v, want %+v", got, want)
	}
	if got, want := playerTrend(en, p(2, 3.50), july), (trend{Dir: "new", Said: "not ranked in July"}); got != want {
		t.Errorf("a player new to the board: got %+v, want %+v", got, want)
	}
}

// The month's card ends on the group's average and how it moved, not on
// how many days the month has had — the eyebrow and the strip say that.
func TestMonthCardEndsOnTheGroupAverage(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, c := range []struct{ lang, group, gone string }{
		{"en", "Group average <b class=\"num\">", "month ·"},
		{"sv", "Gruppens snitt <b class=\"num\">", "månad ·"},
	} {
		body := fetchAs(t, srv, "/share/"+slug+"/months?lang="+c.lang, nil).Body.String()
		hero, ok := sectionOf(body, `<section class="card month-hero">`, "</section>")
		if !ok {
			t.Fatalf("%s: the month has no card", c.lang)
		}
		if !strings.Contains(hero, c.group) {
			t.Errorf("%s: the card does not give the group's average", c.lang)
		}
		if strings.Contains(hero, c.gone) {
			t.Errorf("%s: the card still says how long the month is (%q):\n%s", c.lang, c.gone, hero)
		}
		if !strings.Contains(hero, `class="month-trend `) || !strings.Contains(hero, `class="visually-hidden"`) {
			t.Errorf("%s: the running month's average is not set against the month before, in arrow and in words", c.lang)
		}
	}
}

// The table sets each player's month against their own month before: an
// arrow and the move in the "vs" column, the same on a phone's second line
// beside their fails, and "new" for somebody the month before did not rank.
// The board's oldest month has nothing to set against and no such column.
func TestMonthTableSetsEachPlayerAgainstTheirMonthBefore(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	ctx := context.Background()
	first := time.Date(time.Now().Year(), time.Now().Month()-5, 1, 0, 0, 0, 0, time.Local)
	second := first.AddDate(0, 1, 0)
	ana, err := store.CreatePlayer(ctx, srv.db, store.SystemActor(), "Ana", "ana")
	if err != nil {
		t.Fatal(err)
	}
	bo, err := store.CreatePlayer(ctx, srv.db, store.SystemActor(), "Bo", "bo")
	if err != nil {
		t.Fatal(err)
	}
	for d := 0; d < time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, time.Local).Day(); d++ {
		seedResult(t, srv, ana.ID, wordle.PuzzleForDate(first.AddDate(0, 0, d)), 4, false)
	}
	for d := 0; d < time.Date(second.Year(), second.Month()+1, 0, 0, 0, 0, 0, time.Local).Day(); d++ {
		seedResult(t, srv, ana.ID, wordle.PuzzleForDate(second.AddDate(0, 0, d)), 3, false)
		seedResult(t, srv, bo.ID, wordle.PuzzleForDate(second.AddDate(0, 0, d)), 5, false)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)
	page := func(m time.Time) string {
		body := fetchAs(t, srv, fmt.Sprintf("/share/%s/months?month=%d-%d", slug, m.Year(), m.Month()), nil).Body.String()
		table, ok := sectionOf(body, `<section class="card month-table`, "</section>")
		if !ok {
			t.Fatalf("no table for %s", m.Format("2006-01"))
		}
		return table
	}

	table := page(second)
	short := srv.catalogues["en"]["month.short."+strconv.Itoa(int(first.Month()))]
	full := srv.catalogues["en"]["month."+strconv.Itoa(int(first.Month()))]
	for _, want := range []string{
		`<section class="card month-table with-vs">`,
		`<span class="right">vs ` + short + `</span>`,
		`<span class="month-trend down num" aria-hidden="true">▼1.00</span><span class="visually-hidden">1.00 lower than ` + full + `</span>`,
		`<span class="month-trend new num" aria-hidden="true">new</span><span class="visually-hidden">not ranked in ` + full + `</span>`,
		`<span aria-hidden="true"> vs ` + short + `</span> · 0 fails</span>`,
		// A phone's row says what the figure under the average is.
		`<span class="month-behind-phone num">winner</span>`,
		`<span class="month-behind-phone num">2.00 behind</span>`,
	} {
		if !strings.Contains(table, want) {
			t.Errorf("the second month's table is missing %s", want)
		}
	}

	if oldest := page(first); strings.Contains(oldest, "with-vs") || strings.Contains(oldest, "month-trend") {
		t.Error("the board's oldest month sets its players against a month it does not have")
	}
}
