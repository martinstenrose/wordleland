package reply

import (
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// The fixture is two players, mid-month, so every span has games in it:
// Alma has solved every day of the month in 3, Bo every day in 4 except
// one failure ten days ago. Synthetic names, as CLAUDE.md asks.
var (
	alma = store.Player{ID: 1, Slug: "alma", Name: "Alma", Active: true}
	bo   = store.Player{ID: 2, Slug: "bo", Name: "Bo", Active: true}
	cid  = store.Player{ID: 3, Slug: "cid", Name: "Cid Larsson", Active: true}
)

func fixtureNow() time.Time {
	return time.Date(2026, time.September, 15, 12, 0, 0, 0, time.Local)
}

// play files one solved result per puzzle from first to last for a player.
func play(t *testing.T, player int64, first, last, guesses int, failAt int) []store.BoardResult {
	t.Helper()
	var out []store.BoardResult
	for p := first; p <= last; p++ {
		date, err := wordle.DateForPuzzle(p)
		if err != nil {
			t.Fatalf("DateForPuzzle(%d): %v", p, err)
		}
		r := store.BoardResult{PlayerID: player, PuzzleNo: p, Date: date, Guesses: guesses, Solved: true}
		if p == failAt {
			r.Guesses, r.Solved = 0, false
		}
		out = append(out, r)
	}
	return out
}

func fixture(t *testing.T) ([]store.Player, []store.BoardResult) {
	t.Helper()
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	results := play(t, alma.ID, first, current, 3, 0)
	results = append(results, play(t, bo.ID, first, current, 4, current-10)...)
	return []store.Player{alma, bo, cid}, results
}

func translator(t *testing.T, locale string) i18n.Translator {
	t.Helper()
	cats, err := i18n.Load()
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	return i18n.NewTranslator(cats, locale)
}

func TestAnswers(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)

	tests := []struct {
		name   string
		locale string
		req    Request
		asker  *store.Player
		want   string
	}{
		{
			// Bo's one failure is a 7 against a month of 4s.
			name: "who leads this month",
			req:  Request{Kind: KindLeader, Span: SpanMonth},
			want: "📊 September: Alma leads on 3.00 on average, 120 points clear of Bo.",
		},
		{
			name: "who leads the last seven days",
			req:  Request{Kind: KindLeader, Span: SpanDays, Days: 7},
			want: "📊 The last 7 days: Alma leads on 3.00 on average, 100 points clear of Bo.",
		},
		{
			name: "who leads all time",
			req:  Request{Kind: KindLeader, Span: SpanAll},
			want: "📊 All time: Alma leads on 3.00 on average, 120 points clear of Bo.",
		},
		{
			name: "in Swedish",
			req:  Request{Kind: KindLeader, Span: SpanDays, Days: 7}, locale: "sv",
			want: "📊 De senaste 7 dagarna: Alma leder på 3,00 i snitt, 100 punkter före Bo.",
		},
		{
			name: "a named player's standing",
			req:  Request{Kind: KindStanding, Span: SpanMonth, Player: "Bo"},
			want: "Bo: place 2 of 2 (September), 4.20 on average over 15 games.",
		},
		{
			// "How am I doing" — the model names nobody, the asker is who.
			name: "the asker's own standing", asker: &bo,
			req:  Request{Kind: KindStanding, Span: SpanMonth},
			want: "Bo: place 2 of 2 (September), 4.20 on average over 15 games.",
		},
		{
			name: "a first name for a full one",
			req:  Request{Kind: KindStanding, Span: SpanMonth, Player: "cid"},
			want: "Cid Larsson has no games (September).",
		},
		{
			name: "a name that is nobody's",
			req:  Request{Kind: KindStanding, Span: SpanMonth, Player: "Dag"},
			want: "I don't know Dag. I know Alma, Bo and Cid Larsson.",
		},
		{
			name: "nobody named and the asker unknown",
			req:  Request{Kind: KindStanding, Span: SpanMonth},
			want: "Who do you mean? I know Alma, Bo and Cid Larsson.",
		},
		{
			name: "streaks",
			req:  Request{Kind: KindStreak},
			want: "🔥 Longest streak going: Alma, 15 days.\nLongest ever: Alma, 15 days.",
		},
		{
			name: "one player's streak",
			req:  Request{Kind: KindStreak, Player: "Bo"},
			want: "Bo: 10 days in a row now, 10 at best.",
		},
		{
			name: "today",
			req:  Request{Kind: KindToday},
			want: "Wordle " + i18n.Identifier(current) + ": 2 of 3 in.\nBest so far: Alma in 3.\nStill to post: Cid Larsson.",
		},
		{
			name: "something else",
			req:  Request{Kind: KindUnknown},
			want: "I can answer who is leading",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			locale := tc.locale
			if locale == "" {
				locale = "en"
			}
			got := answer(translator(t, locale), tc.req, tc.asker, players, results, now)
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestLeaderWithNoGamesSaysSo(t *testing.T) {
	got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanDays, Days: 3},
		nil, []store.Player{alma}, nil, fixtureNow())
	if got != "The last 3 days: nobody has played yet." {
		t.Errorf("got %q", got)
	}
}

// A tie is every name, never one of them.
func TestLeaderTieNamesEveryone(t *testing.T) {
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	results := append(play(t, alma.ID, current-6, current, 3, 0), play(t, bo.ID, current-6, current, 3, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanDays, Days: 7},
		nil, []store.Player{alma, bo}, results, now)
	if got != "📊 The last 7 days: Alma and Bo are level at the top on 3.00." {
		t.Errorf("got %q", got)
	}
}
