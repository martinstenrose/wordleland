package reply

import (
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// A made-up result is scored into the month as the player's next day, with
// everyone else as they stand. The fixture: Alma on 3.00 over 15 days, Bo
// on 4.20 with an X among his.
func TestWhatIf(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	// Close: Alma 3s, Bo 3s but a 4 today, so 3.07.
	close := play(t, alma.ID, first, current, 3, 0)
	close = append(close, play(t, bo.ID, first, current-1, 3, 0)...)
	close = append(close, play(t, bo.ID, current, current, 4, 0)...)
	// Through yesterday only: today is still open to everyone.
	yesterday := append(play(t, alma.ID, first, current-1, 3, 0), play(t, bo.ID, first, current-1, 4, 0)...)

	tests := []struct {
		name    string
		req     Request
		results []store.BoardResult
		want    string
	}{
		{"the leader keeps it", Request{Kind: KindWhatIf, Scores: []Hypothetical{{"Alma", 7}, {"Bo", 1}}}, results,
			"🔮 If Alma gets an X and Bo gets a 1 tomorrow:\n" +
				"Alma still leads on 3.25, 75 points ahead of Bo (4.00).\n" +
				"Everyone else as they stand now."},
		{"the lead changes hands", Request{Kind: KindWhatIf, Scores: []Hypothetical{{"alma", 7}, {"Bo", 1}}}, close,
			"🔮 If Alma gets an X and Bo gets a 1 tomorrow:\n" +
				"Bo takes the lead on 2.94, 31 points ahead of Alma (3.25). Plot twist.\n" +
				"Everyone else as they stand now."},
		{"today, before anyone has played it", Request{Kind: KindWhatIf, Scores: []Hypothetical{{"Bo", 2}}}, yesterday,
			"🔮 If Bo gets a 2 today:\n" +
				"Alma still leads on 3.00, 87 points ahead of Bo (3.87).\n" +
				"Everyone else as they stand now."},
		{"today, already played", Request{Kind: KindWhatIf, Date: "2026-09-15", Scores: []Hypothetical{{"Bo", 2}}}, results,
			"Bo has already played today — that result stands."},
		{"a day gone", Request{Kind: KindWhatIf, Date: "2026-09-10", Scores: []Hypothetical{{"Bo", 2}}}, results,
			"That day has been played already"},
		{"next month", Request{Kind: KindWhatIf, Date: "2026-10-01", Scores: []Hypothetical{{"Bo", 2}}}, results,
			"That's next month — September is settled before then."},
		{"on the last day", Request{Kind: KindWhatIf, Date: "2026-09-30", Scores: []Hypothetical{{"Bo", 1}}}, results,
			"🔮 If Bo gets a 1 on 30 September:\n" +
				"Alma still leads on 3.00, 100 points ahead of Bo (4.00).\n" +
				"Everyone else as they stand now. It's the month's last day."},
		{"somebody nobody knows", Request{Kind: KindWhatIf, Scores: []Hypothetical{{"Zed", 2}}}, results,
			"I don't know Zed."},
		{"no results given", Request{Kind: KindWhatIf}, results, "Give me results to try"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := answer(translator(t, "en"), tc.req, nil, players, tc.results, now)
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// A third player the made-up results move is placed on a line of their
// own; in Swedish, the scores read as the group says them.
func TestWhatIfPlacesEveryNamedPlayer(t *testing.T) {
	t.Parallel()
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	results := play(t, alma.ID, first, current, 3, 0)
	results = append(results, play(t, bo.ID, first, current, 4, 0)...)
	results = append(results, play(t, cid.ID, first, current, 5, 0)...)

	got := answer(translator(t, "sv"), Request{Kind: KindWhatIf, Scores: []Hypothetical{{"Cid", 1}}},
		nil, []store.Player{alma, bo, cid}, results, now)
	want := "🔮 Om Cid Larsson får en 1:a i morgon:\n" +
		"Alma leder fortfarande på 3,00, 100 punkter före Bo (4,00).\n" +
		"Cid Larsson landar på 4,75, plats 3.\n" +
		"Övriga som de står nu."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
