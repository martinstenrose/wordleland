package store

import (
	"context"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/wordle"
)

// The page's first question is whether results are still arriving, so an
// empty database must answer it without pretending.
func TestFreshnessOnAnEmptyDatabase(t *testing.T) {
	f, err := ReadFreshness(context.Background(), migratedDB(t))
	if err != nil {
		t.Fatalf("ReadFreshness: %v", err)
	}
	if !f.LastResultAt.IsZero() {
		t.Errorf("LastResultAt = %v, want zero", f.LastResultAt)
	}
	if f.LatestPuzzle != 0 || f.PendingSenders != 0 || f.PendingResults != 0 {
		t.Errorf("freshness = %+v, want all zero", f)
	}
}

// Arrival time comes from the activity log rather than the puzzle's date: a
// backfill of last month is a result arriving now, and reading the date
// would report the board as untouched for weeks.
func TestFreshnessReadsArrivalNotPuzzleDate(t *testing.T) {
	ctx := context.Background()
	db, playerID, _, actor := resultsFixture(t)

	before := time.Now().Add(-time.Minute)

	// An old puzzle, written now.
	old, err := wordle.DateForPuzzle(1500)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}
	guesses := 4
	r := Result{PuzzleNo: 1500, Date: old, PlayerID: playerID, Guesses: &guesses, Solved: true}
	if _, _, err := UpsertResult(ctx, db, r, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}
	if err := LogResultActivity(ctx, db, actor, ActionResultCreated, playerID, r, nil); err != nil {
		t.Fatalf("LogResultActivity: %v", err)
	}

	f, err := ReadFreshness(ctx, db)
	if err != nil {
		t.Fatalf("ReadFreshness: %v", err)
	}
	if f.LastResultAt.Before(before) {
		t.Errorf("LastResultAt = %v, want the arrival time rather than the puzzle's date %v",
			f.LastResultAt, old)
	}
}

// A bridge working perfectly against senders nobody has claimed looks
// healthy and puts nothing on the board, so the count has to be visible.
func TestFreshnessCountsHeldResults(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)

	for _, sender := range []string{"sender-a", "sender-b"} {
		for _, puzzle := range []int{1400, 1401} {
			g := 4
			if err := HoldPendingResult(ctx, db, "signal", sender, "",
				PendingResult{PuzzleNo: puzzle, Solved: true, Guesses: &g}); err != nil {
				t.Fatalf("HoldPendingResult: %v", err)
			}
		}
	}

	f, err := ReadFreshness(ctx, db)
	if err != nil {
		t.Fatalf("ReadFreshness: %v", err)
	}
	if f.PendingSenders != 2 {
		t.Errorf("PendingSenders = %d, want 2", f.PendingSenders)
	}
	if f.PendingResults != 4 {
		t.Errorf("PendingResults = %d, want 4", f.PendingResults)
	}
}
