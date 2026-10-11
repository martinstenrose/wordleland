package web

import (
	"reflect"
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
