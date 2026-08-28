package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func totpFixture(t *testing.T) (*sql.DB, int64, Actor) {
	t.Helper()
	db := migratedDB(t)
	_, actor := adminFixture(t, db)
	user, err := CreateUser(context.Background(), db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	return db, user.ID, actor
}

// A mis-scanned QR code must not lock anyone out, so the secret stays pending
// until a code proves the phone holds the same one.
func TestPendingSecretIsNotLive(t *testing.T) {
	db, userID, _ := totpFixture(t)
	ctx := context.Background()

	if err := SetPendingTOTPSecret(ctx, db, userID, []byte("sealed-secret")); err != nil {
		t.Fatalf("SetPendingTOTPSecret() failed: %v", err)
	}

	if _, err := TOTPSecret(ctx, db, userID); !errors.Is(err, ErrNoTOTPSecret) {
		t.Errorf("the pending secret became live immediately: %v", err)
	}
	got, err := PendingTOTPSecret(ctx, db, userID)
	if err != nil {
		t.Fatalf("PendingTOTPSecret() failed: %v", err)
	}
	if string(got) != "sealed-secret" {
		t.Errorf("pending secret = %q, want the stored value", got)
	}

	// HasTOTP drives whether login demands a second step, so it must stay
	// false until enrolment completes.
	user, err := UserByID(ctx, db, userID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if user.HasTOTP {
		t.Error("HasTOTP = true while enrolment is only pending")
	}
}

func TestPromotePendingSecret(t *testing.T) {
	db, userID, actor := totpFixture(t)
	ctx := context.Background()

	if err := SetPendingTOTPSecret(ctx, db, userID, []byte("sealed-secret")); err != nil {
		t.Fatalf("SetPendingTOTPSecret() failed: %v", err)
	}
	if err := PromotePendingTOTPSecret(ctx, db, actor, userID, 12345); err != nil {
		t.Fatalf("PromotePendingTOTPSecret() failed: %v", err)
	}

	live, err := TOTPSecret(ctx, db, userID)
	if err != nil {
		t.Fatalf("TOTPSecret() failed: %v", err)
	}
	if string(live) != "sealed-secret" {
		t.Errorf("live secret = %q, want the promoted value", live)
	}
	if _, err := PendingTOTPSecret(ctx, db, userID); !errors.Is(err, ErrNoPendingSecret) {
		t.Errorf("the pending secret survived promotion: %v", err)
	}

	user, err := UserByID(ctx, db, userID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if !user.HasTOTP {
		t.Error("HasTOTP = false after enrolment completed")
	}

	// The confirming step is recorded at promotion, so the code just used
	// cannot immediately be replayed against the new secret.
	if err := RecordTOTPStep(ctx, db, userID, 12345); !errors.Is(err, ErrCodeReplayed) {
		t.Errorf("the enrolling code could be replayed: %v", err)
	}
}

func TestPromoteWithoutPendingSecret(t *testing.T) {
	db, userID, actor := totpFixture(t)

	if err := PromotePendingTOTPSecret(context.Background(), db, actor, userID, 1); !errors.Is(err, ErrNoPendingSecret) {
		t.Errorf("error = %v, want ErrNoPendingSecret", err)
	}
}

// : a code from a step already accepted is rejected, so an observed
// code cannot be reused inside its thirty-second window.
func TestRecordTOTPStepRejectsReplay(t *testing.T) {
	db, userID, _ := totpFixture(t)
	ctx := context.Background()

	if err := RecordTOTPStep(ctx, db, userID, 100); err != nil {
		t.Fatalf("first use failed: %v", err)
	}
	if err := RecordTOTPStep(ctx, db, userID, 100); !errors.Is(err, ErrCodeReplayed) {
		t.Errorf("the same step was accepted twice: %v", err)
	}
	// An earlier step is refused too: accepting one would let a code
	// captured a minute ago be used after a newer one.
	if err := RecordTOTPStep(ctx, db, userID, 99); !errors.Is(err, ErrCodeReplayed) {
		t.Errorf("an earlier step was accepted: %v", err)
	}
	if err := RecordTOTPStep(ctx, db, userID, 101); err != nil {
		t.Errorf("the next step was refused: %v", err)
	}
}

// The comparison lives in the UPDATE so two simultaneous submissions of the
// same code cannot both pass a check-then-write.
func TestRecordTOTPStepIsAtomic(t *testing.T) {
	db, userID, _ := totpFixture(t)
	ctx := context.Background()

	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- RecordTOTPStep(ctx, db, userID, 500) }()
	}

	var accepted, replayed int
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, ErrCodeReplayed):
			replayed++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if accepted != 1 || replayed != 1 {
		t.Errorf("accepted=%d replayed=%d, want exactly one of each", accepted, replayed)
	}
}

func TestClearPendingTOTPSecret(t *testing.T) {
	db, userID, _ := totpFixture(t)
	ctx := context.Background()

	if err := SetPendingTOTPSecret(ctx, db, userID, []byte("sealed")); err != nil {
		t.Fatalf("SetPendingTOTPSecret() failed: %v", err)
	}
	if err := ClearPendingTOTPSecret(ctx, db, userID); err != nil {
		t.Fatalf("ClearPendingTOTPSecret() failed: %v", err)
	}
	if _, err := PendingTOTPSecret(ctx, db, userID); !errors.Is(err, ErrNoPendingSecret) {
		t.Errorf("error = %v, want ErrNoPendingSecret", err)
	}
}

// ResetUserTOTP must clear everything, so an admin reset genuinely returns the
// account to un-enrolled rather than leaving a usable secret behind.
func TestResetClearsEnrolmentEntirely(t *testing.T) {
	db, userID, actor := totpFixture(t)
	ctx := context.Background()

	if err := SetPendingTOTPSecret(ctx, db, userID, []byte("sealed")); err != nil {
		t.Fatalf("SetPendingTOTPSecret() failed: %v", err)
	}
	if err := PromotePendingTOTPSecret(ctx, db, actor, userID, 100); err != nil {
		t.Fatalf("PromotePendingTOTPSecret() failed: %v", err)
	}
	if err := ResetUserTOTP(ctx, db, actor, userID); err != nil {
		t.Fatalf("ResetUserTOTP() failed: %v", err)
	}

	if _, err := TOTPSecret(ctx, db, userID); !errors.Is(err, ErrNoTOTPSecret) {
		t.Errorf("the live secret survived a reset: %v", err)
	}
	if _, err := PendingTOTPSecret(ctx, db, userID); !errors.Is(err, ErrNoPendingSecret) {
		t.Errorf("a pending secret survived a reset: %v", err)
	}
	// The step is cleared too, so a fresh enrolment is not blocked by a step
	// recorded against the old secret.
	if err := RecordTOTPStep(ctx, db, userID, 50); err != nil {
		t.Errorf("the step counter survived a reset: %v", err)
	}
}
