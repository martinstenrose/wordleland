package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/martinstenrose/wordleland/internal/auth"
)

func recoveryUser(t *testing.T, db *sql.DB) User {
	t.Helper()
	user, err := CreateUser(context.Background(), db, SystemActor(), "martin@example.tld", "hash", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

// A set is issued whole, in plaintext, exactly once.
func TestReplaceRecoveryCodesIssuesAFullSet(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	user := recoveryUser(t, db)

	codes, err := ReplaceRecoveryCodes(ctx, db, AdminActor(user.ID), user.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}
	if len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("issued %d codes, want %d", len(codes), auth.RecoveryCodeCount)
	}

	n, err := CountRecoveryCodes(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CountRecoveryCodes: %v", err)
	}
	if n != auth.RecoveryCodeCount {
		t.Errorf("%d codes unused, want %d", n, auth.RecoveryCodeCount)
	}

	// Only hashes are stored: a database read must not yield a working code.
	for _, code := range codes {
		var found int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM totp_recovery_codes WHERE code_hash = ?`, code).Scan(&found); err != nil {
			t.Fatalf("query: %v", err)
		}
		if found != 0 {
			t.Fatalf("code %q is stored in plaintext", code)
		}
	}
}

// A code works once, and the grouping dashes a person reads off paper are
// not part of what has to match.
func TestConsumeRecoveryCodeIsSingleUse(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	user := recoveryUser(t, db)

	codes, err := ReplaceRecoveryCodes(ctx, db, AdminActor(user.ID), user.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}

	if err := ConsumeRecoveryCode(ctx, db, user.ID, "  "+codes[0]+" "); err != nil {
		t.Fatalf("first use rejected: %v", err)
	}
	if err := ConsumeRecoveryCode(ctx, db, user.ID, codes[0]); !errors.Is(err, ErrNoRecoveryCode) {
		t.Errorf("second use = %v, want ErrNoRecoveryCode", err)
	}

	n, _ := CountRecoveryCodes(ctx, db, user.ID)
	if n != auth.RecoveryCodeCount-1 {
		t.Errorf("%d codes left, want %d", n, auth.RecoveryCodeCount-1)
	}
}

// One account's codes must not open another's.
func TestConsumeRecoveryCodeIsScopedToTheUser(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	mine := recoveryUser(t, db)
	theirs, err := CreateUser(ctx, db, SystemActor(), "other@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	codes, err := ReplaceRecoveryCodes(ctx, db, AdminActor(mine.ID), mine.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}

	if err := ConsumeRecoveryCode(ctx, db, theirs.ID, codes[0]); !errors.Is(err, ErrNoRecoveryCode) {
		t.Errorf("another account's code was accepted: %v", err)
	}
	// And it is still unspent for the account it belongs to.
	if err := ConsumeRecoveryCode(ctx, db, mine.ID, codes[0]); err != nil {
		t.Errorf("the owner's code was consumed by the failed attempt: %v", err)
	}
}

// Regenerating revokes what came before. A set written down and then
// replaced has to stop working immediately, not once it is used.
func TestReplaceRecoveryCodesRevokesTheOldSet(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	user := recoveryUser(t, db)

	old, err := ReplaceRecoveryCodes(ctx, db, AdminActor(user.ID), user.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}
	fresh, err := ReplaceRecoveryCodes(ctx, db, AdminActor(user.ID), user.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}

	if err := ConsumeRecoveryCode(ctx, db, user.ID, old[0]); !errors.Is(err, ErrNoRecoveryCode) {
		t.Errorf("a revoked code still works: %v", err)
	}
	if err := ConsumeRecoveryCode(ctx, db, user.ID, fresh[0]); err != nil {
		t.Errorf("a fresh code was rejected: %v", err)
	}
	if n, _ := CountRecoveryCodes(ctx, db, user.ID); n != auth.RecoveryCodeCount-1 {
		t.Errorf("%d codes left, want %d — the old set was not cleared", n, auth.RecoveryCodeCount-1)
	}
}

// Nonsense is refused rather than matching an empty hash.
func TestConsumeRecoveryCodeRejectsRubbish(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	user := recoveryUser(t, db)

	if _, err := ReplaceRecoveryCodes(ctx, db, AdminActor(user.ID), user.ID); err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}
	for _, typed := range []string{"", "   ", "----", "not-a-code"} {
		if err := ConsumeRecoveryCode(ctx, db, user.ID, typed); !errors.Is(err, ErrNoRecoveryCode) {
			t.Errorf("ConsumeRecoveryCode(%q) = %v, want ErrNoRecoveryCode", typed, err)
		}
	}
}

// Resetting an enrolment has to take the codes with it: one minted against
// the old secret is a way straight past the new one.
func TestDiscardRecoveryCodes(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	user := recoveryUser(t, db)

	codes, err := ReplaceRecoveryCodes(ctx, db, AdminActor(user.ID), user.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}
	if err := DiscardRecoveryCodes(ctx, db, user.ID); err != nil {
		t.Fatalf("DiscardRecoveryCodes: %v", err)
	}
	if n, _ := CountRecoveryCodes(ctx, db, user.ID); n != 0 {
		t.Errorf("%d codes survived the reset", n)
	}
	if err := ConsumeRecoveryCode(ctx, db, user.ID, codes[0]); !errors.Is(err, ErrNoRecoveryCode) {
		t.Errorf("a discarded code still works: %v", err)
	}
}

// Re-enrolling invalidates the previous set. Somebody replacing their
// two-factor because it was compromised means the codes as well.
func TestReEnrolmentRevokesRecoveryCodes(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	user := recoveryUser(t, db)
	actor := AdminActor(user.ID)

	if err := SetPendingTOTPSecret(ctx, db, user.ID, []byte("first")); err != nil {
		t.Fatalf("SetPendingTOTPSecret: %v", err)
	}
	if err := PromotePendingTOTPSecret(ctx, db, actor, user.ID, 1); err != nil {
		t.Fatalf("PromotePendingTOTPSecret: %v", err)
	}
	codes, err := ReplaceRecoveryCodes(ctx, db, actor, user.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}

	// Enrol again, without reaching the page that issues a new set.
	if err := SetPendingTOTPSecret(ctx, db, user.ID, []byte("second")); err != nil {
		t.Fatalf("SetPendingTOTPSecret: %v", err)
	}
	if err := PromotePendingTOTPSecret(ctx, db, actor, user.ID, 2); err != nil {
		t.Fatalf("PromotePendingTOTPSecret: %v", err)
	}

	if n, _ := CountRecoveryCodes(ctx, db, user.ID); n != 0 {
		t.Errorf("%d codes survived re-enrolment", n)
	}
	if err := ConsumeRecoveryCode(ctx, db, user.ID, codes[0]); !errors.Is(err, ErrNoRecoveryCode) {
		t.Errorf("a code from the old enrolment still works: %v", err)
	}
}

// A CLI reset takes the codes with it, for the same reason.
func TestResetUserTOTPRevokesRecoveryCodes(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	user := recoveryUser(t, db)
	actor := AdminActor(user.ID)

	if err := SetPendingTOTPSecret(ctx, db, user.ID, []byte("secret")); err != nil {
		t.Fatalf("SetPendingTOTPSecret: %v", err)
	}
	if err := PromotePendingTOTPSecret(ctx, db, actor, user.ID, 1); err != nil {
		t.Fatalf("PromotePendingTOTPSecret: %v", err)
	}
	codes, err := ReplaceRecoveryCodes(ctx, db, actor, user.ID)
	if err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}

	if err := ResetUserTOTP(ctx, db, actor, user.ID); err != nil {
		t.Fatalf("ResetUserTOTP: %v", err)
	}
	if n, _ := CountRecoveryCodes(ctx, db, user.ID); n != 0 {
		t.Errorf("%d codes survived the reset", n)
	}
	if err := ConsumeRecoveryCode(ctx, db, user.ID, codes[0]); !errors.Is(err, ErrNoRecoveryCode) {
		t.Errorf("a code survived the reset: %v", err)
	}
}
