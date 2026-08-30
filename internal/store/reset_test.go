package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func resetFixture(t *testing.T) (*sql.DB, User, Actor) {
	t.Helper()
	db := migratedDB(t)
	_, actor := adminFixture(t, db)
	user, err := CreateUser(context.Background(), db, actor, "martin@example.tld", "old-hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	return db, user, actor
}

// Only the hash is stored, so reading the database cannot mint a working link.
func TestResetTokenIsStoredHashed(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	token, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}

	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM password_reset_tokens WHERE user_id = ?`, user.ID).Scan(&stored); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if stored == token {
		t.Fatal("the token was stored in plaintext")
	}
	if stored != HashToken(token) {
		t.Error("the stored value is not the token's hash")
	}
}

func TestConsumeResetToken(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	seedSession(t, db, user.ID, "session-a")
	seedSession(t, db, user.ID, "session-b")

	token, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}

	got, err := ConsumePasswordResetToken(ctx, db, token, "new-hash")
	if err != nil {
		t.Fatalf("ConsumePasswordResetToken() failed: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("user id = %d, want %d", got.ID, user.ID)
	}

	reloaded, err := UserByID(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if reloaded.PasswordHash != "new-hash" {
		t.Errorf("PasswordHash = %q, want the new hash", reloaded.PasswordHash)
	}

	// Consuming a token invalidates the user's existing sessions.
	if got := countSessions(t, db, user.ID); got != 0 {
		t.Errorf("sessions remaining = %d, want 0", got)
	}
}

func TestValidateResetTokenDoesNotSpendIt(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	token, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	if err := ValidatePasswordResetToken(ctx, db, token); err != nil {
		t.Fatalf("ValidatePasswordResetToken() failed: %v", err)
	}
	if _, err := ConsumePasswordResetToken(ctx, db, token, "new-hash"); err != nil {
		t.Fatalf("token was spent by validation: %v", err)
	}
}

func TestValidateResetTokenRejectsUnknown(t *testing.T) {
	db, _, _ := resetFixture(t)

	err := ValidatePasswordResetToken(context.Background(), db, "not-a-token")
	if !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("error = %v, want ErrResetTokenInvalid", err)
	}
}

// Single use: a link left in an inbox must not keep working.
func TestConsumeResetTokenIsSingleUse(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	token, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	if _, err := ConsumePasswordResetToken(ctx, db, token, "new-hash"); err != nil {
		t.Fatalf("first use failed: %v", err)
	}
	if _, err := ConsumePasswordResetToken(ctx, db, token, "newer-hash"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("second use error = %v, want ErrResetTokenInvalid", err)
	}

	reloaded, err := UserByID(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if reloaded.PasswordHash != "new-hash" {
		t.Error("the second use changed the password")
	}
}

// Requesting several resets and using one must retire the rest.
func TestConsumeResetTokenInvalidatesSiblings(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	first, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	second, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}

	if _, err := ConsumePasswordResetToken(ctx, db, second, "new-hash"); err != nil {
		t.Fatalf("consume failed: %v", err)
	}
	if _, err := ConsumePasswordResetToken(ctx, db, first, "other-hash"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("an earlier token still worked: %v", err)
	}
}

func TestConsumeResetTokenRejectsExpired(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	token, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE password_reset_tokens SET expires_at = ? WHERE user_id = ?`,
		time.Now().Add(-time.Minute), user.ID); err != nil {
		t.Fatalf("expire token: %v", err)
	}

	if _, err := ConsumePasswordResetToken(ctx, db, token, "new-hash"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("error = %v, want ErrResetTokenInvalid", err)
	}
}

func TestConsumeResetTokenRejectsUnknown(t *testing.T) {
	db, _, _ := resetFixture(t)

	if _, err := ConsumePasswordResetToken(context.Background(), db, "not-a-token", "hash"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("error = %v, want ErrResetTokenInvalid", err)
	}
}

// A link issued before an account was retired must not be a way back in.
func TestConsumeResetTokenRejectsDisabledAccount(t *testing.T) {
	db, user, actor := resetFixture(t)
	ctx := context.Background()

	token, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	if err := SetUserDisabled(ctx, db, actor, user.ID, true); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}

	if _, err := ConsumePasswordResetToken(ctx, db, token, "new-hash"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("a disabled account was reset: %v", err)
	}
}

// The user reset it themselves; attributing it to an admin would be a lie.
func TestConsumeResetTokenAuditsAsTheUser(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	token, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	if _, err := ConsumePasswordResetToken(ctx, db, token, "new-hash"); err != nil {
		t.Fatalf("consume failed: %v", err)
	}

	var kind string
	var actorID int64
	if err := db.QueryRow(
		`SELECT actor_kind, actor_user_id FROM audit_log WHERE action = ? AND subject_id = ?`,
		ActionUserPasswordReset, user.ID).Scan(&kind, &actorID); err != nil {
		t.Fatalf("read audit entry: %v", err)
	}
	if kind != ActorPlayer || actorID != user.ID {
		t.Errorf("actor = %s/%d, want %s/%d", kind, actorID, ActorPlayer, user.ID)
	}
}

func TestDeleteExpiredResetTokens(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	live, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	if _, err := CreatePasswordResetToken(ctx, db, user.ID); err != nil {
		t.Fatalf("CreatePasswordResetToken() failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE password_reset_tokens SET expires_at = ? WHERE token_hash != ?`,
		time.Now().Add(-time.Hour), HashToken(live)); err != nil {
		t.Fatalf("expire token: %v", err)
	}

	n, err := DeleteExpiredResetTokens(ctx, db)
	if err != nil {
		t.Fatalf("DeleteExpiredResetTokens() failed: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
}

func TestMarkEmailVerified(t *testing.T) {
	db, user, _ := resetFixture(t)
	ctx := context.Background()

	if user.EmailVerifiedAt != nil {
		t.Fatal("a new user is already verified")
	}
	if err := MarkEmailVerified(ctx, db, user.ID); err != nil {
		t.Fatalf("MarkEmailVerified() failed: %v", err)
	}

	reloaded, err := UserByID(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if reloaded.EmailVerifiedAt == nil {
		t.Error("EmailVerifiedAt is still nil")
	}
}

// The two emailed links share a table, so the purpose is the only thing
// keeping them apart. A confirmation link must not set a password, and a
// reset link must not confirm an address.
func TestLinkTokensAreNotInterchangeable(t *testing.T) {
	ctx := context.Background()
	db, user, _ := resetFixture(t)

	verify, err := CreateEmailVerificationToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreateEmailVerificationToken: %v", err)
	}
	if _, err := ConsumePasswordResetToken(ctx, db, verify, "new-hash"); !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("a confirmation link spent at the reset endpoint: err = %v", err)
	}
	// And it survives the attempt: refusing must not burn the token.
	if _, err := ConsumeEmailVerificationToken(ctx, db, verify); err != nil {
		t.Errorf("the confirmation link stopped working after being refused elsewhere: %v", err)
	}

	reset, err := CreatePasswordResetToken(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}
	if _, err := ConsumeEmailVerificationToken(ctx, db, reset); !errors.Is(err, ErrResetTokenInvalid) {
		t.Errorf("a reset link spent at the confirmation endpoint: err = %v", err)
	}
	if _, err := ConsumePasswordResetToken(ctx, db, reset, "new-hash"); err != nil {
		t.Errorf("the reset link stopped working after being refused elsewhere: %v", err)
	}
}
