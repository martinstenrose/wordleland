package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/martinstenrose/wordleland/internal/auth"
)

// ErrNoRecoveryCode covers a code that does not match, has already been
// used, or belongs to somebody else. They are not distinguished: the
// response to all three is the same, and telling them apart tells an
// attacker which of their guesses was closest.
var ErrNoRecoveryCode = errors.New("no such recovery code")

// ReplaceRecoveryCodes issues a fresh set and returns the plaintext, which
// is the only time it exists. Any earlier codes are revoked in the same
// transaction: a set that was written down and then regenerated must stop
// working the moment the new one is shown, not merely once it is used.
func ReplaceRecoveryCodes(ctx context.Context, db *sql.DB, actor Actor, userID int64) ([]string, error) {
	codes := make([]string, 0, auth.RecoveryCodeCount)
	for range auth.RecoveryCodeCount {
		code, err := auth.GenerateRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}

	err := InTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM totp_recovery_codes WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("clear recovery codes: %w", err)
		}
		for _, code := range codes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO totp_recovery_codes (user_id, code_hash) VALUES (?, ?)`,
				userID, HashToken(auth.NormalizeRecoveryCode(code)),
			); err != nil {
				return fmt.Errorf("store recovery code: %w", err)
			}
		}
		return LogActivity(ctx, tx, actor, ActionRecoveryCodesIssued, SubjectUser, &userID,
			map[string]any{"count": len(codes)})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// ConsumeRecoveryCode spends a code, returning ErrNoRecoveryCode if there is
// nothing to spend. Marking it used and checking it are one statement, so
// two requests racing with the same code cannot both succeed.
func ConsumeRecoveryCode(ctx context.Context, db *sql.DB, userID int64, typed string) error {
	normalized := auth.NormalizeRecoveryCode(typed)
	if normalized == "" {
		return ErrNoRecoveryCode
	}

	return InTx(ctx, db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE totp_recovery_codes
			   SET used_at = CURRENT_TIMESTAMP
			 WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
			userID, HashToken(normalized))
		if err != nil {
			return fmt.Errorf("spend recovery code: %w", err)
		}
		spent, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("spend recovery code: %w", err)
		}
		if spent == 0 {
			return ErrNoRecoveryCode
		}

		remaining, err := countRecoveryCodes(ctx, tx, userID)
		if err != nil {
			return err
		}
		// The count goes in the log so the owner can see an account
		// running out before it locks somebody out.
		return LogActivity(ctx, tx, PlayerActor(userID), ActionRecoveryCodeUsed, SubjectUser, &userID,
			map[string]any{"remaining": remaining})
	})
}

// CountRecoveryCodes reports how many are left unused.
func CountRecoveryCodes(ctx context.Context, q Querier, userID int64) (int, error) {
	return countRecoveryCodes(ctx, q, userID)
}

func countRecoveryCodes(ctx context.Context, q Querier, userID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM totp_recovery_codes WHERE user_id = ? AND used_at IS NULL`,
		userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return n, nil
}

// DiscardRecoveryCodes revokes the set outright, which is what resetting an
// enrolment has to do: codes minted against the old secret are a way past
// the new one.
func DiscardRecoveryCodes(ctx context.Context, q Querier, userID int64) error {
	if _, err := q.ExecContext(ctx,
		`DELETE FROM totp_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("discard recovery codes: %w", err)
	}
	return nil
}
