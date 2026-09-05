package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func resultsFixture(t *testing.T) (*sql.DB, int64, int64, Actor) {
	t.Helper()
	db := migratedDB(t)
	adminID, actor := adminFixture(t, db)
	player, err := CreatePlayer(context.Background(), db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	return db, player.ID, adminID, actor
}

func sampleResult(playerID int64, puzzle, guesses int) Result {
	return Result{
		PuzzleNo: puzzle,
		Date:     time.Date(2026, time.August, 23, 0, 0, 0, 0, time.Local),
		PlayerID: playerID,
		Guesses:  ptr(guesses),
		Solved:   true,
	}
}

func TestUpsertResultCreates(t *testing.T) {
	db, playerID, _, _ := resultsFixture(t)
	ctx := context.Background()

	outcome, previous, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 4), nil)
	if err != nil {
		t.Fatalf("UpsertResult() failed: %v", err)
	}
	if outcome != OutcomeCreated {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeCreated)
	}
	if previous != nil {
		t.Error("previous is set for a new row")
	}
}

// A token write may overwrite another token write: automated corrections of
// automated values are how a repost fixes a typo.
func TestTokenWriteOverwritesTokenWrite(t *testing.T) {
	db, playerID, _, _ := resultsFixture(t)
	ctx := context.Background()

	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 5), nil); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	outcome, previous, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 3), nil)
	if err != nil {
		t.Fatalf("second write failed: %v", err)
	}
	if outcome != OutcomeUpdated {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeUpdated)
	}
	if previous == nil || *previous.Guesses != 5 {
		t.Errorf("previous = %+v, want the earlier value", previous)
	}

	stored, err := ResultFor(ctx, db, 1890, playerID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if *stored.Guesses != 3 {
		t.Errorf("guesses = %d, want 3", *stored.Guesses)
	}
}

// The rule exists to protect: a human's value always wins, because
// replaying old Signal history would otherwise silently revert every
// correction ever made.
func TestTokenWriteCannotOverwriteHumanEntry(t *testing.T) {
	db, playerID, adminID, _ := resultsFixture(t)
	ctx := context.Background()

	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 3), &adminID); err != nil {
		t.Fatalf("human write failed: %v", err)
	}

	outcome, previous, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 6), nil)
	if err != nil {
		t.Fatalf("token write failed: %v", err)
	}
	if outcome != OutcomeIgnored {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeIgnored)
	}
	if previous == nil {
		t.Fatal("previous is nil for a refused write")
	}

	// The stored row is untouched, including its attribution.
	stored, err := ResultFor(ctx, db, 1890, playerID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if *stored.Guesses != 3 {
		t.Errorf("guesses = %d, want the human value 3", *stored.Guesses)
	}
	if stored.EnteredBy == nil || *stored.EnteredBy != adminID {
		t.Errorf("entered_by = %v, want %d", stored.EnteredBy, adminID)
	}
}

// A human write is never refused, whatever is already there.
func TestHumanWriteOverwritesAnything(t *testing.T) {
	db, playerID, adminID, _ := resultsFixture(t)
	ctx := context.Background()

	for _, existing := range []*int64{nil, &adminID} {
		if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 5), existing); err != nil {
			t.Fatalf("seed write failed: %v", err)
		}
		outcome, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 2), &adminID)
		if err != nil {
			t.Fatalf("human write failed: %v", err)
		}
		if outcome != OutcomeUpdated {
			t.Errorf("outcome = %q, want %q", outcome, OutcomeUpdated)
		}
	}
}

func TestUpsertResultStoresFailure(t *testing.T) {
	db, playerID, _, _ := resultsFixture(t)
	ctx := context.Background()

	failed := sampleResult(playerID, 1890, 0)
	failed.Solved = false
	failed.Guesses = nil

	if _, _, err := UpsertResult(ctx, db, failed, nil); err != nil {
		t.Fatalf("UpsertResult() failed: %v", err)
	}
	stored, err := ResultFor(ctx, db, 1890, playerID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if stored.Solved || stored.Guesses != nil {
		t.Errorf("stored = %+v, want a failure with no guess count", stored)
	}
}

func TestUpsertResultCarriesHardMode(t *testing.T) {
	db, playerID, _, _ := resultsFixture(t)
	ctx := context.Background()

	hard := sampleResult(playerID, 1890, 4)
	hard.HardMode = true
	if _, _, err := UpsertResult(ctx, db, hard, nil); err != nil {
		t.Fatalf("UpsertResult() failed: %v", err)
	}

	stored, err := ResultFor(ctx, db, 1890, playerID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if !stored.HardMode {
		t.Error("hard_mode was not stored")
	}
}

// A missed day is the absence of a row, so unsetting deletes rather than
// blanking. The CLI notes the consequence, which the next test pins.
func TestDeleteResult(t *testing.T) {
	db, playerID, adminID, actor := resultsFixture(t)
	ctx := context.Background()

	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 4), &adminID); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	if err := DeleteResult(ctx, db, actor, playerID, 1890); err != nil {
		t.Fatalf("DeleteResult() failed: %v", err)
	}
	if _, err := ResultFor(ctx, db, 1890, playerID); !errors.Is(err, ErrResultNotFound) {
		t.Errorf("error = %v, want ErrResultNotFound", err)
	}

	// The row is gone, so the activity entry is the only record of what it held.
	var detail string
	if err := db.QueryRow(`SELECT detail FROM activity_log WHERE action = ?`, ActionResultDeleted).Scan(&detail); err != nil {
		t.Fatalf("read activity detail: %v", err)
	}
	if detail == "" {
		t.Error("the deletion was logged without the value it removed")
	}
}

// Deleting frees the slot: entered_by no longer protects it, so a later token
// write is accepted. Stated and worth pinning, since it is the one way
// a human's value stops winning.
func TestDeleteResultFreesThePrecedenceLock(t *testing.T) {
	db, playerID, adminID, actor := resultsFixture(t)
	ctx := context.Background()

	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 3), &adminID); err != nil {
		t.Fatalf("human write failed: %v", err)
	}
	if outcome, _, _ := UpsertResult(ctx, db, sampleResult(playerID, 1890, 6), nil); outcome != OutcomeIgnored {
		t.Fatalf("outcome = %q before deletion, want %q", outcome, OutcomeIgnored)
	}

	if err := DeleteResult(ctx, db, actor, playerID, 1890); err != nil {
		t.Fatalf("DeleteResult() failed: %v", err)
	}

	outcome, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 6), nil)
	if err != nil {
		t.Fatalf("token write failed: %v", err)
	}
	if outcome != OutcomeCreated {
		t.Errorf("outcome = %q after deletion, want %q", outcome, OutcomeCreated)
	}
}

func TestDeleteResultNotFound(t *testing.T) {
	db, playerID, _, actor := resultsFixture(t)

	if err := DeleteResult(context.Background(), db, actor, playerID, 1890); !errors.Is(err, ErrResultNotFound) {
		t.Errorf("error = %v, want ErrResultNotFound", err)
	}
}
