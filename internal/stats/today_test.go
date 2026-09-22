package stats

import (
	"slices"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

func TestTodaySeparatesFiledFromMissing(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse"), player(3, "cilla")}
	results := []store.BoardResult{
		result(1, 1900, 3, false),
		result(2, 1900, 2, false),
		result(1, 1899, 4, false), // yesterday, irrelevant to today
	}

	today := ComputeToday(players, results, 1900)

	if got := today.FiledCount(); got != 2 {
		t.Errorf("FiledCount() = %d, want 2", got)
	}
	if got := today.Expected(); got != 3 {
		t.Errorf("Expected() = %d, want 3", got)
	}
	if len(today.Missing) != 1 || today.Missing[0].Slug != "cilla" {
		t.Errorf("Missing = %v, want just cilla", today.Missing)
	}
	// Best first.
	if today.Filed[0].Slug != "bosse" {
		t.Errorf("Filed[0] = %s, want bosse with the 2", today.Filed[0].Slug)
	}
	if today.Best == nil || today.Best.Slug != "bosse" {
		t.Errorf("Best = %v, want bosse", today.Best)
	}
}

// A retired player is not expected to play, so listing them as missing
// would be wrong every day forever.
func TestTodayDoesNotExpectRetiredPlayers(t *testing.T) {
	retired := player(2, "gone")
	retired.Active = false
	players := []store.Player{player(1, "alma"), retired}

	today := ComputeToday(players, []store.BoardResult{result(1, 1900, 3, false)}, 1900)

	if len(today.Missing) != 0 {
		t.Errorf("Missing = %v, want nobody", today.Missing)
	}
	if today.Expected() != 1 {
		t.Errorf("Expected() = %d, want 1", today.Expected())
	}
}

// A retired player who does file is still shown: they played, so hiding the
// result would contradict the board.
func TestTodayShowsARetiredPlayerWhoFiles(t *testing.T) {
	retired := player(2, "gone")
	retired.Active = false
	players := []store.Player{player(1, "alma"), retired}
	results := []store.BoardResult{result(1, 1900, 3, false), result(2, 1900, 2, false)}

	today := ComputeToday(players, results, 1900)
	if today.FiledCount() != 2 {
		t.Errorf("FiledCount() = %d, want 2", today.FiledCount())
	}
}

// Failures sort last and never become the day's best.
func TestTodayBestIgnoresFailures(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse")}
	results := []store.BoardResult{
		result(1, 1900, 0, false), // failed
		result(2, 1900, 5, false),
	}

	today := ComputeToday(players, results, 1900)
	if today.Filed[0].Slug != "bosse" {
		t.Errorf("Filed[0] = %s, want the solver first", today.Filed[0].Slug)
	}
	if today.Best == nil || today.Best.Slug != "bosse" {
		t.Errorf("Best = %v, want bosse", today.Best)
	}

	// Nobody solving it means there is no best, rather than a failure
	// being promoted to one.
	onlyFails := ComputeToday(players, []store.BoardResult{result(1, 1900, 0, false)}, 1900)
	if onlyFails.Best != nil {
		t.Errorf("Best = %v, want nil when nobody solved it", onlyFails.Best)
	}
}

// A tie for the day's best is a real outcome; naming one of them would be
// picking a winner the day does not have.
func TestTodayReportsASharedBest(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse"), player(3, "cilla")}
	results := []store.BoardResult{
		result(1, 1900, 2, false),
		result(2, 1900, 2, false),
		result(3, 1900, 4, false),
	}

	today := ComputeToday(players, results, 1900)
	if today.BestShared != 2 {
		t.Errorf("BestShared = %d, want 2", today.BestShared)
	}

	sole := ComputeToday(players, []store.BoardResult{result(1, 1900, 2, false)}, 1900)
	if sole.BestShared != 1 {
		t.Errorf("BestShared = %d for a single filer, want 1", sole.BestShared)
	}
}

// Hard mode breaks a tie and only a tie. Two people on the same score are
// ordered with hard mode first; a better score in normal mode still wins,
// because hard mode orders equal results rather than weighting unequal ones.
func TestTodayPutsHardModeFirstOnATie(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse"), player(3, "cilla")}
	results := []store.BoardResult{
		result(1, 1900, 4, false),
		result(2, 1900, 4, true),
		result(3, 1900, 3, false),
	}

	today := ComputeToday(players, results, 1900)

	// cilla's 3 in normal mode still beats bosse's 4 played hard, and bosse
	// is ahead of alma only because the two of them tie.
	want := []string{"cilla", "bosse", "alma"}
	var got []string
	for _, e := range today.Filed {
		got = append(got, e.Slug)
	}
	if !slices.Equal(got, want) {
		t.Errorf("Filed = %v, want %v", got, want)
	}
	if today.Best == nil || today.Best.Slug != "cilla" {
		t.Errorf("Best = %v, want cilla's 3", today.Best)
	}
}

// Where the day's best is shared across both modes, Best is a hard-mode one,
// so anything naming a single winner names the harder game. The count is
// untouched: both of them hold the score.
func TestTodayBestPrefersHardModeAmongEqualScores(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse")}
	results := []store.BoardResult{
		result(1, 1900, 3, false),
		result(2, 1900, 3, true),
	}

	today := ComputeToday(players, results, 1900)
	if today.Best == nil || today.Best.Slug != "bosse" {
		t.Errorf("Best = %v, want bosse on the hard-mode 3", today.Best)
	}
	if today.BestShared != 2 {
		t.Errorf("BestShared = %d, want 2 \u2014 both of them got it in 3", today.BestShared)
	}
}

// Two failures are the same result as much as two 3s are, so the same
// tiebreak applies at the bottom of the day.
func TestTodayPutsHardModeFirstAmongFailures(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse")}
	results := []store.BoardResult{
		result(1, 1900, 0, false),
		result(2, 1900, 0, true),
	}

	today := ComputeToday(players, results, 1900)
	if today.Filed[0].Slug != "bosse" {
		t.Errorf("Filed[0] = %s, want bosse \u2014 the failure played hard", today.Filed[0].Slug)
	}
}
