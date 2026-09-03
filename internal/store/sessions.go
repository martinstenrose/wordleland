package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// sessionIDLen is the length of the opaque token the cookie carries. It is the
// only secret in the cookie: the session holds no user data, so forging one
// means guessing 32 random bytes.
const sessionIDLen = 32

// SessionLifetime is how long a session lasts without use.
const SessionLifetime = 30 * 24 * time.Hour

// sessionRefreshInterval bounds how often a sliding expiry is written back.
// Refreshing on literally every request would turn each page view into a write,
// and writes serialise in SQLite.
const sessionRefreshInterval = time.Hour

// ErrSessionNotFound covers a session that is absent, expired, or belongs to a
// user who can no longer log in. Callers cannot distinguish these, and should
// not: every one of them means "log in again".
var ErrSessionNotFound = errors.New("session not found")

// Session is a login in progress or established.
type Session struct {
	ID          []byte
	UserID      int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	PendingTOTP bool
}

// CreateSession issues a session.
//
// pendingTOTP marks a session that has passed the password step but not the
// TOTP one; it grants access to nothing but the TOTP prompt.
func CreateSession(ctx context.Context, q Querier, userID int64, pendingTOTP bool) (Session, error) {
	id := make([]byte, sessionIDLen)
	if _, err := rand.Read(id); err != nil {
		return Session{}, fmt.Errorf("generate session id: %w", err)
	}

	expires := time.Now().Add(SessionLifetime)
	if _, err := q.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, expires_at, pending_totp) VALUES (?, ?, ?, ?)`,
		id, userID, expires, pendingTOTP,
	); err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}

	return Session{ID: id, UserID: userID, ExpiresAt: expires, PendingTOTP: pendingTOTP}, nil
}

// SessionUser returns a live session together with its user.
//
// Expiry is enforced here rather than trusted from the cookie, and a disabled
// account is rejected on every request. Checking only at login would leave a
// disabled user working normally until their session expired, which can be a
// month — not what an admin means by "disable".
// This builds its own column list rather than reusing userColumns, because
// it joins. A field added to User therefore has to be added here too, or
// the request context carries a zero value for it — which is how the
// language preference was read as empty on every page.
func SessionUser(ctx context.Context, q Querier, id []byte) (Session, User, error) {
	var s Session
	var u User
	err := q.QueryRowContext(ctx, `
		SELECT s.id, s.user_id, s.created_at, s.expires_at, s.pending_totp,
		       u.id, u.handle, u.email, u.email_verified_at, u.disabled_at,
		       u.password_hash, u.is_admin, u.totp_secret_encrypted IS NOT NULL,
		       u.locale
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.id = ?`, id,
	).Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.PendingTOTP,
		&u.ID, &u.Handle, &u.Email, &u.EmailVerifiedAt, &u.DisabledAt,
		&u.PasswordHash, &u.IsAdmin, &u.HasTOTP, &u.Locale)

	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("read session: %w", err)
	}

	if time.Now().After(s.ExpiresAt) {
		// Deleted rather than left to accumulate: an expired session is
		// already useless, and this is the only moment we know to look at it.
		_, _ = q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return Session{}, User{}, ErrSessionNotFound
	}

	if u.Disabled() {
		return Session{}, User{}, ErrSessionNotFound
	}

	return s, u, nil
}

// TouchSession extends a session's expiry, at most once per refresh interval.
// It reports whether it wrote.
func TouchSession(ctx context.Context, q Querier, s Session) (bool, error) {
	if time.Until(s.ExpiresAt) > SessionLifetime-sessionRefreshInterval {
		return false, nil
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(SessionLifetime), s.ID,
	); err != nil {
		return false, fmt.Errorf("extend session: %w", err)
	}
	return true, nil
}

// RotateSession replaces a session with a new one for the same user.
//
// The id changes on every privilege change — after the password step and again
// after TOTP — so a token captured before the change cannot be replayed
// against the rights granted after it.
func RotateSession(ctx context.Context, q Querier, old Session, pendingTOTP bool) (Session, error) {
	fresh, err := CreateSession(ctx, q, old.UserID, pendingTOTP)
	if err != nil {
		return Session{}, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, old.ID); err != nil {
		return Session{}, fmt.Errorf("delete rotated session: %w", err)
	}
	return fresh, nil
}

// DeleteSession removes one session, for logout.
func DeleteSession(ctx context.Context, q Querier, id []byte) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes every session past its expiry, returning how
// many. Called on a schedule by the janitor in cmd/wordleland; SessionUser
// also reaps opportunistically in between.
func DeleteExpiredSessions(ctx context.Context, q Querier) (int64, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return n, nil
}
