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

	_, _, err := ResolveIdentity(context.Background(), db, "signal", testUUID)
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
	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1888, 2), &adminID, nil); err != nil {
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

	if _, _, err := ResolveIdentity(ctx, db, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
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

	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1888, 2), &adminID, nil); err != nil {
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
	if _, _, err := ResolveIdentity(ctx, db, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
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
	if _, _, err := ResolveIdentity(ctx, db, "signal", testUUID); err != nil {
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
	if _, _, err := ResolveIdentity(ctx, db, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
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
	player, _, err := ResolveIdentity(ctx, db, "signal", testUUID)
	if err != nil {
		t.Fatalf("ResolveIdentity() failed: %v", err)
	}
	if player.ID != playerID {
		t.Errorf("player = %d, want %d", player.ID, playerID)
	}
}

func TestListClaimedIdentitiesEmpty(t *testing.T) {
	db, _, _, _ := identityFixture(t)

	claimed, err := ListClaimedIdentities(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("ListClaimedIdentities() failed: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("claimed = %d, want 0", len(claimed))
	}
}

func TestListClaimedIdentitiesScopesToPlayer(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	other, err := CreatePlayer(ctx, db, actor, "Other", "other")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if _, err := LinkIdentity(ctx, db, actor, other.ID, "signal", "22222222-3333-4444-5555-666666666666", ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	all, err := ListClaimedIdentities(ctx, db, nil)
	if err != nil {
		t.Fatalf("ListClaimedIdentities() failed: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %d, want 2", len(all))
	}

	scoped, err := ListClaimedIdentities(ctx, db, &playerID)
	if err != nil {
		t.Fatalf("ListClaimedIdentities(scoped) failed: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ExternalID != testUUID {
		t.Errorf("scoped = %+v, want only %s", scoped, testUUID)
	}
}

// A player can have more than one claimed identity, each writing its own
// results. Reassigning one must move only what it wrote, never results a
// different identity produced for the same player.
func TestReassignIdentityMovesOnlyItsOwnResults(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	target, err := CreatePlayer(ctx, db, actor, "Target", "target")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	const secondUUID = "22222222-3333-4444-5555-666666666666"
	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if err := HoldPendingResult(ctx, db, "signal", secondUUID, "", PendingResult{PuzzleNo: 1889, Solved: true, Guesses: ptr(3)}); err != nil {
		t.Fatalf("HoldPendingResult() failed: %v", err)
	}
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", secondUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	summary, err := ReassignIdentity(ctx, db, actor, "signal", testUUID, target.ID, true, false)
	if err != nil {
		t.Fatalf("ReassignIdentity() failed: %v", err)
	}
	if summary.Moved != 1 || summary.Left != 0 {
		t.Errorf("summary = %+v, want 1 moved and 0 left", summary)
	}

	if _, err := ResultFor(ctx, db, 1888, target.ID); err != nil {
		t.Errorf("puzzle 1888 was not moved to the target: %v", err)
	}
	if _, err := ResultFor(ctx, db, 1889, playerID); err != nil {
		t.Errorf("puzzle 1889 (a different identity's result) should have stayed with the old player: %v", err)
	}
	if _, err := ResultFor(ctx, db, 1889, target.ID); !errors.Is(err, ErrResultNotFound) {
		t.Error("puzzle 1889 was moved to the target, but it belongs to a different identity")
	}

	newPlayer, _, err := ResolveIdentity(ctx, db, "signal", testUUID)
	if err != nil {
		t.Fatalf("ResolveIdentity() failed: %v", err)
	}
	if newPlayer.ID != target.ID {
		t.Errorf("resolved player = %d, want the target %d", newPlayer.ID, target.ID)
	}
}

// A result the target player already has for that puzzle always wins, so
// the moved-from row must stay with the old player rather than being
// dropped or overwriting a hand-entered value.
func TestReassignIdentityLeavesConflictingResults(t *testing.T) {
	db, playerID, adminID, actor := identityFixture(t)
	ctx := context.Background()

	target, err := CreatePlayer(ctx, db, actor, "Target", "target")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if _, _, err := UpsertResult(ctx, db, sampleResult(target.ID, 1888, 6), &adminID, nil); err != nil {
		t.Fatalf("human write failed: %v", err)
	}

	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	summary, err := ReassignIdentity(ctx, db, actor, "signal", testUUID, target.ID, true, false)
	if err != nil {
		t.Fatalf("ReassignIdentity() failed: %v", err)
	}
	if summary.Moved != 0 || summary.Left != 1 {
		t.Errorf("summary = %+v, want 0 moved and 1 left", summary)
	}

	stored, err := ResultFor(ctx, db, 1888, target.ID)
	if err != nil {
		t.Fatalf("ResultFor(target) failed: %v", err)
	}
	if *stored.Guesses != 6 {
		t.Errorf("guesses = %d, want the target's hand-entered 6 to survive", *stored.Guesses)
	}
	if _, err := ResultFor(ctx, db, 1888, playerID); err != nil {
		t.Errorf("the old player's result should have stayed, since it could not move: %v", err)
	}
}

// The mapping still moves even when moveResults is false — only the
// results stay behind.
func TestReassignIdentityWithoutMovingResults(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	target, err := CreatePlayer(ctx, db, actor, "Target", "target")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	summary, err := ReassignIdentity(ctx, db, actor, "signal", testUUID, target.ID, false, false)
	if err != nil {
		t.Fatalf("ReassignIdentity() failed: %v", err)
	}
	if summary.Moved != 0 || summary.Left != 0 {
		t.Errorf("summary = %+v, want no result accounting when moveResults is false", summary)
	}

	newPlayer, _, err := ResolveIdentity(ctx, db, "signal", testUUID)
	if err != nil {
		t.Fatalf("ResolveIdentity() failed: %v", err)
	}
	if newPlayer.ID != target.ID {
		t.Errorf("resolved player = %d, want the target %d", newPlayer.ID, target.ID)
	}
	if _, err := ResultFor(ctx, db, 1888, playerID); err != nil {
		t.Errorf("the result should have stayed with the old player: %v", err)
	}
}

// A result written before results.identity_id existed (or by some other
// automated path that never set it) cannot be safely attributed to this
// identity, so it must be reported rather than silently left behind
// unmentioned or, worse, guessed at and moved.
func TestReassignIdentityReportsUntrackedResults(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	target, err := CreatePlayer(ctx, db, actor, "Target", "target")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	// An automated result with no identity_id at all: pre-migration history,
	// or any other automated write that predates attribution.
	if _, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1700, 5), nil, nil); err != nil {
		t.Fatalf("untracked automated write failed: %v", err)
	}

	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	summary, err := ReassignIdentity(ctx, db, actor, "signal", testUUID, target.ID, true, false)
	if err != nil {
		t.Fatalf("ReassignIdentity() failed: %v", err)
	}
	if summary.Moved != 1 {
		t.Errorf("summary = %+v, want the identity's own result moved", summary)
	}
	if summary.Untracked != 1 {
		t.Errorf("summary = %+v, want the pre-existing untracked result counted", summary)
	}

	// Untracked means untouched, not moved on a guess.
	if _, err := ResultFor(ctx, db, 1700, playerID); err != nil {
		t.Errorf("the untracked result should have stayed with the old player: %v", err)
	}
	if _, err := ResultFor(ctx, db, 1700, target.ID); !errors.Is(err, ErrResultNotFound) {
		t.Error("the untracked result was moved despite no identity to attribute it to")
	}
}

func TestReassignIdentityDryRun(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	target, err := CreatePlayer(ctx, db, actor, "Target", "target")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	summary, err := ReassignIdentity(ctx, db, actor, "signal", testUUID, target.ID, true, true)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if summary.Moved != 1 {
		t.Errorf("summary = %+v, want 1 (would be) moved", summary)
	}

	// Nothing was actually written.
	unchanged, _, err := ResolveIdentity(ctx, db, "signal", testUUID)
	if err != nil {
		t.Fatalf("ResolveIdentity() failed: %v", err)
	}
	if unchanged.ID != playerID {
		t.Errorf("player = %d, want the dry run to have left it at %d", unchanged.ID, playerID)
	}
	if _, err := ResultFor(ctx, db, 1888, target.ID); !errors.Is(err, ErrResultNotFound) {
		t.Error("the dry run moved a result")
	}
}

func TestReassignIdentityRejectsUnclaimed(t *testing.T) {
	db, _, _, actor := identityFixture(t)
	ctx := context.Background()

	target, err := CreatePlayer(ctx, db, actor, "Target", "target")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	if _, err := ReassignIdentity(ctx, db, actor, "signal", testUUID, target.ID, false, false); !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("error = %v, want ErrIdentityNotFound", err)
	}
}

func TestReassignIdentityRejectsSamePlayer(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	if _, err := ReassignIdentity(ctx, db, actor, "signal", testUUID, playerID, false, false); err == nil {
		t.Error("reassigning to the same player should fail")
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

// A held result keeps the time it was posted through the wait, and the
// replay hands it on: a newcomer who posts for a week before being claimed
// was still first or last on those days.
func TestLinkIdentityReplaysThePostingTime(t *testing.T) {
	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	posted := time.Date(2026, time.August, 21, 7, 5, 0, 0, time.Local)
	r := PendingResult{PuzzleNo: 1888, Solved: true, Guesses: ptr(4), PostedAt: &posted}
	if err := HoldPendingResult(ctx, db, "signal", testUUID, "Someone", r); err != nil {
		t.Fatalf("HoldPendingResult() failed: %v", err)
	}
	// A re-post of the same puzzle carries a later time; the first stands.
	repost := PendingResult{PuzzleNo: 1888, Solved: true, Guesses: ptr(3), PostedAt: ptr(posted.Add(time.Hour))}
	if err := HoldPendingResult(ctx, db, "signal", testUUID, "Someone", repost); err != nil {
		t.Fatalf("HoldPendingResult() re-post failed: %v", err)
	}

	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	stored, err := ResultFor(ctx, db, 1888, playerID)
	if err != nil {
		t.Fatalf("ResultFor: %v", err)
	}
	if stored.PostedAt == nil || !stored.PostedAt.Equal(posted) {
		t.Errorf("posted_at = %v, want the first posting %v", stored.PostedAt, posted)
	}
	if *stored.Guesses != 3 {
		t.Errorf("guesses = %d, want the re-post's 3", *stored.Guesses)
	}
}
