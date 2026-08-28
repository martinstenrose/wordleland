package stats

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// today fixes the clock so the form window is deterministic.
func today(t *testing.T) time.Time {
	t.Helper()
	d, err := wordle.DateForPuzzle(1900)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}
	return d
}

func player(id int64, slug string) store.Player {
	return store.Player{ID: id, Slug: slug, Name: slug, Active: true}
}

// result builds one result. guesses of 0 means failed.
func result(playerID int64, puzzle, guesses int, hardMode bool) store.BoardResult {
	date, _ := wordle.DateForPuzzle(puzzle)
	return store.BoardResult{
		PlayerID: playerID, PuzzleNo: puzzle, Date: date,
		Guesses: guesses, Solved: guesses > 0, HardMode: hardMode,
	}
}

// run builds a consecutive run of results at a fixed score.
func run(playerID, from, to, guesses int, hardMode bool) []store.BoardResult {
	var out []store.BoardResult
	for p := from; p <= to; p++ {
		out = append(out, result(int64(playerID), p, guesses, hardMode))
	}
	return out
}

func find(t *testing.T, b Board, slug string) Player {
	t.Helper()
	for _, p := range append(append([]Player{}, b.Ranked...), b.Unranked...) {
		if p.Slug == slug {
			return p
		}
	}
	t.Fatalf("player %q is not on the board", slug)
	return Player{}
}

func deref(t *testing.T, v *float64) float64 {
	t.Helper()
	if v == nil {
		t.Fatal("value is nil, want a number")
	}
	return *v
}

// X counts as 7 by default, and the 7 never reaches storage.
func TestCountXAsSeven(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "martin")}
	// Four solved in 4, one failure.
	results := append(run(1, 1890, 1893, 4, false), result(1, 1894, 0, false))

	on := Compute(players, results, Options{CountXAsSeven: true, Now: now})
	// (4+4+4+4+7)/5 = 4.6
	if got := deref(t, find(t, on, "martin").Average); got != 4.6 {
		t.Errorf("average with X as 7 = %v, want 4.6", got)
	}

	off := Compute(players, results, Options{CountXAsSeven: false, Now: now})
	// The failure is excluded rather than scored: (4+4+4+4)/4 = 4
	if got := deref(t, find(t, off, "martin").Average); got != 4 {
		t.Errorf("average with X excluded = %v, want 4", got)
	}
}

// Missed days count only between a player's first and last result, or
// anyone who joined late or dropped off gets a meaningless number.
func TestCountMissedIsBoundedToTheActiveWindow(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "martin")}
	// Played 1890 and 1893, missing 1891 and 1892. Nothing before or after.
	results := []store.BoardResult{
		result(1, 1890, 3, false),
		result(1, 1893, 3, false),
	}

	off := Compute(players, results, Options{CountXAsSeven: true, Now: now})
	if got := deref(t, find(t, off, "martin").Average); got != 3 {
		t.Errorf("average with missed off = %v, want 3", got)
	}

	on := Compute(players, results, Options{CountXAsSeven: true, CountMissed: true, Now: now})
	// Two played at 3, two missed at 7: (3+3+7+7)/4 = 5
	if got := deref(t, find(t, on, "martin").Average); got != 5 {
		t.Errorf("average with missed on = %v, want 5 — only the two inside the window count", got)
	}

	// The seven puzzles between 1893 and today are outside the window and
	// must not appear, or the number would be far worse than 5.
	if find(t, on, "martin").Games != 2 {
		t.Errorf("games = %d, want the count of real games only", find(t, on, "martin").Games)
	}
}

// Both a failure and a missed day break a streak.
func TestStreaksBreakOnFailuresAndMisses(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "martin")}

	tests := []struct {
		name        string
		results     []store.BoardResult
		wantCurrent int
		wantLongest int
	}{
		{
			name:        "unbroken to today",
			results:     run(1, 1891, 1900, 3, false),
			wantCurrent: 10, wantLongest: 10,
		},
		{
			name: "a failure breaks it",
			results: append(append(run(1, 1891, 1895, 3, false),
				result(1, 1896, 0, false)), run(1, 1897, 1900, 3, false)...),
			wantCurrent: 4, wantLongest: 5,
		},
		{
			name: "a missed day breaks it",
			results: append(run(1, 1891, 1895, 3, false),
				run(1, 1897, 1900, 3, false)...),
			wantCurrent: 4, wantLongest: 5,
		},
		{
			// Nothing today, so nothing is running.
			name:        "stopped playing",
			results:     run(1, 1880, 1889, 3, false),
			wantCurrent: 0, wantLongest: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := Compute(players, tt.results, DefaultOptions(now))
			p := find(t, b, "martin")
			if p.CurrentStreak != tt.wantCurrent {
				t.Errorf("current = %d, want %d", p.CurrentStreak, tt.wantCurrent)
			}
			if p.LongestStreak != tt.wantLongest {
				t.Errorf("longest = %d, want %d", p.LongestStreak, tt.wantLongest)
			}
		})
	}
}

// Form needs ten games in the window, or it is undefined rather than
// computed from a handful.
func TestFormNeedsTenGames(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "martin")}

	nine := Compute(players, run(1, 1892, 1900, 3, false), DefaultOptions(now))
	p := find(t, nine, "martin")
	if p.Form != nil {
		t.Errorf("form = %v with nine games, want it undefined", *p.Form)
	}
	if p.Delta != nil {
		t.Error("delta is set without a form to derive it from")
	}

	ten := Compute(players, run(1, 1891, 1900, 3, false), DefaultOptions(now))
	if find(t, ten, "martin").Form == nil {
		t.Error("form is undefined with ten games")
	}
}

// The eligibility ladder, and the reasons must not depend on any toggle.
func TestEligibilityLadder(t *testing.T) {
	now := today(t)

	tests := []struct {
		name    string
		results []store.BoardResult
		want    string
	}{
		{name: "no games at all", results: nil, want: ReasonInactive},
		{
			name:    "played, but nothing recent",
			results: run(1, 1800, 1830, 3, false),
			want:    ReasonNoRecentGames,
		},
		{
			name:    "a handful of recent games",
			results: run(1, 1896, 1900, 3, false),
			want:    ReasonLowData,
		},
		{
			name:    "ranked",
			results: run(1, 1885, 1900, 3, false),
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := Compute([]store.Player{player(1, "martin")}, tt.results, DefaultOptions(now))
			p := find(t, b, "martin")
			if p.Reason != tt.want {
				t.Errorf("reason = %q, want %q", p.Reason, tt.want)
			}
			if (p.Rank > 0) != (tt.want == "") {
				t.Errorf("rank = %d with reason %q", p.Rank, p.Reason)
			}
		})
	}
}

// The board sorts on the average,, rather than on form as the design
// does. Lower is better.
func TestRankedByAverageAscending(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "best"), player(2, "middle"), player(3, "worst")}
	var results []store.BoardResult
	results = append(results, run(1, 1885, 1900, 3, false)...)
	results = append(results, run(2, 1885, 1900, 4, false)...)
	results = append(results, run(3, 1885, 1900, 5, false)...)

	b := Compute(players, results, DefaultOptions(now))
	if len(b.Ranked) != 3 {
		t.Fatalf("ranked %d players, want 3", len(b.Ranked))
	}
	for i, want := range []string{"best", "middle", "worst"} {
		if b.Ranked[i].Slug != want {
			t.Errorf("position %d = %q, want %q", i+1, b.Ranked[i].Slug, want)
		}
		if b.Ranked[i].Rank != i+1 {
			t.Errorf("rank = %d at position %d", b.Ranked[i].Rank, i+1)
		}
	}
}

// The toggles have to change the ordering, which is presumably why they
// exist. A player who fails often should place worse once X counts as 7.
func TestTogglesChangeTheOrdering(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "steady"), player(2, "erratic")}

	var results []store.BoardResult
	// Steady: always 4.
	results = append(results, run(1, 1885, 1900, 4, false)...)
	// Erratic: brilliant when they solve, but fails often enough that
	// counting X as 7 outweighs it. Nine 2s and seven failures averages
	// 4.19 with the toggle on and 2.00 with it off, either side of steady's
	// flat 4.
	results = append(results, run(2, 1885, 1893, 2, false)...)
	for p := 1894; p <= 1900; p++ {
		results = append(results, result(2, p, 0, false))
	}

	withSeven := Compute(players, results, Options{CountXAsSeven: true, Now: now})
	withoutSeven := Compute(players, results, Options{CountXAsSeven: false, Now: now})

	if withSeven.Ranked[0].Slug != "steady" {
		t.Errorf("with X as 7 the leader is %q, want steady", withSeven.Ranked[0].Slug)
	}
	if withoutSeven.Ranked[0].Slug != "erratic" {
		t.Errorf("with X excluded the leader is %q, want erratic", withoutSeven.Ranked[0].Slug)
	}
}

// realisticRoster mirrors the actual distribution: a third of the roster
// plays hard mode near-exclusively, the rest never touch it. A roster with
// hard mode spread evenly would surface neither of the two problems below.
func realisticRoster(t *testing.T) ([]store.Player, []store.BoardResult) {
	t.Helper()

	var (
		players []store.Player
		results []store.BoardResult
	)
	// Four hard-mode players, near-exclusively but not entirely.
	for i := 1; i <= 4; i++ {
		players = append(players, player(int64(i), "hard"+string(rune('a'+i-1))))
		for p := 1871; p <= 1900; p++ {
			// One ordinary game in the middle of an otherwise hard-mode run.
			results = append(results, result(int64(i), p, 3, p != 1885))
		}
	}
	// Eight who never play hard mode.
	for i := 5; i <= 12; i++ {
		players = append(players, player(int64(i), "normal"+string(rune('a'+i-5))))
		results = append(results, run(i, 1871, 1900, 4, false)...)
	}
	return players, results
}

// Under mode=hard the eight who never play it have no games in the filtered
// set. The ladder's first rung is "no games at all", so they would all be
// labelled inactive — which defines as having left the group. Someone
// with 30 games would carry a reason chip that is simply untrue.
func TestHardModeFilterDoesNotMislabelPlayersAsInactive(t *testing.T) {
	now := today(t)
	players, results := realisticRoster(t)

	b := Compute(players, results, Options{CountXAsSeven: true, HardModeOnly: true, Now: now})

	for _, p := range append(append([]Player{}, b.Ranked...), b.Unranked...) {
		if p.Reason == ReasonInactive {
			t.Errorf("%s is labelled %q under the hard-mode filter", p.Slug, p.Reason)
		}
	}

	// They are absent from the board entirely rather than ranked low.
	if len(b.Ranked)+len(b.Unranked) != 4 {
		t.Errorf("board lists %d players under the filter, want the 4 who play hard mode",
			len(b.Ranked)+len(b.Unranked))
	}
	if b.ExcludedByFilter != 8 {
		t.Errorf("ExcludedByFilter = %d, want 8 so the omission is visible", b.ExcludedByFilter)
	}
}

// A reason has to mean the same thing whatever the toggles say, or the chip
// is describing the filter rather than the player.
func TestReasonsAreIndependentOfTheFilter(t *testing.T) {
	now := today(t)
	players, results := realisticRoster(t)
	// One hard-mode player who stopped long ago, so a real reason exists
	// under both views.
	players = append(players, player(20, "lapsed"))
	results = append(results, run(20, 1800, 1830, 3, true)...)

	unfiltered := Compute(players, results, Options{CountXAsSeven: true, Now: now})
	filtered := Compute(players, results, Options{CountXAsSeven: true, HardModeOnly: true, Now: now})

	want := find(t, unfiltered, "lapsed").Reason
	if got := find(t, filtered, "lapsed").Reason; got != want {
		t.Errorf("reason = %q under the filter and %q without it", got, want)
	}
	if want != ReasonNoRecentGames {
		t.Errorf("reason = %q, want %q", want, ReasonNoRecentGames)
	}
}

// Filtering manufactures absences. A player's ordinary game mid-run is not
// a day they failed to turn up, so their streak must survive mode=hard.
func TestStreaksSurviveTheHardModeFilter(t *testing.T) {
	now := today(t)
	players, results := realisticRoster(t)

	unfiltered := Compute(players, results, Options{CountXAsSeven: true, Now: now})
	filtered := Compute(players, results, Options{CountXAsSeven: true, HardModeOnly: true, Now: now})

	before := find(t, unfiltered, "harda")
	after := find(t, filtered, "harda")

	if before.CurrentStreak != 30 {
		t.Fatalf("current streak = %d before filtering, want 30", before.CurrentStreak)
	}
	if after.CurrentStreak != before.CurrentStreak {
		t.Errorf("current streak = %d under the filter, want %d — the ordinary game "+
			"mid-run is not a missed day", after.CurrentStreak, before.CurrentStreak)
	}
	if after.LongestStreak != before.LongestStreak {
		t.Errorf("longest streak = %d under the filter, want %d",
			after.LongestStreak, before.LongestStreak)
	}
}

// The same reasoning for missed days: filtering must not invent them.
func TestMissedDaysAreNotManufacturedByTheFilter(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "martin")}
	// Ten consecutive games, one of them ordinary.
	var results []store.BoardResult
	for p := 1891; p <= 1900; p++ {
		results = append(results, result(1, p, 3, p != 1895))
	}

	opts := Options{CountXAsSeven: true, CountMissed: true, HardModeOnly: true, Now: now}
	b := Compute(players, results, opts)
	p := find(t, b, "martin")

	// Nine hard-mode games, all solved in 3. If the ordinary game were read
	// as a miss it would be scored 7 and drag the average above 3.
	if got := deref(t, p.Average); got != 3 {
		t.Errorf("average = %v under the filter with missed days on, want 3 — "+
			"the ordinary game is not a day they skipped", got)
	}
}

// Filtering changes the population, not the arithmetic: a 4 counts as 4
// whichever mode it was played in.
func TestHardModeDoesNotWeightScores(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "martin")}
	var results []store.BoardResult
	for p := 1891; p <= 1900; p++ {
		results = append(results, result(1, p, 4, p%2 == 0))
	}

	unfiltered := Compute(players, results, DefaultOptions(now))
	filtered := Compute(players, results, Options{CountXAsSeven: true, HardModeOnly: true, Now: now})

	if got := deref(t, find(t, unfiltered, "martin").Average); got != 4 {
		t.Errorf("unfiltered average = %v, want 4", got)
	}
	if got := deref(t, find(t, filtered, "martin").Average); got != 4 {
		t.Errorf("filtered average = %v, want 4 — the same scores, a smaller set", got)
	}
	if find(t, filtered, "martin").Games != 5 {
		t.Errorf("filtered games = %d, want 5", find(t, filtered, "martin").Games)
	}
}

// A day nobody has played yet is not a missed day. Until somebody files,
// the streak they had yesterday still stands — reporting a long run as "—"
// all morning is simply wrong.
func TestTodayDoesNotBreakAStreakUntilItIsFailed(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "alma")}

	// Solved every day up to yesterday, nothing filed for today yet.
	pending := run(1, 1871, 1899, 4, false)
	board := Compute(players, pending, DefaultOptions(now))
	if got := find(t, board, "alma").CurrentStreak; got != 29 {
		t.Errorf("streak = %d with today unplayed, want the 29 already standing", got)
	}

	// Filing today extends it.
	extended := append(append([]store.BoardResult{}, pending...), result(1, 1900, 3, false))
	board = Compute(players, extended, DefaultOptions(now))
	if got := find(t, board, "alma").CurrentStreak; got != 30 {
		t.Errorf("streak = %d after playing today, want 30", got)
	}

	// Failing today does break it.
	failed := append(append([]store.BoardResult{}, pending...), result(1, 1900, 0, false))
	board = Compute(players, failed, DefaultOptions(now))
	if got := find(t, board, "alma").CurrentStreak; got != 0 {
		t.Errorf("streak = %d after failing today, want 0", got)
	}

	// And a genuine gap still breaks it: yesterday missed, today unplayed.
	gapped := run(1, 1871, 1898, 4, false)
	board = Compute(players, gapped, DefaultOptions(now))
	if got := find(t, board, "alma").CurrentStreak; got != 0 {
		t.Errorf("streak = %d with yesterday missed, want 0", got)
	}
}
