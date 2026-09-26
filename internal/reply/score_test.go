package reply

import (
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// A day's score is a lookup, not a computation, with the asker as the
// default player and today as the default day.
func TestScoreOnADay(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()
	failed, _ := wordle.DateForPuzzle(wordle.PuzzleForDate(now) - 10)

	tests := []struct {
		name  string
		req   Request
		asker *store.Player
		want  string
	}{
		{name: "the day Bo failed", req: Request{Kind: KindScore, Player: "Bo", Date: failed.Format(DateLayout)},
			want: "Bo, 5 September: X — not solved."},
		{name: "the asker, today", req: Request{Kind: KindScore}, asker: &alma,
			want: "Alma, 15 September: 3/6."},
		{name: "a day before the history", req: Request{Kind: KindScore, Player: "Alma", Date: "2026-07-05"},
			want: "Alma has no result for 5 July."},
		{name: "a day still to come", req: Request{Kind: KindScore, Player: "Alma", Date: "2026-09-20"},
			want: "20 September hasn't happened yet."},
		{name: "nobody to ask about", req: Request{Kind: KindScore},
			want: "Who do you mean?"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := answer(translator(t, "en"), tc.req, tc.asker, players, results, now)
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScoreInHardModeSaysSo(t *testing.T) {
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	results := play(t, alma.ID, current, current, 2, 0)
	results[0].HardMode = true

	got := answer(translator(t, "sv"), Request{Kind: KindScore, Player: "Alma"}, nil, []store.Player{alma}, results, now)
	if got != "Alma, 15 september: 2/6, hard mode." {
		t.Errorf("got %q", got)
	}
}

// Wins count closed months only, so the month in progress hands nobody a
// title, however clearly they lead it.
func TestMonthlyWins(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()

	got := answer(translator(t, "en"), Request{Kind: KindWins}, nil, players, results, now)
	if got != "No month has been won yet." {
		t.Errorf("with only the running month: %q", got)
	}

	// July and August: Alma takes both, Bo plays them too.
	for _, month := range []time.Month{time.July, time.August} {
		first := wordle.PuzzleForDate(time.Date(2026, month, 1, 0, 0, 0, 0, time.Local))
		last := wordle.PuzzleForDate(time.Date(2026, month+1, 1, 0, 0, 0, 0, time.Local)) - 1
		results = append(results, play(t, alma.ID, first, last, 3, 0)...)
		results = append(results, play(t, bo.ID, first, last, 4, 0)...)
	}
	// June: Bo's, alone.
	juneFirst := wordle.PuzzleForDate(time.Date(2026, time.June, 1, 0, 0, 0, 0, time.Local))
	results = append(results, play(t, bo.ID, juneFirst, juneFirst+29, 3, 0)...)

	got = answer(translator(t, "en"), Request{Kind: KindWins}, nil, players, results, now)
	if got != "🏆 Most monthly wins: Alma, 2. Then Bo (1)." {
		t.Errorf("got %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindWins, Player: "Bo"}, nil, players, results, now)
	if got != "Bo: 1 monthly wins." {
		t.Errorf("got %q", got)
	}
}

func TestParseRequestKeepsADateOnlyForAScore(t *testing.T) {
	got, _ := parseRequest(`{"kind":"score","span":"month","days":0,"player":"Bo","topic":"","date":"2026-07-05"}`)
	if got.Date != "2026-07-05" {
		t.Errorf("score with a date: %+v", got)
	}
	got, _ = parseRequest(`{"kind":"score","span":"month","days":0,"player":"","topic":"","date":"July 5"}`)
	if got.Date != "" {
		t.Errorf("a date the model could not write is not today: %+v", got)
	}
	got, _ = parseRequest(`{"kind":"leader","span":"month","days":0,"player":"","topic":"","date":"2026-07-05"}`)
	if got.Date != "" {
		t.Errorf("a date survived on a non-score request: %+v", got)
	}
}
