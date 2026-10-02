package reply

import (
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Sixty days: Alma 4s then 3s (improving), Bo 3s then 4s (slipping), Cid
// alternating 2s and 6s (erratic, and even).
func formFixture(t *testing.T) ([]store.Player, []store.BoardResult) {
	t.Helper()
	current := wordle.PuzzleForDate(fixtureNow())
	players := []store.Player{alma, bo, cid}
	half := current - 29
	return players, scored(t, current-59, current, players, func(puzzle int, p store.Player) int {
		switch p.ID {
		case alma.ID:
			if puzzle < half {
				return 4
			}
			return 3
		case bo.ID:
			if puzzle < half {
				return 3
			}
			return 4
		}
		if puzzle%2 == 0 {
			return 2
		}
		return 6
	})
}

func TestForm(t *testing.T) {
	t.Parallel()
	players, results := formFixture(t)
	now := fixtureNow()
	tests := []struct {
		loc  string
		req  Request
		want string
	}{
		{"en", Request{Kind: KindForm},
			"🔥 Best form right now: Alma, 3.00 over the last 30 puzzles.\nMost improved: Alma, 0.50 better than their own average."},
		{"sv", Request{Kind: KindForm, Worst: true},
			"🥶 Kallast form just nu: Bo och Cid Larsson, 4,00 i snitt över de senaste 30 pusslen.\nTappar mest: Bo, 0,50 sämre än sitt eget snitt."},
		{"sv", Request{Kind: KindForm, Player: "Alma"},
			"📈 Alma: 3,00 i snitt de senaste 30 pusslen, mot 3,50 totalt — på uppgång."},
		{"en", Request{Kind: KindForm, Player: "Bo"},
			"📉 Bo: 4.00 over the last 30 puzzles, against 3.50 overall — slipping."},
		{"en", Request{Kind: KindForm, Player: "Cid"},
			"Cid Larsson: 4.00 over the last 30 puzzles, against 4.00 overall — about their usual."},
	}
	for _, tc := range tests {
		if got := answer(translator(t, tc.loc), tc.req, nil, players, results, now); got != tc.want {
			t.Errorf("%+v:\ngot  %q\nwant %q", tc.req, got, tc.want)
		}
	}
	if got, want := answer(translator(t, "en"), Request{Kind: KindForm, Player: "Alma"}, nil, players, results[:9], now),
		"Alma needs 10 games in the last 30 puzzles for a form."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestSteadiness(t *testing.T) {
	t.Parallel()
	players, results := formFixture(t)
	now := fixtureNow()
	if got, want := answer(translator(t, "sv"), Request{Kind: KindSteady}, nil, players, results, now),
		"🎯 Stabilast: Alma och Bo, plus minus 0,50 gissningar. Mest oberäknelig: Cid Larsson, plus minus 2,00."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got, want := answer(translator(t, "en"), Request{Kind: KindSteady, Player: "Cid"}, nil, players, results, now),
		"🎯 Cid Larsson: give or take 2.00 guesses — 3 of 3 for steadiness."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// Form with a span is the ranking over that span: "best form this week" is
// best this week, and a player's form this week is their standing in it.
func TestFormOverASpan(t *testing.T) {
	t.Parallel()
	players, results := formFixture(t)
	now := fixtureNow()
	tr := translator(t, "sv")
	for _, tc := range []struct{ form, want Request }{
		{Request{Kind: KindForm, Span: SpanWeek}, Request{Kind: KindLeader, Span: SpanWeek}},
		{Request{Kind: KindForm, Span: SpanLastWeek, Worst: true}, Request{Kind: KindLeader, Span: SpanLastWeek, Worst: true}},
		{Request{Kind: KindForm, Span: SpanDays, Days: 14, Player: "Bo"}, Request{Kind: KindStanding, Span: SpanDays, Days: 14, Player: "Bo"}},
	} {
		got := answer(tr, tc.form, nil, players, results, now)
		if want := answer(tr, tc.want, nil, players, results, now); got != want {
			t.Errorf("%+v: got %q, want %q", tc.form, got, want)
		}
	}
	// The month is form's own default span, and keeps the form answer.
	if got := answer(tr, Request{Kind: KindForm, Span: SpanMonth}, nil, players, results, now); !strings.HasPrefix(got, "🔥") {
		t.Errorf("form over the month: %q", got)
	}
}
