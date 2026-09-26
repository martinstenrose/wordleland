package reply

import (
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// July and August 2026, closed months: Alma takes July by a guess over Bo;
// August is Bo's alone.
func closedMonths(t *testing.T) ([]store.Player, []store.BoardResult) {
	t.Helper()
	players, results := fixture(t)
	span := func(month time.Month) (int, int) {
		first := wordle.PuzzleForDate(time.Date(2026, month, 1, 0, 0, 0, 0, time.Local))
		return first, wordle.PuzzleForDate(time.Date(2026, month+1, 1, 0, 0, 0, 0, time.Local)) - 1
	}
	jf, jl := span(time.July)
	results = append(results, play(t, alma.ID, jf, jl, 3, 0)...)
	results = append(results, play(t, bo.ID, jf, jl, 4, 0)...)
	af, al := span(time.August)
	results = append(results, play(t, bo.ID, af, al, 3, 0)...)
	return players, results
}

// A past month has a winner, not a leader, and is spoken of the way the 🏆
// message spoke of it.
func TestANamedPastMonth(t *testing.T) {
	players, results := closedMonths(t)
	now := fixtureNow()

	got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-07"}, nil, players, results, now)
	if got != "🏆 Alma took July with an average of 3.00, by 1.00 of a guess over 31 puzzles." {
		t.Errorf("july: %q", got)
	}
	got = answer(translator(t, "sv"), Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-08"}, nil, players, results, now)
	if got != "🏆 Bo tog månaden med ett snitt på 3,00, över 31 pussel." {
		t.Errorf("august, sv: %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindStanding, Span: SpanMonth, Month: "2026-07", Player: "Bo"}, nil, players, results, now)
	if got != "Bo: place 2 of 2 (July), 4.00 on average over 31 games." {
		t.Errorf("standing in july: %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-06"}, nil, players, results, now)
	if got != "June: nobody has played yet." {
		t.Errorf("an empty month: %q", got)
	}
	// Another year is said.
	got = answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanMonth, Month: "2025-07"}, nil, players, results, now)
	if got != "July 2025: nobody has played yet." {
		t.Errorf("another year: %q", got)
	}
}

// Naming the current month is just the current month, present tense.
func TestTheCurrentMonthNamedIsTheCurrentMonth(t *testing.T) {
	players, results := fixture(t)
	got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-09"}, nil, players, results, fixtureNow())
	if !strings.HasPrefix(got, "📊 September: Alma leads") {
		t.Errorf("got %q", got)
	}
}

func TestParseRequestKeepsAMonthOnlyWhereItMeansSomething(t *testing.T) {
	got, _ := parseRequest(`{"kind":"leader","span":"days","days":7,"worst":false,"player":"","topic":"","date":"","month":"2026-07","guesses":0}`)
	if got.Month != "2026-07" || got.Span != SpanMonth || got.Days != 0 {
		t.Errorf("a named month did not win over the span: %+v", got)
	}
	got, _ = parseRequest(`{"kind":"streak","span":"month","days":0,"worst":false,"player":"","topic":"","date":"","month":"2026-07","guesses":0}`)
	if got.Month != "" {
		t.Errorf("a month survived on a streak question: %+v", got)
	}
	got, _ = parseRequest(`{"kind":"leader","span":"month","days":0,"worst":false,"player":"","topic":"","date":"","month":"July","guesses":0}`)
	if got.Month != "" {
		t.Errorf("a month the model could not write was kept: %+v", got)
	}
	got, _ = parseRequest(`{"kind":"count","span":"month","days":0,"worst":false,"player":"","topic":"","date":"","month":"","guesses":9}`)
	if got.Guesses != 0 {
		t.Errorf("guesses out of range were kept: %+v", got)
	}
	got, _ = parseRequest(`{"kind":"leader","span":"month","days":0,"worst":false,"player":"","topic":"","date":"","month":"","guesses":2}`)
	if got.Guesses != 0 {
		t.Errorf("guesses survived on a leader question: %+v", got)
	}
}

// Counts come from the whole history. The fixture: Alma fifteen 3s, Bo
// fourteen 4s and one X.
func TestCounts(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()
	tests := []struct {
		name  string
		req   Request
		asker *store.Player
		want  string
	}{
		{name: "how many 3s", req: Request{Kind: KindCount, Player: "Alma", Guesses: 3},
			want: "Alma: 15 3s out of 15 games (100%)."},
		{name: "how often does Bo fail", req: Request{Kind: KindCount, Player: "Bo", Guesses: 7},
			want: "Bo: 1 X's out of 15 games (7%)."},
		{name: "any 1s", req: Request{Kind: KindCount, Guesses: 1, Player: "Alma"}, asker: &alma,
			want: "Alma: no 1s in 15 games."},
		{name: "the whole distribution", req: Request{Kind: KindCount, Player: "Bo"},
			want: "Bo over 15 games: 0×1, 0×2, 0×3, 14×4, 0×5, 0×6, 1×X."},
		{name: "who has the most 4s", req: Request{Kind: KindCount, Guesses: 4},
			want: "Most 4s: Bo, 14."},
		{name: "nobody has one", req: Request{Kind: KindCount, Guesses: 1},
			want: "Nobody has any 1s yet."},
		{name: "distribution of nobody in particular", req: Request{Kind: KindCount},
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
	got := answer(translator(t, "sv"), Request{Kind: KindCount, Player: "Bo", Guesses: 4}, nil, players, results, now)
	if got != "Bo: 14 4:or av 15 spel (93 %)." {
		t.Errorf("sv: %q", got)
	}
}

// stamp gives every result of a player a posting time at the same wall
// clock each day, so an order exists.
func stamp(results []store.BoardResult, player int64, hour, minute int) {
	for i := range results {
		if results[i].PlayerID != player {
			continue
		}
		at := time.Date(results[i].Date.Year(), results[i].Date.Month(), results[i].Date.Day(), hour, minute, 0, 0, time.Local)
		results[i].PostedAt = &at
	}
}

func TestPostingHabits(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()

	got := answer(translator(t, "en"), Request{Kind: KindHabits}, nil, players, results, now)
	if got != "No posting order on record yet." {
		t.Errorf("unstamped: %q", got)
	}

	stamp(results, alma.ID, 7, 30)
	stamp(results, bo.ID, 21, 5)

	got = answer(translator(t, "en"), Request{Kind: KindHabits}, nil, players, results, now)
	if got != "Opens the day most often: Alma, 15 of the last 15 days.\nCloses it most often: Bo, 15." {
		t.Errorf("who: %q", got)
	}
	got = answer(translator(t, "sv"), Request{Kind: KindHabits, Player: "Bo"}, &bo, players, results, now)
	if got != "Bo brukar posta runt 21:05 — öppnade dagen 0 gånger och stängde den 15 gånger de senaste 15 dagarna." {
		t.Errorf("bo, sv: %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindHabits, Player: "Cid"}, nil, players, results, now)
	if got != "Cid Larsson: no posting times on record." {
		t.Errorf("cid: %q", got)
	}
}
