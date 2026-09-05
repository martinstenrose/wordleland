package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// apiTokenLen is the entropy in an ingest token.
const apiTokenLen = 32

// ErrTokenInvalid covers a token that is unknown, revoked, or expired. The
// caller gets one answer for all three: telling them which would help someone
// probing for a token that merely needs its expiry extended.
var ErrTokenInvalid = errors.New("token is invalid")

// APIToken is a bearer credential for the ingest API.
type APIToken struct {
	ID        int64
	Label     string
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// CreateAPIToken issues a token, returning the plaintext exactly once.
//
// Only the hash is stored, so the database cannot yield a working token. The
// plaintext exists in the operator's terminal and then nowhere.
func CreateAPIToken(ctx context.Context, db *sql.DB, actor Actor, label string, expiresAt *time.Time) (string, APIToken, error) {
	raw := make([]byte, apiTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", APIToken{}, fmt.Errorf("generate token: %w", err)
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)

	var token APIToken
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO api_tokens (label, token_hash, expires_at) VALUES (?, ?, ?)`,
			label, HashToken(plaintext), expiresAt)
		if err != nil {
			return fmt.Errorf("create token: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("create token: %w", err)
		}
		token = APIToken{ID: id, Label: label, ExpiresAt: expiresAt}
		return LogActivity(ctx, tx, actor, ActionTokenCreated, SubjectToken, &id,
			map[string]any{"label": label})
	})
	return plaintext, token, err
}

// AuthenticateToken resolves a bearer token, respecting expiry and revocation.
func AuthenticateToken(ctx context.Context, q Querier, plaintext string) (APIToken, error) {
	var t APIToken
	err := q.QueryRowContext(ctx,
		`SELECT id, label, expires_at, revoked_at FROM api_tokens WHERE token_hash = ?`,
		HashToken(plaintext),
	).Scan(&t.ID, &t.Label, &t.ExpiresAt, &t.RevokedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrTokenInvalid
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("read token: %w", err)
	}
	if t.RevokedAt != nil {
		return APIToken{}, ErrTokenInvalid
	}
	// NULL expires_at means never expires.
	if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
		return APIToken{}, ErrTokenInvalid
	}
	return t, nil
}

// RevokeAPIToken marks a token revoked.
//
// Revocation is by flag, never by deleting the row: activity_log references
// tokens under RESTRICT, so a token that has written anything cannot be
// deleted — and should not be, since that would erase what it did.
func RevokeAPIToken(ctx context.Context, db *sql.DB, actor Actor, id int64) error {
	return InTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP) WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("revoke token: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return ErrTokenInvalid
		}
		return LogActivity(ctx, tx, actor, ActionTokenRevoked, SubjectToken, &id, nil)
	})
}

// ListAPITokens returns every token, newest first.
func ListAPITokens(ctx context.Context, q Querier) ([]APIToken, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, label, expires_at, revoked_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	var tokens []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Label, &t.ExpiresAt, &t.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}
