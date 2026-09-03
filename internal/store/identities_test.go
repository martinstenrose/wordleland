package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

const testUUID = "11111111-2222-3333-4444-555555555555"

func identityFixture(t *testing.T) (*sql.DB, int64, int64, Actor) {
	t.Helper()
	return resultsFixture(t)
}

func holdResult(t *testing.T, db *sql.DB, puzzle int, guesses int, hardMode bool) {
	t.Helper()
	r := PendingResult{PuzzleNo: puzzle, Solved: true, Guesses: ptr(guesses), HardMode: hardMode}
	if err := HoldPendingResult(context.Background(), db, "signal", testUUID, "Someone", r); err != nil {
		t.Fatalf("HoldPendingResult() failed: %v", err)
	}
}

func TestResolveIdentityNotFound(t *testing.T) {
	db, _, _, _ := identityFixture(t)

	_, err := ResolveIdentity(context.Background(), db, "signal", testUUID)
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("error = %v, want ErrIdentityNotFound", err)
	}
}

func TestListPendingSendersAggregates(t *testing.T) {
	db, _, _, _ := identityFixture(t)

	holdResult(t, db, 1888, 4, false)
	holdResult(t, db, 1889, 3, true)
	holdResult(t, db, 1890, 5, false)

	senders, err := ListPendingSenders(context.Background(), db)
	if err != nil {
		t.Fatalf("ListPendingSenders() failed: %v", err)
	}
	if len(senders) != 1 {
		t.Fatalf("senders = %d, want 1 aggregated row", len(senders))
	}
	if senders[0].Count != 3 {
		t.Errorf("count = %d, want 3", senders[0].Count)
	}
	if senders[0].DisplayHint != "Someone" {
		t.Errorf("display hint = %q, want the last one seen", senders[0].DisplayHint)
	}
}

// Claiming recovers everything that arrived while the sender was unclaimed —
// the whole reason pending_results holds payloads rather than a counter.
func TestLinkIdentityReplaysHeldResults(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1888, 4, false)
	holdResult(t, db, 1889, 3, true)

	summary, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false)
	if err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if summary.Replayed != 2 || summary.Skipped != 0 {
		t.Errorf("summary = %+v, want 2 replayed and none skipped", summary)
	}

	for _, tc := range []struct {
		puzzle   int
		guesses  int
		hardMode bool
	}{{1888, 4, false}, {1889, 3, true}} {
		stored, err := ResultFor(ctx, db, tc.puzzle, playerID)
		if err != nil {
			t.Fatalf("puzzle %d was not replayed: %v", tc.puzzle, err)
		}
		if *stored.Guesses != tc.guesses || stored.HardMode != tc.hardMode {
			t.Errorf("puzzle %d = %+v, want guesses %d hard %v", tc.puzzle, stored, tc.guesses, tc.hardMode)
		}
		// Replayed rows originated from a token, so they must stay
		// overwritable by a later token write.
		if stored.EnteredBy != nil {
			t.Errorf("puzzle %d has entered_by set; replayed rows must carry NULL", tc.puzzle)
		}
		if stored.Date.IsZero() {
			t.Errorf("puzzle %d has no derived date", tc.puzzle)
		}
	}

	// The held rows are consumed.
	senders, err := ListPendingSenders(ctx, db)
	if err != nil {
		t.Fatalf("ListPendingSenders() failed: %v", err)
	}
	if len(senders) != 0 {
		t.Errorf("senders = %d after claiming, want 0", len(senders))
	}
}

// A replayed result must never overwrite something entered by hand, and the
// held row is still consumed — leaving it would keep a claimed sender in the
// pending list forever with something that can never apply.
func TestLinkIdentityRespectsPrecedence(t *testing.T) {
	db, playerID, adminID, actor := identityFixture(t)
	ctx := context.Background()

	// A correction already exists for one of the puzzles.
	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1888, 2), &adminID); err != nil {
		t.Fatalf("human write failed: %v", err)
	}
	holdResult(t, db, 1888, 4, false)
	holdResult(t, db, 1889, 3, false)

	summary, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false)
	if err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if summary.Replayed != 1 || summary.Skipped != 1 {
		t.Errorf("summary = %+v, want 1 replayed and 1 skipped", summary)
	}

	// The hand-entered value survived untouched.
	stored, err := ResultFor(ctx, db, 1888, playerID)
	if err != nil {
		t.Fatalf("ResultFor() failed: %v", err)
	}
	if *stored.Guesses != 2 {
		t.Errorf("guesses = %d, want the hand-entered 2", *stored.Guesses)
	}
	if stored.EnteredBy == nil || *stored.EnteredBy != adminID {
		t.Errorf("entered_by = %v, want it unchanged", stored.EnteredBy)
	}

	// Both held rows are gone, including the refused one.
	var held int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_results`).Scan(&held); err != nil {
		t.Fatalf("count held: %v", err)
	}
	if held != 0 {
		t.Errorf("held results remaining = %d, want 0 including the skipped one", held)
	}
}

// A crash midway must leave nothing: an identity that exists with results
// half-replayed cannot be recovered by re-running, because claiming refuses a
// sender that already resolves.
func TestLinkIdentityIsAtomic(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1888, 4, false)
	holdResult(t, db, 1889, 3, false)

	// A puzzle number past the sanity bound makes date derivation fail
	// partway through the replay loop, after the identity row was written.
	if err := HoldPendingResult(ctx, db, "signal", testUUID, "Someone",
		PendingResult{PuzzleNo: 999999, Solved: true, Guesses: ptr(3)}); err != nil {
		t.Fatalf("HoldPendingResult() failed: %v", err)
	}

	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false); err == nil {
		t.Fatal("LinkIdentity() succeeded despite an unusable held result")
	}

	if _, err := ResolveIdentity(ctx, db, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
		t.Error("the identity row survived a failed claim")
	}
	var results, held int
	if err := db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&results); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_results`).Scan(&held); err != nil {
		t.Fatalf("count held: %v", err)
	}
	if results != 0 {
		t.Errorf("results written = %d, want 0", results)
	}
	if held != 3 {
		t.Errorf("held results = %d, want all 3 still there to retry", held)
	}
}

func TestLinkIdentityDryRun(t *testing.T) {
	db, playerID, adminID, actor := identityFixture(t)
	ctx := context.Background()

	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1888, 2), &adminID); err != nil {
		t.Fatalf("human write failed: %v", err)
	}
	holdResult(t, db, 1888, 4, false)
	holdResult(t, db, 1889, 3, false)

	summary, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, true)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if summary.Replayed != 1 || summary.Skipped != 1 {
		t.Errorf("summary = %+v, want the same counts a real run would report", summary)
	}

	// Nothing was written.
	if _, err := ResolveIdentity(ctx, db, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
		t.Error("the dry run created an identity")
	}
	if _, err := ResultFor(ctx, db, 1889, playerID); !errors.Is(err, ErrResultNotFound) {
		t.Error("the dry run wrote a result")
	}
	var held int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_results`).Scan(&held); err != nil {
		t.Fatalf("count held: %v", err)
	}
	if held != 2 {
		t.Errorf("held results = %d after a dry run, want 2", held)
	}
}

func TestLinkIdentityRejectsAlreadyClaimed(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false); !errors.Is(err, ErrIdentityTaken) {
		t.Errorf("error = %v, want ErrIdentityTaken", err)
	}
}

// Adding an identity directly must replay too, or held results are orphaned
// with nothing left to link them.
func TestLinkIdentityWithoutHeldResults(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	summary, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false)
	if err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if summary.Replayed != 0 {
		t.Errorf("summary = %+v, want nothing replayed", summary)
	}
	if _, err := ResolveIdentity(ctx, db, "signal", testUUID); err != nil {
		t.Errorf("the identity was not created: %v", err)
	}
}

func TestDiscardPendingResults(t *testing.T) {
	db, _, _, actor := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1888, 4, false)
	holdResult(t, db, 1889, 3, false)

	discarded, err := DiscardPendingResults(ctx, db, actor, "signal", testUUID)
	if err != nil {
		t.Fatalf("DiscardPendingResults() failed: %v", err)
	}
	if discarded != 2 {
		t.Errorf("discarded = %d, want 2", discarded)
	}

	// No player, no identity, no results: just gone.
	if _, err := ResolveIdentity(ctx, db, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
		t.Error("discarding created an identity")
	}
	var results int
	if err := db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&results); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if results != 0 {
		t.Errorf("results = %d, want 0", results)
	}
}

// Zero retention means unlimited, so nothing is purged regardless of age;
// past the window, a held result is dropped.
func TestDeleteExpiredPendingResults(t *testing.T) {
	db, _, _, _ := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1900, 4, false)
	if _, err := db.ExecContext(ctx,
		`UPDATE pending_results SET received_at = ? WHERE puzzle_no = 1900`,
		time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatalf("backdate pending result: %v", err)
	}

	if n, err := DeleteExpiredPendingResults(ctx, db, 0); err != nil {
		t.Fatalf("DeleteExpiredPendingResults() failed: %v", err)
	} else if n != 0 {
		t.Errorf("purged %d with zero (unlimited) retention, want 0", n)
	}

	n, err := DeleteExpiredPendingResults(ctx, db, 24*time.Hour)
	if err != nil {
		t.Fatalf("DeleteExpiredPendingResults() failed: %v", err)
	}
	if n != 1 {
		t.Errorf("purged = %d, want 1", n)
	}

	var held int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_results`).Scan(&held); err != nil {
		t.Fatalf("count pending results: %v", err)
	}
	if held != 0 {
		t.Errorf("pending_results still has %d rows after purge", held)
	}
}

func TestDiscardPendingResultsNothingHeld(t *testing.T) {
	db, _, _, actor := identityFixture(t)

	if _, err := DiscardPendingResults(context.Background(), db, actor, "signal", testUUID); !errors.Is(err, ErrNoPendingResults) {
		t.Errorf("error = %v, want ErrNoPendingResults", err)
	}
}

// The hint is cosmetic, so a sender renaming themselves must not disturb the
// mapping — that is why resolution uses the UUID.
func TestRefreshDisplayHint(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if err := RefreshDisplayHint(ctx, db, "signal", testUUID, "New Name"); err != nil {
		t.Fatalf("RefreshDisplayHint() failed: %v", err)
	}

	var hint string
	if err := db.QueryRow(
		`SELECT COALESCE(display_hint, '') FROM player_identities WHERE external_id = ?`, testUUID).Scan(&hint); err != nil {
		t.Fatalf("read hint: %v", err)
	}
	if hint != "New Name" {
		t.Errorf("display_hint = %q, want %q", hint, "New Name")
	}

	// Still resolves to the same player.
	player, err := ResolveIdentity(ctx, db, "signal", testUUID)
	if err != nil {
		t.Fatalf("ResolveIdentity() failed: %v", err)
	}
	if player.ID != playerID {
		t.Errorf("player = %d, want %d", player.ID, playerID)
	}
}

func TestHoldPendingResultOverwritesRepost(t *testing.T) {
	db, _, _, _ := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1890, 5, false)
	holdResult(t, db, 1890, 3, true)

	held, _, err := pendingResultsFor(ctx, db, "signal", testUUID)
	if err != nil {
		t.Fatalf("pendingResultsFor() failed: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1 after a repost", len(held))
	}
	if *held[0].Guesses != 3 || !held[0].HardMode {
		t.Errorf("held = %+v, want the repost to have won", held[0])
	}
}
