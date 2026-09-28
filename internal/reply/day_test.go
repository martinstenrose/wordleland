package reply

import (
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// scored files a result for each player on each puzzle from first to last,
// as value says: guesses, 7 for a failure, 0 for not played.
func scored(t *testing.T, first, last int, players []store.Player, value func(puzzle int, p store.Player) int) []store.BoardResult {
	t.Helper()
	var out []store.BoardResult
	for puzzle := first; puzzle <= last; puzzle++ {
		date, err := wordle.DateForPuzzle(puzzle)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range players {
			v := value(puzzle, p)
			switch {
			case v == 0:
			case v == failGuesses:
				out = append(out, store.BoardResult{PlayerID: p.ID, PuzzleNo: puzzle, Date: date})
			default:
				out = append(out, store.BoardResult{PlayerID: p.ID, PuzzleNo: puzzle, Date: date, Guesses: v, Solved: true})
			}
		}
	}
	return out
}

// Thirty ordinary days — Alma 3s, Bo and Cid 4s — then a hard day two days
// ago and an easy one yesterday.
func dayFixture(t *testing.T) ([]store.Player, []store.BoardResult, int) {
	t.Helper()
	current := wordle.PuzzleForDate(fixtureNow())
	players := []store.Player{alma, bo, cid}
	results := scored(t, current-32, current-3, players, func(_ int, p store.Player) int {
		if p.ID == alma.ID {
			return 3
		}
		return 4
	})
	hard := map[int64]int{alma.ID: 5, bo.ID: 5, cid.ID: failGuesses}
	easy := map[int64]int{alma.ID: 2, bo.ID: 2, cid.ID: 3}
	results = append(results, scored(t, current-2, current-2, players, func(_ int, p store.Player) int { return hard[p.ID] })...)
	results = append(results, scored(t, current-1, current-1, players, func(_ int, p store.Player) int { return easy[p.ID] })...)
	return players, results, current
}

func TestADay(t *testing.T) {
	t.Parallel()
	players, results, _ := dayFixture(t)
	now := fixtureNow()
	tests := []struct {
		loc  string
		req  Request
		want string
	}{
		{"sv", Request{Kind: KindDay, Date: "2026-09-13"},
			"📅 Wordle 1912, 13 september:\nAlma 5 · Bo 5 · Cid Larsson X\n🧱 En tuff en: 5,67 i snitt, mot 3,67 normalt."},
		{"en", Request{Kind: KindDay, Date: "2026-09-14"},
			"📅 Wordle 1913, 14 September:\nAlma 2 · Bo 2 · Cid Larsson 3\n🪶 An easy one: 2.33 on average, against 3.73 usually."},
		{"en", Request{Kind: KindDay, Date: "2026-09-12"},
			"📅 Wordle 1911, 12 September:\nAlma 3 · Bo 4 · Cid Larsson 4\nGroup average 3.67, against 3.67 usually — an ordinary one."},
		{"en", Request{Kind: KindDay},
			"📅 Wordle 1914, 15 September:\nNobody has posted that one."},
		{"en", Request{Kind: KindDay, Date: "2026-09-20"}, "20 September hasn't happened yet."},
	}
	for _, tc := range tests {
		if got := answer(translator(t, tc.loc), tc.req, nil, players, results, now); got != tc.want {
			t.Errorf("%+v:\ngot  %q\nwant %q", tc.req, got, tc.want)
		}
	}
}

// Hard mode shows on the day as the board shows it, with an asterisk.
func TestADayMarksHardMode(t *testing.T) {
	t.Parallel()
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	results := play(t, alma.ID, current, current, 3, 0)
	results[0].HardMode = true
	got := answer(translator(t, "en"), Request{Kind: KindDay}, nil, []store.Player{alma}, results, now)
	if want := "📅 Wordle 1914, 15 September:\nAlma 3*\nGroup average: 3.00."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestHardestAndEasiestPuzzle(t *testing.T) {
	t.Parallel()
	players, results, _ := dayFixture(t)
	now := fixtureNow()
	if got, want := answer(translator(t, "sv"), Request{Kind: KindPuzzles}, nil, players, results, now),
		"🧱 September, svåraste pusslet: Wordle 1912 (13 september), 5,67 i snitt på 3 resultat."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got, want := answer(translator(t, "en"), Request{Kind: KindPuzzles, Worst: true, Span: SpanAll}, nil, players, results, now),
		"🪶 All time, the easiest puzzle: Wordle 1913 (14 September), 2.33 on average from 3 results."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// A day too few played is not the hardest, however bad it went.
	lonely := append(results, scored(t, 1880, 1880, []store.Player{bo}, func(int, store.Player) int { return failGuesses })...)
	if got := answer(translator(t, "en"), Request{Kind: KindPuzzles, Span: SpanAll}, nil, players, lonely, now); got[len("🧱 All time, the hardest puzzle: Wordle "):][:4] != "1912" {
		t.Errorf("a day one player failed was the hardest: %q", got)
	}
}

// Sundays hard, Tuesdays easy, everything else in between: for the group
// and for one player.
func TestWeekdays(t *testing.T) {
	t.Parallel()
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	players := []store.Player{alma, bo, cid}
	results := scored(t, current-56, current-1, players, func(puzzle int, _ store.Player) int {
		date, _ := wordle.DateForPuzzle(puzzle)
		switch date.Weekday().String() {
		case "Sunday":
			return 5
		case "Tuesday":
			return 2
		}
		return 4
	})
	if got, want := answer(translator(t, "sv"), Request{Kind: KindWeekday}, nil, players, results, now),
		"📆 Svåraste veckodagen: söndag, 5,00 i snitt. Lättast: tisdag, 2,00."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got, want := answer(translator(t, "en"), Request{Kind: KindWeekday, Player: "Bo"}, nil, players, results, now),
		"📆 Bo is best on a Tuesday (2.00) and worst on a Sunday (5.00)."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got, want := answer(translator(t, "en"), Request{Kind: KindWeekday}, nil, players, results[:9], now),
		"Not enough results on every day of the week yet."; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
