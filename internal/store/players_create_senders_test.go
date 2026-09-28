package store

import (
	"context"
	"errors"
	"testing"
)

// A player made with a waiting sender takes that sender's held results onto
// the board in the same breath.
func TestCreatePlayerForSendersReplaysWhatWasHeld(t *testing.T) {
	t.Parallel()

	db, _, _, actor := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1888, 4, false)
	player, summary, err := CreatePlayerForSenders(ctx, db, actor, "Karin", "", []Sender{{"signal", testUUID}})
	if err != nil {
		t.Fatalf("CreatePlayerForSenders: %v", err)
	}
	if player.Slug != "karin" {
		t.Errorf("slug = %q, want it made from the name", player.Slug)
	}
	if summary.Replayed != 1 {
		t.Errorf("replayed %d, want 1", summary.Replayed)
	}
	if _, err := ResultFor(ctx, db, 1888, player.ID); err != nil {
		t.Errorf("the held result is not on the new player: %v", err)
	}
}

// A claim that fails leaves no player behind: the two are one act.
func TestCreatePlayerForSendersIsAllOrNothing(t *testing.T) {
	t.Parallel()

	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false); err != nil {
		t.Fatalf("LinkIdentity: %v", err)
	}
	_, _, err := CreatePlayerForSenders(ctx, db, actor, "Karin", "karin", []Sender{{"signal", testUUID}})
	if !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("claiming a taken sender = %v, want ErrIdentityTaken", err)
	}
	if _, err := PlayerBySlug(ctx, db, "karin"); !errors.Is(err, ErrPlayerNotFound) {
		t.Errorf("the player survived its failed claim: %v", err)
	}

	if _, _, err := CreatePlayerForSenders(ctx, db, actor, "Karin", "Not A Slug", nil); !errors.Is(err, ErrInvalidSlug) {
		t.Errorf("an invalid slug = %v, want ErrInvalidSlug", err)
	}
}
