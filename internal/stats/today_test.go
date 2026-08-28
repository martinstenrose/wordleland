package stats

import (
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
