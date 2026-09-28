package store

import (
	"context"
	"errors"
	"testing"
)

// Unlinking a sender leaves its results with the player and sends its next
// one back to pending.
func TestUnlinkIdentityHoldsTheSendersNextResult(t *testing.T) {
	t.Parallel()

	db, playerID, _, actor := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1888, 4, false)
	if _, err := LinkIdentity(ctx, db, actor, playerID, "signal", testUUID, ActionIdentityClaimed, false); err != nil {
		t.Fatalf("LinkIdentity: %v", err)
	}
	if err := UnlinkIdentity(ctx, db, actor, playerID, "signal", testUUID); err != nil {
		t.Fatalf("UnlinkIdentity: %v", err)
	}
	if _, _, err := ResolveIdentity(ctx, db, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("the sender still resolves after unlinking: %v", err)
	}
	if _, err := ResultFor(ctx, db, 1888, playerID); err != nil {
		t.Errorf("the sender's filed result went with the link: %v", err)
	}
	if err := UnlinkIdentity(ctx, db, actor, playerID, "signal", testUUID); !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("unlinking again = %v, want ErrIdentityNotFound", err)
	}
}
