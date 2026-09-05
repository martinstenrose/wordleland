package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ResetTokenLifetime bounds how long a reset link works. An hour is plenty for
// someone who just asked for one, and short enough that a link left in an
// inbox is not a standing key to the account.
const ResetTokenLifetime = time.Hour

// resetTokenLen is the entropy in the emailed link.
const resetTokenLen = 32

// ErrResetTokenInvalid covers a token that is unknown, expired, or already
// used. They are deliberately indistinguishable: the response is the same, and
// separating them tells whoever is probing which guesses were close.
var ErrResetTokenInvalid = errors.New("reset token is invalid")

// newToken mints a single-use link token. One generator for every kind of
// them, so a new flow cannot quietly pick a shorter one.
func newToken() (string, error) {
	raw := make([]byte, resetTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashToken returns the stored form of a token.
//
// SHA-256 rather than argon2id: these are 32 random bytes, not a password, so
// there is no low-entropy guess to slow down — and a reset link should verify
// instantly. The same reasoning covers api_tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Token purposes. password_reset_tokens holds both kinds of emailed link,
// and the purpose is what keeps them apart: without it a token issued for
// one is spendable at the other — a confirmation link would set a password,
// and it would be burned in the process. The two tables that would avoid
// the problem are the same columns under different names.
const (
	purposeReset  = "reset"
	purposeVerify = "verify"
)

// CreatePasswordResetToken issues a reset token, returning the plaintext to
// email. Only its hash is stored, so a database read cannot mint a working
// link.
func CreatePasswordResetToken(ctx context.Context, q Querier, userID int64) (string, error) {
	return createLinkToken(ctx, q, userID, purposeReset)
}

// ValidatePasswordResetToken reports whether a reset token can currently be
// used without spending it. The reset handler calls this before doing the
// expensive password hash; ConsumePasswordResetToken checks it again inside
// the transaction, so validation here does not weaken single-use semantics.
func ValidatePasswordResetToken(ctx context.Context, q Querier, token string) error {
	_, err := passwordResetTokenUser(ctx, q, token)
	return err
}

func passwordResetTokenUser(ctx context.Context, q Querier, token string) (User, error) {
	var (
		userID    int64
		expiresAt time.Time
		usedAt    *time.Time
	)
	err := q.QueryRowContext(ctx,
		`SELECT user_id, expires_at, used_at FROM password_reset_tokens
		 WHERE token_hash = ? AND purpose = ?`,
		HashToken(token), purposeReset,
	).Scan(&userID, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrResetTokenInvalid
	}
	if err != nil {
		return User{}, fmt.Errorf("read reset token: %w", err)
	}
	if usedAt != nil || time.Now().After(expiresAt) {
		return User{}, ErrResetTokenInvalid
	}

	// A disabled account must not be reachable through a link issued before
	// it was retired.
	user, err := UserByID(ctx, q, userID)
	if err != nil {
		return User{}, err
	}
	if user.Disabled() {
		return User{}, ErrResetTokenInvalid
	}
	return user, nil
}

// createLinkToken mints one emailed link of the given purpose. Both flows go
// through here so a new one cannot be added without naming what it is for.
func createLinkToken(ctx context.Context, q Querier, userID int64, purpose string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}

	if _, err := q.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (token_hash, user_id, expires_at, purpose)
		 VALUES (?, ?, ?, ?)`,
		HashToken(token), userID, time.Now().Add(ResetTokenLifetime), purpose,
	); err != nil {
		return "", fmt.Errorf("store %s token: %w", purpose, err)
	}
	return token, nil
}

// ConsumePasswordResetToken validates a token, marks it used, sets the new
// password, and invalidates every session for that user.
//
// All of it happens in one transaction. A reset that set the password but left
// the token usable, or left old sessions alive, would not actually be a reset —
// and those are exactly the states a crash between steps would leave behind.
func ConsumePasswordResetToken(ctx context.Context, db *sql.DB, token, passwordHash string) (User, error) {
	var user User
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		var err error
		user, err = passwordResetTokenUser(ctx, tx, token)
		if err != nil {
			return err
		}
		userID := user.ID

		if _, err := tx.ExecContext(ctx,
			`UPDATE password_reset_tokens SET used_at = CURRENT_TIMESTAMP
			 WHERE token_hash = ? AND purpose = ?`,
			HashToken(token), purposeReset,
		); err != nil {
			return fmt.Errorf("mark reset token used: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID,
		); err != nil {
			return fmt.Errorf("set password: %w", err)
		}

		// Every other outstanding token goes too: if someone requested
		// several resets, the ones they did not use should not stay live.
		if _, err := tx.ExecContext(ctx,
			`UPDATE password_reset_tokens SET used_at = CURRENT_TIMESTAMP
			 WHERE user_id = ? AND used_at IS NULL`, userID,
		); err != nil {
			return fmt.Errorf("invalidate other reset tokens: %w", err)
		}

		deleted, err := deleteSessions(ctx, tx, userID)
		if err != nil {
			return err
		}

		// The actor is the user themselves: nobody else authorised this, and
		// attributing it to an admin would be a lie.
		return LogActivity(ctx, tx, PlayerActor(userID), ActionUserPasswordReset, SubjectUser, &userID,
			map[string]any{"via": "email", "sessions_invalidated": deleted})
	})
	return user, err
}

// DeleteExpiredResetTokens removes spent and expired tokens.
func DeleteExpiredResetTokens(ctx context.Context, q Querier) (int64, error) {
	res, err := q.ExecContext(ctx,
		`DELETE FROM password_reset_tokens WHERE expires_at < ? OR used_at IS NOT NULL`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired reset tokens: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired reset tokens: %w", err)
	}
	return n, nil
}

// MarkEmailVerified records that an address was confirmed reachable.
func MarkEmailVerified(ctx context.Context, q Querier, userID int64) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE users SET email_verified_at = CURRENT_TIMESTAMP WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("mark email verified: %w", err)
	}
	return nil
}

// CreateEmailVerificationToken issues a token for confirming an address.
//
// It reuses password_reset_tokens: both are single-use, short-lived,
// hash-stored links tied to a user, and a second table would be the same
// columns under a different name.
func CreateEmailVerificationToken(ctx context.Context, q Querier, userID int64) (string, error) {
	return createLinkToken(ctx, q, userID, purposeVerify)
}

// ConsumeEmailVerificationToken marks a token used, records verification,
// and applies a pending address change if one is waiting.
//
// The promotion happens inside the same transaction as consuming the token:
// a confirmed address that failed to take over would leave the account
// signing in with the old one and no link left to try again.
func ConsumeEmailVerificationToken(ctx context.Context, db *sql.DB, token string) (User, error) {
	var user User
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		var (
			userID    int64
			expiresAt time.Time
			usedAt    *time.Time
		)
		err := tx.QueryRowContext(ctx,
			`SELECT user_id, expires_at, used_at FROM password_reset_tokens
			 WHERE token_hash = ? AND purpose = ?`,
			HashToken(token), purposeVerify,
		).Scan(&userID, &expiresAt, &usedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrResetTokenInvalid
		}
		if err != nil {
			return fmt.Errorf("read verification token: %w", err)
		}
		if usedAt != nil || time.Now().After(expiresAt) {
			return ErrResetTokenInvalid
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE password_reset_tokens SET used_at = CURRENT_TIMESTAMP
			 WHERE token_hash = ? AND purpose = ?`,
			HashToken(token), purposeVerify,
		); err != nil {
			return fmt.Errorf("mark verification token used: %w", err)
		}
		if err := MarkEmailVerified(ctx, tx, userID); err != nil {
			return err
		}
		// A change of address waiting on this confirmation takes effect now.
		// PromotePendingEmail does nothing when there is none, which is the
		// ordinary case of somebody confirming the address they already use.
		if err := PromotePendingEmail(ctx, tx, PlayerActor(userID), userID); err != nil {
			return err
		}

		user, err = UserByID(ctx, tx, userID)
		return err
	})
	return user, err
}
