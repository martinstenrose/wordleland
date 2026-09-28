package store

import (
	"context"
	"testing"
)

// The summary counts solved results by guesses, and a miss in none of them:
// the sign-in page draws the group's distribution from it, and a miss drawn
// as a seventh guess would be a guess nobody made.
func TestGroupSummaryCountsGuesses(t *testing.T) {
	t.Parallel()

	db := migratedDB(t)
	a, b := seedPlayer(t, db, "anna"), seedPlayer(t, db, "bert")
	for _, r := range []struct {
		player  int64
		puzzle  int
		guesses int
		solved  bool
	}{
		{a, 1200, 3, true}, {b, 1200, 3, true},
		{a, 1201, 4, true}, {b, 1201, 0, false},
		{a, 1202, 2, true},
	} {
		// A miss has no guess count: the schema keeps it NULL.
		var guesses any = r.guesses
		if !r.solved {
			guesses = nil
		}
		if _, err := db.Exec(`INSERT INTO results (puzzle_no, date, player_id, guesses, solved) VALUES (?, '2025-01-01', ?, ?, ?)`,
			r.puzzle, r.player, guesses, r.solved); err != nil {
			t.Fatal(err)
		}
	}

	got, err := GroupSummary(context.Background(), db, 1202)
	if err != nil {
		t.Fatal(err)
	}
	if want := [6]int{0, 1, 2, 1, 0, 0}; got.Distribution != want {
		t.Errorf("Distribution = %v, want %v", got.Distribution, want)
	}
	if got.Games != 5 {
		t.Errorf("Games = %d, want 5, the miss included", got.Games)
	}
}
