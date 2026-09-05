package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	// ErrNoPendingSecret reports that enrolment was not started, or was
	// already completed.
	ErrNoPendingSecret = errors.New("no pending TOTP enrolment")
	// ErrNoTOTPSecret reports that the account has not enrolled.
	ErrNoTOTPSecret = errors.New("no TOTP secret")
	// ErrCodeReplayed reports a code from a step already accepted.
	ErrCodeReplayed = errors.New("code already used")
)

// SetPendingTOTPSecret stores an enrolment secret, not yet in force.
//
// It goes to the pending column rather than the live one so a mis-scanned QR
// code cannot lock anyone out: until a valid code proves the phone holds the
// same secret, the account is exactly as it was.
func SetPendingTOTPSecret(ctx context.Context, q Querier, userID int64, encrypted []byte) error {
	res, err := q.ExecContext(ctx,
		`UPDATE users SET totp_pending_secret_encrypted = ? WHERE id = ?`, encrypted, userID)
	if err != nil {
		return fmt.Errorf("store pending TOTP secret: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// PendingTOTPSecret returns the enrolment secret awaiting confirmation.
func PendingTOTPSecret(ctx context.Context, q Querier, userID int64) ([]byte, error) {
	var secret []byte
	err := q.QueryRowContext(ctx,
		`SELECT totp_pending_secret_encrypted FROM users WHERE id = ?`, userID).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read pending TOTP secret: %w", err)
	}
	if secret == nil {
		return nil, ErrNoPendingSecret
	}
	return secret, nil
}

// TOTPSecret returns the enrolled secret.
func TOTPSecret(ctx context.Context, q Querier, userID int64) ([]byte, error) {
	var secret []byte
	err := q.QueryRowContext(ctx,
		`SELECT totp_secret_encrypted FROM users WHERE id = ?`, userID).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read TOTP secret: %w", err)
	}
	if secret == nil {
		return nil, ErrNoTOTPSecret
	}
	return secret, nil
}

// PromotePendingTOTPSecret makes the pending secret live, recording the step
// that confirmed it.
//
// Promotion happens only once a valid code has been submitted, which is the
// ordering the rule is. The confirming step is stored immediately so the code
// just used cannot be replayed against the newly enrolled secret.
func PromotePendingTOTPSecret(ctx context.Context, db *sql.DB, actor Actor, userID int64, step int64) error {
	return InTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE users
			SET totp_secret_encrypted = totp_pending_secret_encrypted,
			    totp_pending_secret_encrypted = NULL,
			    totp_last_step = ?
			WHERE id = ? AND totp_pending_secret_encrypted IS NOT NULL`, step, userID)
		if err != nil {
			return fmt.Errorf("promote TOTP secret: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return ErrNoPendingSecret
		}

		// Codes from a previous enrolment die with the secret they were
		// issued against. Somebody replacing their two-factor because the
		// old one was compromised means the old codes too; leaving them
		// live would keep a stolen sheet working.
		if err := DiscardRecoveryCodes(ctx, tx, userID); err != nil {
			return err
		}
		return LogActivity(ctx, tx, actor, ActionUser2FAEnrolled, SubjectUser, &userID, nil)
	})
}

// RecordTOTPStep stores a newly accepted step, rejecting one already used.
//
// This is the replay defence. A TOTP code stays valid for its whole
// window, so without this an observed code — read over someone's shoulder, or
// captured from a phishing page — could be used a second time within the same
// thirty seconds. The comparison is done in the UPDATE itself so two
// simultaneous submissions cannot both pass a check-then-write.
func RecordTOTPStep(ctx context.Context, q Querier, userID int64, step int64) error {
	res, err := q.ExecContext(ctx, `
		UPDATE users
		SET totp_last_step = ?
		WHERE id = ? AND (totp_last_step IS NULL OR totp_last_step < ?)`,
		step, userID, step)
	if err != nil {
		return fmt.Errorf("record TOTP step: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrCodeReplayed
	}
	return nil
}

// ClearPendingTOTPSecret abandons an unconfirmed enrolment.
func ClearPendingTOTPSecret(ctx context.Context, q Querier, userID int64) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE users SET totp_pending_secret_encrypted = NULL WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("clear pending TOTP secret: %w", err)
	}
	return nil
}
