package web

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
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
// average on either side there is nothing to compare.
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
