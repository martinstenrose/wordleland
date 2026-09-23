package store

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"
)

// onCEST runs the rest of a test with the process clock two hours ahead of
// UTC, as the deployment's is in summer. A machine running in UTC would
// otherwise pass every test here whether or not times were converted.
func onCEST(t *testing.T) {
	t.Helper()
	saved := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	t.Cleanup(func() { time.Local = saved })
}

// utcForm is SQLite's own spelling, the one CURRENT_TIMESTAMP writes.
var utcForm = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)

// stored reads a column's text as SQLite holds it, bypassing the driver's
// conversion to time.Time.
func stored(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var v string
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("read %q: %v", query, err)
	}
	return v
}

// Every time a writer binds from Go is stored in UTC, in the form
// CURRENT_TIMESTAMP writes — whatever zone the process runs in.
func TestTimestampsAreStoredInUTC(t *testing.T) {
	onCEST(t)
	db, playerID, adminID, actor := resultsFixture(t)
	ctx := context.Background()

	posted := time.Date(2026, time.September, 23, 5, 29, 14, 67e6, time.Local)
	r := sampleResult(playerID, 1890, 4)
	r.PostedAt = &posted
	if _, _, err := UpsertResult(ctx, db, r, nil, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}
	if err := HoldPendingResult(ctx, db, "signal", testUUID, "Someone",
		PendingResult{PuzzleNo: 1890, Solved: true, Guesses: ptr(4), PostedAt: &posted}); err != nil {
		t.Fatalf("HoldPendingResult: %v", err)
	}
	if _, err := CreateSession(ctx, db, adminID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := CreatePasswordResetToken(ctx, db, adminID); err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}
	if _, err := CreateInvitation(ctx, db, actor, playerID, "someone@example.tld", "en"); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	expires := time.Date(2026, time.December, 1, 0, 30, 0, 0, time.Local)
	if _, _, err := CreateAPIToken(ctx, db, actor, "script", &expires); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	for _, c := range []struct{ query, want string }{
		{`SELECT CAST(posted_at AS TEXT) FROM results`, "2026-09-23 03:29:14"},
		{`SELECT CAST(posted_at AS TEXT) FROM pending_results`, "2026-09-23 03:29:14"},
		// The day before in UTC: half past midnight in CEST.
		{`SELECT CAST(expires_at AS TEXT) FROM api_tokens`, "2026-11-30 22:30:00"},
		{`SELECT CAST(expires_at AS TEXT) FROM sessions`, ""},
		{`SELECT CAST(expires_at AS TEXT) FROM password_reset_tokens`, ""},
		{`SELECT CAST(expires_at AS TEXT) FROM invitations`, ""},
	} {
		got := stored(t, db, c.query)
		if !utcForm.MatchString(got) {
			t.Errorf("%s stored %q, want the form 2006-01-02 15:04:05", c.query, got)
			continue
		}
		if c.want != "" && got != c.want {
			t.Errorf("%s stored %q, want the UTC instant %q", c.query, got, c.want)
		}
	}

	// And it reads back as the same instant.
	back, err := ResultFor(ctx, db, 1890, playerID)
	if err != nil {
		t.Fatalf("ResultFor: %v", err)
	}
	if !back.PostedAt.Equal(posted.Truncate(time.Second)) {
		t.Errorf("posted_at read back as %v, want %v", back.PostedAt, posted)
	}
}

// A held result an hour old is not past a ninety-minute retention. The
// cutoff used to be bound in the process's zone and compared as text with
// the UTC received_at, which on a CEST clock put it two hours in the
// future and purged the row.
func TestPendingRetentionComparesLikeWithLike(t *testing.T) {
	onCEST(t)
	db, _, _, _ := identityFixture(t)
	ctx := context.Background()

	holdResult(t, db, 1900, 4, false)
	if _, err := db.ExecContext(ctx,
		`UPDATE pending_results SET received_at = datetime('now', '-1 hour')`); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	n, err := DeleteExpiredPendingResults(ctx, db, 90*time.Minute)
	if err != nil {
		t.Fatalf("DeleteExpiredPendingResults: %v", err)
	}
	if n != 0 {
		t.Errorf("purged %d held results an hour old under a 90-minute retention, want 0", n)
	}
}

// An invitation that expired an hour ago is not pending. Its expiry used to
// be stored in the process's zone and compared as text with UTC
// CURRENT_TIMESTAMP, which on a CEST clock kept it pending two hours late.
func TestAnExpiredInvitationIsNotPending(t *testing.T) {
	onCEST(t)
	db, playerID, _, actor := resultsFixture(t)
	ctx := context.Background()

	if _, err := CreateInvitation(ctx, db, actor, playerID, "someone@example.tld", "en"); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE invitations SET expires_at = ?`,
		time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("expire invitation: %v", err)
	}
	if _, err := PendingInvitation(ctx, db, playerID); err == nil {
		t.Error("an invitation that expired an hour ago is still pending")
	}
}
