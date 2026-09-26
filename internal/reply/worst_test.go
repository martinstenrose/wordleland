package reply

import (
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// "Who is worst" is the other end of the same table, among those who
// played most of the span — the weekly recap's rule, so a two-day span
// needs both days and a week five.
func TestWorstNamesTheBottomAmongRegulars(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()

	got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanDays, Days: 7, Worst: true}, nil, players, results, now)
	if got != "🥄 The last 7 days: Bo brings up the rear on 4.00 on average." {
		t.Errorf("en: %q", got)
	}
	got = answer(translator(t, "sv"), Request{Kind: KindLeader, Span: SpanMonth, Worst: true}, nil, players, results, now)
	if got != "🥄 September: Bo är jumbo på 4,20 i snitt." {
		t.Errorf("sv: %q", got)
	}
}

func TestWorstLeavesOutWhoeverWasAway(t *testing.T) {
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	// Alma and Bo both played both days; Cid only today, scoring an X for
	// yesterday under the month's rules and looking worst of all.
	results := play(t, alma.ID, current-1, current, 3, 0)
	results = append(results, play(t, bo.ID, current-1, current, 4, 0)...)
	results = append(results, play(t, cid.ID, current, current, 2, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanDays, Days: 2, Worst: true},
		nil, []store.Player{alma, bo, cid}, results, now)
	if got != "🥄 The last 2 days: Bo brings up the rear on 4.00 on average." {
		t.Errorf("got %q", got)
	}

	// With only one regular there is nobody to be behind.
	got = answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanDays, Days: 2, Worst: true},
		nil, []store.Player{alma, cid}, results, now)
	if got != "The last 2 days: too few have played most of the days to name a last place." {
		t.Errorf("got %q", got)
	}
}

func TestWorstTieNamesEveryone(t *testing.T) {
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	results := play(t, alma.ID, current-6, current, 3, 0)
	results = append(results, play(t, bo.ID, current-6, current, 5, 0)...)
	results = append(results, play(t, cid.ID, current-6, current, 5, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanDays, Days: 7, Worst: true},
		nil, []store.Player{alma, bo, cid}, results, now)
	if got != "🥄 The last 7 days: Bo and Cid Larsson share the bottom spot on 5.00." {
		t.Errorf("got %q", got)
	}
}

func TestParseRequestKeepsWorstOnlyForALeaderQuestion(t *testing.T) {
	got, _ := parseRequest(`{"kind":"leader","span":"days","days":2,"worst":true,"player":"","topic":"","date":""}`)
	if !got.Worst {
		t.Errorf("worst dropped on a leader question: %+v", got)
	}
	got, _ = parseRequest(`{"kind":"standing","span":"month","days":0,"worst":true,"player":"Bo","topic":"","date":""}`)
	if got.Worst {
		t.Errorf("worst kept on a standing question: %+v", got)
	}
}
