package ingest

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// applyDB is a migrated database with one admin to attribute writes to.
//
// These tests use a real store rather than a fake. The rules being checked
// here — precedence, holding, reactivation — are agreements between this
// package and the schema, and a fake would let both drift together while
// the tests stayed green.
func applyDB(t *testing.T) (*sql.DB, store.Actor) {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(ctx, db, store.Migrations()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	admin, _ := store.CreateUser(ctx, db, store.SystemActor(), "martin@example.tld", "hash", true)
	return db, store.AdminActor(admin.ID)
}

func mustPlayer(t *testing.T, db *sql.DB, actor store.Actor, name, slug string) store.Player {
	t.Helper()
	p, err := store.CreatePlayer(context.Background(), db, actor, name, slug)
	if err != nil {
		t.Fatalf("CreatePlayer(%s): %v", slug, err)
	}
	return p
}

// retire marks a player as having left the group.
func retire(t *testing.T, db *sql.DB, actor store.Actor, playerID int64) {
	t.Helper()
	inactive := false
	if _, err := store.UpdatePlayer(context.Background(), db, actor, playerID,
		store.PlayerUpdate{Active: &inactive}); err != nil {
		t.Fatalf("retire player: %v", err)
	}
}

func submission(slug string, puzzle, guesses int) Submission {
	return Submission{Slug: slug, PuzzleNo: puzzle, Solved: true, Guesses: &guesses}
}

// A slug or an id that names nobody is the caller's mistake, not a sender
// we have yet to meet: nothing is stored and nothing is held.
func TestApplyRefusesAnUnknownPlayer(t *testing.T) {
	db, actor := applyDB(t)
	ctx := context.Background()

	if _, err := Apply(ctx, db, actor, submission("nobody", 1500, 4), true); !errors.Is(err, ErrNoSuchPlayer) {
		t.Errorf("unknown slug: err = %v, want ErrNoSuchPlayer", err)
	}
	missing := int64(9999)
	sub := Submission{PlayerID: &missing, PuzzleNo: 1500, Solved: true, Guesses: ptr(4)}
	if _, err := Apply(ctx, db, actor, sub, true); !errors.Is(err, ErrNoSuchPlayer) {
		t.Errorf("unknown id: err = %v, want ErrNoSuchPlayer", err)
	}

	held, err := store.ListPendingSenders(ctx, db)
	if err != nil {
		t.Fatalf("PendingIdentities: %v", err)
	}
	if len(held) != 0 {
		t.Errorf("a mistyped name was held as an unclaimed sender: %d entries", len(held))
	}
}

// A sender nobody has claimed is held rather than refused: nothing is lost,
// and the result becomes real when somebody claims them.
func TestApplyHoldsAnUnclaimedSender(t *testing.T) {
	db, actor := applyDB(t)
	ctx := context.Background()

	sub := Submission{
		Source: "signal", ExternalID: "uuid-1", DisplayHint: "M.",
		PuzzleNo: 1500, Solved: true, Guesses: ptr(3), Via: "signal",
	}
	result, err := Apply(ctx, db, actor, sub, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Status != StatusPending {
		t.Errorf("status = %q, want %q", result.Status, StatusPending)
	}

	held, err := store.ListPendingSenders(ctx, db)
	if err != nil {
		t.Fatalf("PendingIdentities: %v", err)
	}
	if len(held) != 1 || held[0].ExternalID != "uuid-1" {
		t.Fatalf("held = %+v, want one entry for uuid-1", held)
	}
}

// write() must resolve a sender-named submission inside its own transaction
// rather than trust a player resolved earlier: an `identity reassign` could
// commit in the window between that earlier resolution and this write, and a
// write that trusted it would land on whoever the sender used to map to.
// Simulated here by calling write() directly with a's Player value — the one
// applyFromSender would have resolved before the reassignment below — for a
// submission that now maps to b.
func TestWriteResolvesSenderInsideItsOwnTransaction(t *testing.T) {
	db, actor := applyDB(t)
	ctx := context.Background()

	a := mustPlayer(t, db, actor, "Alice", "alice")
	b := mustPlayer(t, db, actor, "Bob", "bob")
	if _, err := store.LinkIdentity(ctx, db, actor, a.ID, "signal", "uuid-1", "claim", false); err != nil {
		t.Fatalf("LinkIdentity: %v", err)
	}
	if _, err := store.ReassignIdentity(ctx, db, actor, "signal", "uuid-1", b.ID, false, false); err != nil {
		t.Fatalf("ReassignIdentity: %v", err)
	}

	sub := Submission{Source: "signal", ExternalID: "uuid-1", PuzzleNo: 1500, Solved: true, Guesses: ptr(4)}
	res, err := write(ctx, db, actor, a, sub, true)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.PlayerID != b.ID {
		t.Errorf("PlayerID = %d, want %d (b) — a stale pre-resolved player must not be trusted", res.PlayerID, b.ID)
	}

	if _, err := store.ResultFor(ctx, db, 1500, a.ID); !errors.Is(err, store.ErrResultNotFound) {
		t.Error("the result landed on the stale, pre-reassignment player")
	}
	if _, err := store.ResultFor(ctx, db, 1500, b.ID); err != nil {
		t.Errorf("the result did not land on the current player: %v", err)
	}
}

// A human-entered value beats an automated one. The bridge must not be able
// to undo a correction an admin typed.
func TestApplyDoesNotOverwriteAHumanEntry(t *testing.T) {
	db, actor := applyDB(t)
	ctx := context.Background()
	player := mustPlayer(t, db, actor, "Martin", "martin")

	// An admin files 3 by hand: entered_by is set.
	date, _ := wordle.DateForPuzzle(1500)
	guesses := 3
	entered := actor.UserID
	handEntered := store.Result{
		PuzzleNo: 1500, Date: date, PlayerID: player.ID,
		Guesses: &guesses, Solved: true,
	}
	if _, _, err := store.UpsertResult(ctx, db, handEntered, entered, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}

	// The bridge then reports 5 for the same puzzle.
	result, err := Apply(ctx, db, store.SystemActor(), submission("martin", 1500, 5), true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Status != StatusIgnored {
		t.Errorf("status = %q, want %q — an automated write overwrote a hand-entered one", result.Status, StatusIgnored)
	}

	results, err := store.ResultsForBoard(ctx, db)
	if err != nil {
		t.Fatalf("ResultsForBoard: %v", err)
	}
	if len(results) != 1 || results[0].Guesses != 3 {
		t.Errorf("stored %+v, want the hand-entered 3 to stand", results)
	}
}

// Reactivation happens on a live post only. A replay or a backfill is
// historical and says nothing about whether somebody has rejoined.
//
// The sender path is the one that carries the question at all: naming a
// player directly never reactivates, whatever the caller passes.
func TestApplyReactivatesOnlyOnALivePost(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name          string
		mayReactivate bool
		wantActive    bool
	}{
		{"live post", true, true},
		{"replay or backfill", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, actor := applyDB(t)
			player := mustPlayer(t, db, actor, "Martin", "martin")
			if _, err := store.LinkIdentity(ctx, db, actor, player.ID, "signal", "uuid-1", "claim", false); err != nil {
				t.Fatalf("LinkIdentity: %v", err)
			}
			retire(t, db, actor, player.ID)

			sub := Submission{
				Source: "signal", ExternalID: "uuid-1",
				PuzzleNo: 1500, Solved: true, Guesses: ptr(4), Via: "signal",
			}
			if _, err := Apply(ctx, db, actor, sub, tt.mayReactivate); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			after, err := store.PlayerBySlug(ctx, db, "martin")
			if err != nil {
				t.Fatalf("PlayerBySlug: %v", err)
			}
			if after.Active != tt.wantActive {
				t.Errorf("active = %v, want %v", after.Active, tt.wantActive)
			}
		})
	}
}

// Naming a player directly never reactivates, whatever the caller passes:
// an admin or a script filing a score says nothing about membership.
func TestApplyByNameNeverReactivates(t *testing.T) {
	db, actor := applyDB(t)
	ctx := context.Background()
	player := mustPlayer(t, db, actor, "Martin", "martin")
	retire(t, db, actor, player.ID)

	sub := Submission{PlayerID: &player.ID, PuzzleNo: 1500, Solved: true, Guesses: ptr(4)}
	if _, err := Apply(ctx, db, actor, sub, true); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	after, err := store.PlayerBySlug(ctx, db, "martin")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}
	if after.Active {
		t.Error("naming a player by id brought them back into the group")
	}
}

// The activity log records how a result arrived, which is what lets it
// tell a bridge write from a hand-entered one.
func TestApplyRecordsHowTheResultArrived(t *testing.T) {
	db, actor := applyDB(t)
	ctx := context.Background()
	mustPlayer(t, db, actor, "Martin", "martin")

	sub := submission("martin", 1500, 4)
	sub.Via = "signal"
	if _, err := Apply(ctx, db, store.SystemActor(), sub, true); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	events, _, err := store.ListActivity(ctx, db, store.ActivityResults, 10)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("filing a result logged nothing")
	}
	if !strings.Contains(events[0].Detail, `"via":"signal"`) {
		t.Errorf("detail = %s, want it to record via=signal", events[0].Detail)
	}
}

// The posting time travels with the submission: onto the result when the
// sender is known, and into the held row when they are not, so a claim
// later replays it. A submission without one leaves the result unstamped
// rather than borrowing the time of entry.
func TestApplyCarriesThePostingTime(t *testing.T) {
	db, actor := applyDB(t)
	ctx := context.Background()
	alice := mustPlayer(t, db, actor, "Alice", "alice")

	posted := time.Date(2026, time.August, 23, 6, 12, 0, 0, time.Local)
	sub := submission("alice", 1890, 4)
	sub.PostedAt = &posted
	if _, err := Apply(ctx, db, actor, sub, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	stored, err := store.ResultFor(ctx, db, 1890, alice.ID)
	if err != nil {
		t.Fatalf("ResultFor: %v", err)
	}
	if stored.PostedAt == nil || !stored.PostedAt.Equal(posted) {
		t.Errorf("posted_at = %v, want %v", stored.PostedAt, posted)
	}

	if _, err := Apply(ctx, db, actor, submission("alice", 1891, 4), false); err != nil {
		t.Fatalf("Apply without a time: %v", err)
	}
	stored, err = store.ResultFor(ctx, db, 1891, alice.ID)
	if err != nil {
		t.Fatalf("ResultFor: %v", err)
	}
	if stored.PostedAt != nil {
		t.Errorf("posted_at = %v for a submission without one, want nil", stored.PostedAt)
	}

	// Unknown sender: held, then replayed with the time intact.
	held := Submission{Source: "signal", ExternalID: "uuid-new", PuzzleNo: 1890, Solved: true,
		Guesses: ptr(5), PostedAt: &posted}
	res, err := Apply(ctx, db, actor, held, false)
	if err != nil {
		t.Fatalf("Apply for an unknown sender: %v", err)
	}
	if res.Status != StatusPending {
		t.Fatalf("status = %q, want pending", res.Status)
	}
	bob := mustPlayer(t, db, actor, "Bob", "bob")
	if _, err := store.LinkIdentity(ctx, db, actor, bob.ID, "signal", "uuid-new",
		store.ActionIdentityClaimed, false); err != nil {
		t.Fatalf("LinkIdentity: %v", err)
	}
	stored, err = store.ResultFor(ctx, db, 1890, bob.ID)
	if err != nil {
		t.Fatalf("ResultFor after the claim: %v", err)
	}
	if stored.PostedAt == nil || !stored.PostedAt.Equal(posted) {
		t.Errorf("replayed posted_at = %v, want %v", stored.PostedAt, posted)
	}
}
