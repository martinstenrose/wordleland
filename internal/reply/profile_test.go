package reply

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// The fixture's September, plus July (Alma 4s) and August (Alma 3s), both
// with Bo on 4s and an X early on, and one lucky 1 of Bo's in May.
func seasonFixture(t *testing.T) ([]store.Player, []store.BoardResult) {
	t.Helper()
	players, results := fixture(t)
	for _, month := range []time.Month{time.July, time.August} {
		first := wordle.PuzzleForDate(time.Date(2026, month, 1, 0, 0, 0, 0, time.Local))
		last := wordle.PuzzleForDate(time.Date(2026, month+1, 1, 0, 0, 0, 0, time.Local)) - 1
		g := 3
		if month == time.July {
			g = 4
		}
		results = append(results, play(t, alma.ID, first, last, g, 0)...)
		results = append(results, play(t, bo.ID, first, last, 4, first+3)...)
	}
	results = append(results, store.BoardResult{PlayerID: bo.ID, PuzzleNo: 1800,
		Date: time.Date(2026, 5, 29, 0, 0, 0, 0, time.Local), Guesses: 1, Solved: true})
	return players, results
}

func TestAProfile(t *testing.T) {
	t.Parallel()
	players, results := seasonFixture(t)
	now := fixtureNow()
	tests := []struct {
		loc  string
		req  Request
		want string
	}{
		{"en", Request{Kind: KindProfile, Player: "Alma"},
			"👤 Alma — Fourward: at least two results in five are 4s.\n" +
				"All time: place 1 of 2, 3.40 on average over 77 games.\n" +
				"September: place 1 of 2, 3.00.\n" +
				"Streak: 15 now, 15 at best.\n" +
				"Scores: 0×1, 0×2, 46×3, 31×4, 0×5, 0×6, 0×X.\n" +
				"Form: 3.00 over the last 30 puzzles.\n" +
				"🏆 Alma: 2 monthly wins."},
		{"sv", Request{Kind: KindProfile, Player: "Bo"},
			"👤 Bo — Ren svit: de senaste tio dagarna är lösta utan luckor.\n" +
				"Totalt: plats 2 av 2, 4,08 i snitt på 78 spel.\n" +
				"September: plats 2 av 2, 4,20.\n" +
				"Svit: 10 i rad nu, 10 som bäst.\n" +
				"Resultat: 1×1, 0×2, 0×3, 74×4, 0×5, 0×6, 3×X.\n" +
				"Form: 4,10 de senaste 30 pusslen.\n" +
				"🏆 Bo: 1 månadsvinst."},
		{"en", Request{Kind: KindProfile, Player: "Cid"}, "Cid Larsson hasn't posted a result yet."},
		{"sv", Request{Kind: KindHistory, Player: "Alma"},
			"📚 Alma, månad för månad:\n" +
				"september 3,00 (plats 1) · augusti 3,00 (plats 1) 🏆 · juli 4,00 (plats 1) 🏆\n" +
				"Bästa månaden: september, 3,00."},
		{"en", Request{Kind: KindHistory, Player: "Cid"}, "Cid Larsson hasn't been ranked in any month yet."},
		{"sv", Request{Kind: KindRecords},
			"📖 Rekorden:\n" +
				"📉 Bästa månaden någonsin: Alma, 3,00 i augusti.\n" +
				"🤏 Jämnaste avgörandet: juli, med 10 punkter.\n" +
				"🔥 Längsta sviten någonsin: Alma, 15 dagar.\n" +
				"🎯 Flest 1:or: Bo, 1.\n" +
				"🏆 Flest månadsvinster: Alma, 2."},
		{"en", Request{Kind: KindGroup},
			"👥 2 players, 155 results over 78 days.\n" +
				"Group average 3.74 all time. In September: 3.60.\n" +
				"All scores: 1×1, 0×2, 46×3, 105×4, 0×5, 0×6, 3×X."},
	}
	for _, tc := range tests {
		if got := answer(translator(t, tc.loc), tc.req, nil, players, results, now); got != tc.want {
			t.Errorf("%+v:\ngot  %q\nwant %q", tc.req, got, tc.want)
		}
	}
}

// With only the month in progress there is no finished month to hold a
// record, and the records say what there is.
func TestRecordsWithoutAFinishedMonth(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	got := answer(translator(t, "en"), Request{Kind: KindRecords}, nil, players, results, fixtureNow())
	want := "📖 The records:\n🔥 Longest streak ever: Alma, 15 days."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
