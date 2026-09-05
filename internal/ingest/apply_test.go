package ingest

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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
	if _, _, err := store.UpsertResult(ctx, db, handEntered, entered); err != nil {
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

// The audit trail records how a result arrived, which is what lets the
// activity log tell a bridge write from a hand-entered one.
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
