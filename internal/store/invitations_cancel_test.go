package store

import (
	"context"
	"errors"
	"testing"
)

// A cancelled invitation stops being pending at once, its link with it, and
// the cancelling is on the record.
func TestCancelInvitationEndsItAndSaysSo(t *testing.T) {
	t.Parallel()

	db, playerID, _, actor := resultsFixture(t)
	ctx := context.Background()

	token, err := CreateInvitation(ctx, db, actor, playerID, "someone@example.tld", "en")
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if err := CancelInvitation(ctx, db, actor, playerID); err != nil {
		t.Fatalf("CancelInvitation: %v", err)
	}
	if _, err := PendingInvitation(ctx, db, playerID); err == nil {
		t.Error("a cancelled invitation is still pending")
	}
	if _, _, err := InvitationByToken(ctx, db, token); err == nil {
		t.Error("a cancelled invitation's link still works")
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_log WHERE action = ?`, ActionInvitationCancelled).Scan(&n); err != nil || n != 1 {
		t.Errorf("cancellations logged = %d (%v), want 1", n, err)
	}
	// Nothing left to cancel is said, not ignored.
	if err := CancelInvitation(ctx, db, actor, playerID); !errors.Is(err, ErrInvitationInvalid) {
		t.Errorf("cancelling again = %v, want ErrInvitationInvalid", err)
	}
}
