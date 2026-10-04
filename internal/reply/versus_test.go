package reply

import (
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// Head to head: each player's place over the span, then the days both
// played, won, lost and drawn.
func TestVersus(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	now := fixtureNow()
	tests := []struct {
		name  string
		loc   string
		req   Request
		asker *store.Player
		want  string
	}{
		{"two named", "en", Request{Kind: KindVersus, Player: "Alma", Other: "Bo"}, nil,
			"⚔️ Alma vs Bo, September:\n" +
				"Alma 3.00 (place 1) · Bo 4.20 (place 2)\n" +
				"Same day: Alma better 15 times, Bo 0, level 0 — of 15 days both played.\n" +
				"Alma owns this duel."},
		{"one named, against the asker", "sv", Request{Kind: KindVersus, Player: "Alma"}, &bo,
			"⚔️ Bo mot Alma, september:\n" +
				"Bo 4,20 (plats 2) · Alma 3,00 (plats 1)\n" +
				"Samma dag: Bo bättre 0 gånger, Alma 15, lika 0 — av 15 dagar båda spelat.\n" +
				"Alma äger den här duellen."},
		{"never on the same day", "en", Request{Kind: KindVersus, Player: "Alma", Other: "Cid", Span: SpanAll}, nil,
			"⚔️ Alma vs Cid Larsson, all time:\n" +
				"Alma 3.00 (place 1) · Cid Larsson not placed\n" +
				"They haven't played the same day in that span."},
		{"against themselves", "en", Request{Kind: KindVersus, Player: "Bo"}, &bo,
			"Two different players, please — who against whom?"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := answer(translator(t, tc.loc), tc.req, tc.asker, players, results, now); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// A draw on the day is a draw, and neither side owns an even duel.
func TestVersusCountsDraws(t *testing.T) {
	t.Parallel()
	now := fixtureNow()
	first, current := 1520, 1529
	results := play(t, alma.ID, first, current, 3, 0)
	results = append(results, play(t, bo.ID, first, first+4, 2, 0)...)
	results = append(results, play(t, bo.ID, first+5, current, 4, 0)...)
	req := Request{Kind: KindVersus, Player: "Alma", Other: "Bo", Span: SpanAll}
	got := answer(translator(t, "en"), req, nil, []store.Player{alma, bo}, results, now)
	want := "Same day: Alma better 5 times, Bo 5, level 0 — of 10 days both played.\nDead even. Somebody break the tie."
	if len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Errorf("got %q\nwant it to end %q", got, want)
	}
}

// A day's best score is a win for everyone who had it, on a day at least
// two played and somebody solved.
func TestDayWins(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	now := fixtureNow()
	if got, want := answer(translator(t, "en"), Request{Kind: KindDayWins}, nil, players, results, now),
		"🥇 September, most days with the day's best score: Alma, 15 of 15."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got, want := answer(translator(t, "sv"), Request{Kind: KindDayWins, Player: "Bo"}, nil, players, results, now),
		"🥇 September: Bo hade dagens bästa resultat 0 gånger på 15 spelade dagar."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// Level every day: both win every day.
	first, current := 1520, 1529
	level := append(play(t, alma.ID, first, current, 3, 0), play(t, bo.ID, first, current, 3, 0)...)
	if got, want := answer(translator(t, "en"), Request{Kind: KindDayWins, Span: SpanAll}, nil, players, level, now),
		"🥇 All time, most days with the day's best score: Alma and Bo, 10 of 10."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// This week and last week are the calendar weeks the Sunday recap closes,
// for every kind that takes a span.
func TestTheWeekSpans(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	now := fixtureNow()
	tests := []struct {
		loc  string
		req  Request
		want string
	}{
		{"sv", Request{Kind: KindLeader, Span: SpanWeek}, "📊 Den här veckan: Alma leder på 3,00 i snitt, 100 punkter före Bo."},
		{"en", Request{Kind: KindStanding, Span: SpanLastWeek}, "Last week:\n1. Alma 3.00\n2. Bo 4.00"},
		{"sv", Request{Kind: KindLeader, Span: SpanLastWeek, Worst: true}, "🐘 Förra veckan: Bo är jumbo på 4,00 i snitt."},
	}
	for _, tc := range tests {
		if got := answer(translator(t, tc.loc), tc.req, nil, players, results, now); got != tc.want {
			t.Errorf("%+v: got %q\nwant %q", tc.req, got, tc.want)
		}
	}
}
