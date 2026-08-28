package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreateAndReadSession(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)

	session, err := CreateSession(ctx, db, userID, false)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}
	if len(session.ID) != sessionIDLen {
		t.Errorf("session id length = %d, want %d", len(session.ID), sessionIDLen)
	}

	got, user, err := SessionUser(ctx, db, session.ID)
	if err != nil {
		t.Fatalf("SessionUser() failed: %v", err)
	}
	if got.UserID != userID || user.ID != userID {
		t.Errorf("session user = %d/%d, want %d", got.UserID, user.ID, userID)
	}
	if got.PendingTOTP {
		t.Error("PendingTOTP = true, want false")
	}
}

// Forging a session means guessing 32 random bytes; two sessions sharing an id
// would mean it was not random at all.
func TestSessionIDsAreDistinct(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)

	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		s, err := CreateSession(ctx, db, userID, false)
		if err != nil {
			t.Fatalf("CreateSession() failed: %v", err)
		}
		if seen[string(s.ID)] {
			t.Fatal("CreateSession() repeated an id")
		}
		seen[string(s.ID)] = true
	}
}

func TestSessionUserUnknownID(t *testing.T) {
	db := migratedDB(t)

	_, _, err := SessionUser(context.Background(), db, []byte("nonsense"))
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("error = %v, want ErrSessionNotFound", err)
	}
}

// Expiry is enforced server-side, not merely by the cookie's own lifetime,
// which the client controls.
func TestSessionUserRejectsExpired(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)

	session, err := CreateSession(ctx, db, userID, false)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour), session.ID); err != nil {
		t.Fatalf("expire session: %v", err)
	}

	if _, _, err := SessionUser(ctx, db, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("error = %v, want ErrSessionNotFound", err)
	}

	// Reaped on the way past, since this is the one moment we look at it.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, session.ID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Error("the expired session was left in place")
	}
}

// Checking only at login would leave a disabled account working normally
// until its session expired — up to a month, which is not what an admin means
// by "disable".
func TestSessionUserRejectsDisabledAccount(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	session, err := CreateSession(ctx, db, user.ID, false)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}

	// Disabled directly, leaving the session in place: SetUserDisabled deletes
	// sessions, so this isolates the middleware's own check.
	if _, err := db.Exec(`UPDATE users SET disabled_at = CURRENT_TIMESTAMP WHERE id = ?`, user.ID); err != nil {
		t.Fatalf("disable user: %v", err)
	}

	if _, _, err := SessionUser(ctx, db, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("error = %v, want a disabled account's session to be refused", err)
	}
}

// A token captured before a privilege change must not work after it.
func TestRotateSessionReplacesID(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)

	first, err := CreateSession(ctx, db, userID, true)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}
	second, err := RotateSession(ctx, db, first, false)
	if err != nil {
		t.Fatalf("RotateSession() failed: %v", err)
	}

	if bytes.Equal(first.ID, second.ID) {
		t.Error("rotation reused the session id")
	}
	if second.PendingTOTP {
		t.Error("PendingTOTP survived rotation")
	}
	if _, _, err := SessionUser(ctx, db, first.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("the old session still works after rotation: %v", err)
	}
	if _, _, err := SessionUser(ctx, db, second.ID); err != nil {
		t.Errorf("the rotated session does not work: %v", err)
	}
}

func TestDeleteSession(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)

	session, err := CreateSession(ctx, db, userID, false)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}
	if err := DeleteSession(ctx, db, session.ID); err != nil {
		t.Fatalf("DeleteSession() failed: %v", err)
	}
	if _, _, err := SessionUser(ctx, db, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("the session survived deletion: %v", err)
	}
}

// Refreshing on every request would turn each page view into a write, and
// writes serialise in SQLite.
func TestTouchSessionThrottlesWrites(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)

	session, err := CreateSession(ctx, db, userID, false)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}

	wrote, err := TouchSession(ctx, db, session)
	if err != nil {
		t.Fatalf("TouchSession() failed: %v", err)
	}
	if wrote {
		t.Error("a fresh session was written back immediately")
	}

	// Aged past the refresh interval, as a session in daily use would be.
	session.ExpiresAt = time.Now().Add(SessionLifetime - 2*sessionRefreshInterval)
	wrote, err = TouchSession(ctx, db, session)
	if err != nil {
		t.Fatalf("TouchSession() failed: %v", err)
	}
	if !wrote {
		t.Error("an aged session was not extended")
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)

	live, err := CreateSession(ctx, db, userID, false)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}
	stale, err := CreateSession(ctx, db, userID, false)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour), stale.ID); err != nil {
		t.Fatalf("expire session: %v", err)
	}

	n, err := DeleteExpiredSessions(ctx, db)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions() failed: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
	if _, _, err := SessionUser(ctx, db, live.ID); err != nil {
		t.Errorf("the live session was deleted: %v", err)
	}
}
