package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// adminFixture creates an admin to act as, since every CLI write is attributed.
func adminFixture(t *testing.T, db *sql.DB) (int64, Actor) {
	t.Helper()
	id := seedUser(t, db, "admin@example.tld", true)
	return id, AdminActor(id)
}

func TestCreateUser(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "Martin@Example.TLD", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}

	if user.Email != "martin@example.tld" {
		t.Errorf("Email = %q, want it normalized to lowercase", user.Email)
	}
	if len(user.Handle) != handleLen {
		t.Errorf("Handle length = %d, want %d", len(user.Handle), handleLen)
	}
	if user.IsAdmin {
		t.Error("IsAdmin = true, want false")
	}
	if user.Disabled() {
		t.Error("a new user is disabled")
	}
	if user.HasTOTP {
		t.Error("a new user already has TOTP enrolled")
	}
}

// The handle is a WebAuthn user identifier: reusing one across accounts would
// defeat the reason keeps it opaque.
func TestCreateUserHandlesAreDistinct(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	first, err := CreateUser(ctx, db, actor, "one@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	second, err := CreateUser(ctx, db, actor, "two@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}

	if string(first.Handle) == string(second.Handle) {
		t.Error("two users share a handle")
	}
}

func TestCreateUserRejectsDuplicateEmail(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	if _, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false); err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}

	// Differing only by case must still collide, or two accounts could exist
	// that nobody can tell apart when logging in.
	_, err := CreateUser(ctx, db, actor, "MARTIN@example.tld", "hash", false)
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("error = %v, want ErrEmailTaken", err)
	}
}

func TestCreateUserRejectsEmptyEmail(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	if _, err := CreateUser(ctx, db, actor, "   ", "hash", false); err == nil {
		t.Error("CreateUser() accepted an empty email")
	}
}

func TestUserByEmailNotFound(t *testing.T) {
	db := migratedDB(t)

	_, err := UserByEmail(context.Background(), db, "nobody@example.tld")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("error = %v, want ErrUserNotFound", err)
	}
}

// A password reset that leaves old sessions alive does not lock anyone out,
// which is the reason to reset in the first place.
func TestSetUserPasswordInvalidatesSessions(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "old-hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	seedSession(t, db, user.ID, "session-a")
	seedSession(t, db, user.ID, "session-b")

	if err := SetUserPassword(ctx, db, actor, user.ID, "new-hash"); err != nil {
		t.Fatalf("SetUserPassword() failed: %v", err)
	}

	if got := countSessions(t, db, user.ID); got != 0 {
		t.Errorf("sessions remaining = %d, want 0", got)
	}
	reloaded, err := UserByID(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if reloaded.PasswordHash != "new-hash" {
		t.Errorf("PasswordHash = %q, want the new hash", reloaded.PasswordHash)
	}
}

func TestSetUserPasswordUnknownUser(t *testing.T) {
	db := migratedDB(t)
	_, actor := adminFixture(t, db)

	err := SetUserPassword(context.Background(), db, actor, 9999, "hash")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("error = %v, want ErrUserNotFound", err)
	}
}

// A session that already cleared TOTP must not outlive the secret it was
// granted against.
func TestResetUserTOTPClearsSecretsAndSessions(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", true)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE users
		SET totp_secret_encrypted = ?, totp_pending_secret_encrypted = ?, totp_last_step = ?
		WHERE id = ?`,
		[]byte("secret"), []byte("pending"), 12345, user.ID); err != nil {
		t.Fatalf("seed totp: %v", err)
	}
	seedSession(t, db, user.ID, "session-a")

	if err := ResetUserTOTP(ctx, db, actor, user.ID); err != nil {
		t.Fatalf("ResetUserTOTP() failed: %v", err)
	}

	var secret, pending []byte
	var lastStep sql.NullInt64
	if err := db.QueryRow(`
		SELECT totp_secret_encrypted, totp_pending_secret_encrypted, totp_last_step
		FROM users WHERE id = ?`, user.ID).Scan(&secret, &pending, &lastStep); err != nil {
		t.Fatalf("read totp columns: %v", err)
	}
	if secret != nil || pending != nil || lastStep.Valid {
		t.Errorf("TOTP state survived the reset: secret=%v pending=%v step=%v", secret, pending, lastStep)
	}
	if got := countSessions(t, db, user.ID); got != 0 {
		t.Errorf("sessions remaining = %d, want 0", got)
	}
}

// Disabling must end existing sessions. Otherwise the account stays usable for
// up to the session lifetime, which is not what "disabled" means.
func TestSetUserDisabledInvalidatesSessions(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	seedSession(t, db, user.ID, "session-a")

	if err := SetUserDisabled(ctx, db, actor, user.ID, true); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}

	reloaded, err := UserByID(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if !reloaded.Disabled() {
		t.Error("Disabled() = false after disabling")
	}
	if got := countSessions(t, db, user.ID); got != 0 {
		t.Errorf("sessions remaining = %d, want 0", got)
	}
}

func TestSetUserDisabledThenEnabled(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}

	if err := SetUserDisabled(ctx, db, actor, user.ID, true); err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if err := SetUserDisabled(ctx, db, actor, user.ID, false); err != nil {
		t.Fatalf("enable failed: %v", err)
	}

	reloaded, err := UserByID(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if reloaded.Disabled() {
		t.Error("Disabled() = true after re-enabling")
	}
}

// Attribution is the reason the audit log exists; a mutation that writes no
// entry is invisible to the admin view later.
func TestUserMutationsAreAudited(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	adminID, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	if err := SetUserDisabled(ctx, db, actor, user.ID, true); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}
	if err := SetUserDisabled(ctx, db, actor, user.ID, false); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}
	if err := ResetUserTOTP(ctx, db, actor, user.ID); err != nil {
		t.Fatalf("ResetUserTOTP() failed: %v", err)
	}

	want := []string{ActionUserCreated, ActionUserDisabled, ActionUserEnabled, ActionUser2FAReset}
	got := auditActions(t, db, SubjectUser, user.ID)
	if len(got) != len(want) {
		t.Fatalf("audit actions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("audit action %d = %q, want %q", i, got[i], want[i])
		}
	}

	var actorID int64
	if err := db.QueryRow(
		`SELECT actor_user_id FROM audit_log WHERE action = ? AND subject_id = ?`,
		ActionUserCreated, user.ID).Scan(&actorID); err != nil {
		t.Fatalf("read audit actor: %v", err)
	}
	if actorID != adminID {
		t.Errorf("audit actor = %d, want the acting admin %d", actorID, adminID)
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := map[string]string{
		"  Martin@Example.TLD ": "martin@example.tld",
		"martin@example.tld":    "martin@example.tld",
		"":                      "",
	}
	for in, want := range tests {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func seedSession(t *testing.T, db *sql.DB, userID int64, id string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
		[]byte(id), userID, "2099-01-01"); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

func countSessions(t *testing.T, db *sql.DB, userID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

func auditActions(t *testing.T, db *sql.DB, subjectType string, subjectID int64) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT action FROM audit_log WHERE subject_type = ? AND subject_id = ? ORDER BY id`,
		subjectType, subjectID)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	defer rows.Close()

	var actions []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan audit action: %v", err)
		}
		actions = append(actions, a)
	}
	return actions
}

// Re-disabling an already-disabled account still ends any sessions opened in
// between, so the audit entry records that something happened even though
// disabled_at does not move.
func TestSetUserDisabledTwiceKeepsOriginalTimestamp(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	if err := SetUserDisabled(ctx, db, actor, user.ID, true); err != nil {
		t.Fatalf("first disable failed: %v", err)
	}

	var first string
	if err := db.QueryRow(`SELECT disabled_at FROM users WHERE id = ?`, user.ID).Scan(&first); err != nil {
		t.Fatalf("read disabled_at: %v", err)
	}

	// A session created after the account was disabled — the situation that
	// makes re-running the command meaningful rather than a no-op.
	seedSession(t, db, user.ID, "session-after")

	if err := SetUserDisabled(ctx, db, actor, user.ID, true); err != nil {
		t.Fatalf("second disable failed: %v", err)
	}

	var second string
	if err := db.QueryRow(`SELECT disabled_at FROM users WHERE id = ?`, user.ID).Scan(&second); err != nil {
		t.Fatalf("read disabled_at: %v", err)
	}
	if second != first {
		t.Errorf("disabled_at moved from %q to %q; when they were disabled is the fact worth keeping", first, second)
	}

	if got := countSessions(t, db, user.ID); got != 0 {
		t.Errorf("sessions remaining = %d, want 0", got)
	}

	// Two entries: the sessions really were ended the second time.
	var entries int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action = ? AND subject_id = ?`,
		ActionUserDisabled, user.ID).Scan(&entries); err != nil {
		t.Fatalf("count audit entries: %v", err)
	}
	if entries != 2 {
		t.Errorf("audit entries = %d, want 2", entries)
	}
}

func TestBootstrapAdminCreatesTheFirstUser(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	user, created, err := BootstrapAdmin(ctx, db, "Admin@Example.TLD", "hash")
	if err != nil {
		t.Fatalf("BootstrapAdmin() failed: %v", err)
	}
	if !created {
		t.Fatal("created = false on an empty database")
	}
	if user.Email != "admin@example.tld" {
		t.Errorf("Email = %q, want it normalized", user.Email)
	}
	if !user.IsAdmin {
		t.Error("the bootstrapped user is not an admin")
	}
	if len(user.Handle) != handleLen {
		t.Errorf("Handle length = %d, want %d", len(user.Handle), handleLen)
	}
	// 2FA is still mandatory for admins: bootstrap is a convenience, not a
	// way past the security model.
	if user.HasTOTP {
		t.Error("the bootstrapped admin already has TOTP, which it cannot")
	}
}

// Leaving the variables set must be harmless, which is what makes them
// safe to keep in a compose file indefinitely.
func TestBootstrapAdminIsANoOpWhenUsersExist(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if _, _, err := BootstrapAdmin(ctx, db, "admin@example.tld", "first-hash"); err != nil {
		t.Fatalf("first BootstrapAdmin() failed: %v", err)
	}

	// A later start with a different password must not touch anything.
	_, created, err := BootstrapAdmin(ctx, db, "admin@example.tld", "second-hash")
	if err != nil {
		t.Fatalf("second BootstrapAdmin() failed: %v", err)
	}
	if created {
		t.Error("created = true on a database that already has users")
	}

	user, err := UserByEmail(ctx, db, "admin@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail() failed: %v", err)
	}
	if user.PasswordHash != "first-hash" {
		t.Error("a restart overwrote the stored password")
	}
}

// "First" means the installation, not the address. An email-specific check
// would quietly recreate an account someone had removed on purpose.
func TestBootstrapAdminDoesNotResurrectARemovedAccount(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	// Some other user exists; the configured admin does not.
	if _, _, err := BootstrapAdmin(ctx, db, "gone@example.tld", "hash"); err != nil {
		t.Fatalf("BootstrapAdmin() failed: %v", err)
	}
	if _, err := UserByEmail(ctx, db, "gone@example.tld"); !errors.Is(err, ErrUserNotFound) {
		t.Error("bootstrap created an account on a populated database")
	}
	_ = actor
}

// Nothing authorised it, because nothing existed that could have.
func TestBootstrapAdminIsAuditedAsSystem(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	user, _, err := BootstrapAdmin(ctx, db, "admin@example.tld", "hash")
	if err != nil {
		t.Fatalf("BootstrapAdmin() failed: %v", err)
	}

	var kind, detail string
	if err := db.QueryRow(
		`SELECT actor_kind, detail FROM audit_log WHERE subject_id = ? AND action = ?`,
		user.ID, ActionUserCreated).Scan(&kind, &detail); err != nil {
		t.Fatalf("read audit entry: %v", err)
	}
	if kind != ActorSystem {
		t.Errorf("actor_kind = %q, want %q", kind, ActorSystem)
	}
	if !strings.Contains(detail, "bootstrap") {
		t.Errorf("detail = %q, want it to record how the account was made", detail)
	}
}

// Two app instances racing on a fresh volume must not both succeed.
func TestBootstrapAdminIsAtomic(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, created, err := BootstrapAdmin(ctx, db, "admin@example.tld", "hash")
			errs <- err
			results <- created
		}()
	}

	var createdCount int
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("BootstrapAdmin() failed: %v", err)
		}
		if <-results {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Errorf("created %d times, want exactly 1", createdCount)
	}

	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("users = %d, want 1", users)
	}
}

// A new account reads in the default language until it says otherwise.
func TestUserLocaleDefaultsToEnglish(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)

	user, err := CreateUser(ctx, db, SystemActor(), "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Locale != "en" {
		t.Errorf("Locale = %q, want %q", user.Locale, "en")
	}

	if err := SetUserLocale(ctx, db, user.ID, "sv"); err != nil {
		t.Fatalf("SetUserLocale: %v", err)
	}
	again, err := UserByID(ctx, db, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if again.Locale != "sv" {
		t.Errorf("Locale = %q after setting, want %q", again.Locale, "sv")
	}

	// Empty is refused rather than stored: it would mean the fallback
	// forever, with nothing to show it had gone wrong.
	if err := SetUserLocale(ctx, db, user.ID, ""); err == nil {
		t.Error("SetUserLocale accepted an empty locale")
	}
}
