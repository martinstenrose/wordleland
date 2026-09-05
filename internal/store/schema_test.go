package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// migratedDB returns a database with the real schema applied.
func migratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	if err := Migrate(context.Background(), db, Migrations()); err != nil {
		t.Fatalf("Migrate() failed: %v", err)
	}
	return db
}

// seedUser inserts a user and returns its id.
func seedUser(t *testing.T, db *sql.DB, email string, admin bool) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (handle, email, password_hash, is_admin) VALUES (?, ?, ?, ?)`,
		[]byte("handle-"+email), email, "argon2id-placeholder", admin,
	)
	if err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("user id: %v", err)
	}
	return id
}

// seedPlayer inserts a player and returns its id.
func seedPlayer(t *testing.T, db *sql.DB, slug string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO players (slug, name) VALUES (?, ?)`, slug, strings.ToUpper(slug[:1])+slug[1:])
	if err != nil {
		t.Fatalf("seed player %s: %v", slug, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("player id: %v", err)
	}
	return id
}

func TestSchemaTablesExist(t *testing.T) {
	db := migratedDB(t)

	want := []string{
		"activity_log", "api_tokens", "password_reset_tokens", "pending_results",
		"player_identities", "players", "results", "sessions", "settings",
		"users",
	}
	for _, table := range want {
		t.Run(table, func(t *testing.T) {
			var name string
			err := db.QueryRow(
				`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
			).Scan(&name)
			if err != nil {
				t.Errorf("table %s is missing: %v", table, err)
			}
		})
	}
}

// : entered_by is ON DELETE RESTRICT, never SET NULL. Ingest reads
// "entered_by IS NULL" as "safe for a token to overwrite", so nulling it on
// delete would arm the bridge to revert every correction that user made.
// The point of the FK is that this is impossible, not merely discouraged.
func TestDeletingUserWithResultsIsRefused(t *testing.T) {
	db := migratedDB(t)

	userID := seedUser(t, db, "martin@example.tld", true)
	playerID := seedPlayer(t, db, "martin")

	if _, err := db.Exec(
		`INSERT INTO results (puzzle_no, date, player_id, guesses, solved, entered_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		1890, "2026-08-23", playerID, 4, true, userID,
	); err != nil {
		t.Fatalf("insert result: %v", err)
	}

	_, err := db.Exec(`DELETE FROM users WHERE id = ?`, userID)
	if err == nil {
		t.Fatal("deleting a user who entered results succeeded; RESTRICT is not in force")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("error = %v, want a foreign key constraint failure", err)
	}

	// And the attribution survives, which is the whole point.
	var entered sql.NullInt64
	if err := db.QueryRow(`SELECT entered_by FROM results WHERE puzzle_no = ?`, 1890).Scan(&entered); err != nil {
		t.Fatalf("read back result: %v", err)
	}
	if !entered.Valid || entered.Int64 != userID {
		t.Errorf("entered_by = %v, want %d", entered, userID)
	}
}

// A user who has entered nothing may be deleted outright.
func TestDeletingUserWithoutResultsIsAllowed(t *testing.T) {
	db := migratedDB(t)
	userID := seedUser(t, db, "nobody@example.tld", false)

	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("deleting a user with no results failed: %v", err)
	}
}

// : players.user_id is SET NULL — unlinking a login from a scoreboard
// entity is normal and loses nothing.
func TestDeletingUserUnlinksPlayer(t *testing.T) {
	db := migratedDB(t)

	userID := seedUser(t, db, "player@example.tld", false)
	playerID := seedPlayer(t, db, "martin")
	if _, err := db.Exec(`UPDATE players SET user_id = ? WHERE id = ?`, userID, playerID); err != nil {
		t.Fatalf("link player: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	var linked sql.NullInt64
	if err := db.QueryRow(`SELECT user_id FROM players WHERE id = ?`, playerID).Scan(&linked); err != nil {
		t.Fatalf("read player: %v", err)
	}
	if linked.Valid {
		t.Errorf("players.user_id = %v after the user was deleted, want NULL", linked)
	}
}

// : results.player_id is CASCADE, which is exactly why players are never
// hard-deleted. This test documents the destruction rather than endorsing it —
// retirement is active = false, and the CLI exposes no player delete.
func TestDeletingPlayerCascadesResults(t *testing.T) {
	db := migratedDB(t)
	playerID := seedPlayer(t, db, "martin")

	for _, puzzle := range []int{1888, 1889, 1890} {
		if _, err := db.Exec(
			`INSERT INTO results (puzzle_no, date, player_id, guesses, solved)
			 VALUES (?, ?, ?, ?, ?)`,
			puzzle, "2026-08-23", playerID, 4, true,
		); err != nil {
			t.Fatalf("insert result %d: %v", puzzle, err)
		}
	}

	if _, err := db.Exec(`DELETE FROM players WHERE id = ?`, playerID); err != nil {
		t.Fatalf("delete player: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM results WHERE player_id = ?`, playerID).Scan(&count); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if count != 0 {
		t.Errorf("results remaining = %d, want 0 (CASCADE); if this changed, the schema's "+
			"reason for never deleting players changed with it", count)
	}
}

// : solved carries a guess count, failed carries none. A missed day is
// the absence of a row, not a row with both NULL.
func TestResultsSolvedGuessesConsistency(t *testing.T) {
	db := migratedDB(t)
	playerID := seedPlayer(t, db, "martin")

	tests := []struct {
		name    string
		guesses any
		solved  bool
		wantErr bool
	}{
		{name: "solved in four", guesses: 4, solved: true},
		{name: "solved in one", guesses: 1, solved: true},
		{name: "solved in six", guesses: 6, solved: true},
		{name: "failed with no guess count", guesses: nil, solved: false},

		{name: "solved without a guess count", guesses: nil, solved: true, wantErr: true},
		{name: "failed with a guess count", guesses: 3, solved: false, wantErr: true},
		{name: "guesses below range", guesses: 0, solved: true, wantErr: true},
		{name: "guesses above range", guesses: 7, solved: true, wantErr: true},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.Exec(
				`INSERT INTO results (puzzle_no, date, player_id, guesses, solved)
				 VALUES (?, ?, ?, ?, ?)`,
				2000+i, "2026-08-23", playerID, tt.guesses, tt.solved,
			)
			if tt.wantErr {
				if err == nil {
					t.Errorf("insert succeeded, want a CHECK constraint failure")
				}
				return
			}
			if err != nil {
				t.Errorf("insert failed: %v", err)
			}
		})
	}
}

func TestResultsUniquePerPuzzleAndPlayer(t *testing.T) {
	db := migratedDB(t)
	playerID := seedPlayer(t, db, "martin")

	insert := func() error {
		_, err := db.Exec(
			`INSERT INTO results (puzzle_no, date, player_id, guesses, solved)
			 VALUES (?, ?, ?, ?, ?)`,
			1890, "2026-08-23", playerID, 4, true,
		)
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert failed: %v", err)
	}
	if err := insert(); err == nil {
		t.Error("second insert for the same puzzle and player succeeded, want a UNIQUE failure")
	}
}

func TestResultsHardModeDefaultsFalse(t *testing.T) {
	db := migratedDB(t)
	playerID := seedPlayer(t, db, "martin")

	// Backfilled rows omit hard_mode: the spreadsheet has no such data.
	if _, err := db.Exec(
		`INSERT INTO results (puzzle_no, date, player_id, guesses, solved)
		 VALUES (?, ?, ?, ?, ?)`,
		1890, "2026-08-23", playerID, 4, true,
	); err != nil {
		t.Fatalf("insert result: %v", err)
	}

	var hardMode bool
	if err := db.QueryRow(`SELECT hard_mode FROM results WHERE puzzle_no = ?`, 1890).Scan(&hardMode); err != nil {
		t.Fatalf("read hard_mode: %v", err)
	}
	if hardMode {
		t.Error("hard_mode defaulted to true, want false")
	}
}

// : a repost of the same puzzle overwrites rather than accumulating.
func TestPendingResultsUpsertOnRepost(t *testing.T) {
	db := migratedDB(t)

	const upsert = `
		INSERT INTO pending_results
			(source, external_id, display_hint, puzzle_no, solved, guesses, hard_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, external_id, puzzle_no) DO UPDATE SET
			solved       = excluded.solved,
			guesses      = excluded.guesses,
			hard_mode    = excluded.hard_mode,
			display_hint = excluded.display_hint,
			received_at  = CURRENT_TIMESTAMP`

	if _, err := db.Exec(upsert, "signal", "uuid-1", "Old Name", 1890, true, 5, false); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if _, err := db.Exec(upsert, "signal", "uuid-1", "New Name", 1890, true, 3, true); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_results`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("pending_results rows = %d after a repost, want 1", count)
	}

	var (
		guesses  int
		hardMode bool
		hint     string
	)
	if err := db.QueryRow(
		`SELECT guesses, hard_mode, display_hint FROM pending_results`,
	).Scan(&guesses, &hardMode, &hint); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if guesses != 3 || !hardMode || hint != "New Name" {
		t.Errorf("got guesses=%d hard_mode=%v display_hint=%q, want the repost to have won", guesses, hardMode, hint)
	}
}

// Different puzzles from the same sender are separate held results; only the
// same puzzle collapses.
func TestPendingResultsAccumulateAcrossPuzzles(t *testing.T) {
	db := migratedDB(t)

	for _, puzzle := range []int{1888, 1889, 1890} {
		if _, err := db.Exec(
			`INSERT INTO pending_results (source, external_id, puzzle_no, solved, guesses)
			 VALUES (?, ?, ?, ?, ?)`,
			"signal", "uuid-1", puzzle, true, 4,
		); err != nil {
			t.Fatalf("insert puzzle %d: %v", puzzle, err)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_results WHERE external_id = ?`, "uuid-1").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("held results = %d, want 3", count)
	}
}

func TestPendingResultsSolvedGuessesConsistency(t *testing.T) {
	db := migratedDB(t)

	// A payload that could never become a result must not be storable as a
	// pending one either, or claiming would fail at replay time instead.
	_, err := db.Exec(
		`INSERT INTO pending_results (source, external_id, puzzle_no, solved, guesses)
		 VALUES (?, ?, ?, ?, ?)`,
		"signal", "uuid-1", 1890, false, 3,
	)
	if err == nil {
		t.Error("stored a failed result with a guess count, want a CHECK failure")
	}
}

// : one external identity maps to exactly one player.
func TestPlayerIdentitiesUniquePerSourceAndExternalID(t *testing.T) {
	db := migratedDB(t)
	first := seedPlayer(t, db, "martin")
	second := seedPlayer(t, db, "alex")

	if _, err := db.Exec(
		`INSERT INTO player_identities (player_id, source, external_id) VALUES (?, ?, ?)`,
		first, "signal", "uuid-1",
	); err != nil {
		t.Fatalf("first identity: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO player_identities (player_id, source, external_id) VALUES (?, ?, ?)`,
		second, "signal", "uuid-1",
	); err == nil {
		t.Error("the same identity mapped to a second player, want a UNIQUE failure")
	}

	// The same external id under a different source is a different identity.
	if _, err := db.Exec(
		`INSERT INTO player_identities (player_id, source, external_id) VALUES (?, ?, ?)`,
		second, "discord", "uuid-1",
	); err != nil {
		t.Errorf("a different source was rejected: %v", err)
	}
}

// display_hint is cosmetic and explicitly not unique: two people can set the
// same Signal profile name, which is why resolution uses the UUID.
func TestPlayerIdentitiesDisplayHintNeedNotBeUnique(t *testing.T) {
	db := migratedDB(t)
	first := seedPlayer(t, db, "martin")
	second := seedPlayer(t, db, "alex")

	for i, playerID := range []int64{first, second} {
		if _, err := db.Exec(
			`INSERT INTO player_identities (player_id, source, external_id, display_hint)
			 VALUES (?, ?, ?, ?)`,
			playerID, "signal", "uuid-"+string(rune('a'+i)), "Same Name",
		); err != nil {
			t.Fatalf("insert identity %d: %v", i, err)
		}
	}
}

func TestPlayersActiveDefaultsTrue(t *testing.T) {
	db := migratedDB(t)
	playerID := seedPlayer(t, db, "martin")

	var active bool
	if err := db.QueryRow(`SELECT active FROM players WHERE id = ?`, playerID).Scan(&active); err != nil {
		t.Fatalf("read active: %v", err)
	}
	if !active {
		t.Error("active defaulted to false, want true")
	}
}

// The single-row constraint is a property of the schema, not a convention
// every caller has to honour.
func TestSettingsIsSingleRow(t *testing.T) {
	db := migratedDB(t)

	if _, err := db.Exec(`INSERT INTO settings (id, share_slug) VALUES (1, ?)`, "abc123"); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO settings (id, share_slug) VALUES (2, ?)`, "def456"); err == nil {
		t.Error("a second settings row was accepted, want a CHECK failure")
	}
}

// : an activity entry must identify its actor unless the system acted.
func TestActivityLogActorConstraint(t *testing.T) {
	db := migratedDB(t)
	userID := seedUser(t, db, "martin@example.tld", true)

	res, err := db.Exec(`INSERT INTO api_tokens (label, token_hash) VALUES (?, ?)`, "import-script", "hash-1")
	if err != nil {
		t.Fatalf("insert token: %v", err)
	}
	tokenID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("token id: %v", err)
	}

	tests := []struct {
		name    string
		kind    string
		userID  any
		tokenID any
		wantErr bool
	}{
		{name: "admin with a user", kind: "admin", userID: userID},
		{name: "player with a user", kind: "player", userID: userID},
		{name: "token with a token", kind: "token", tokenID: tokenID},
		{name: "system with neither", kind: "system"},

		{name: "admin with no actor", kind: "admin", wantErr: true},
		{name: "token with no actor", kind: "token", wantErr: true},
		{name: "system naming a user", kind: "system", userID: userID, wantErr: true},
		{name: "unknown kind", kind: "robot", userID: userID, wantErr: true},

		// An entry naming two actors describes neither.
		{name: "token also naming a user", kind: "token", userID: userID, tokenID: tokenID, wantErr: true},
		{name: "admin also naming a token", kind: "admin", userID: userID, tokenID: tokenID, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.Exec(
				`INSERT INTO activity_log (actor_kind, actor_user_id, actor_token_id, action, subject_type, subject_id)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				tt.kind, tt.userID, tt.tokenID, "result.created", "result", 1,
			)
			if tt.wantErr {
				if err == nil {
					t.Error("insert succeeded, want a CHECK failure")
				}
				return
			}
			if err != nil {
				t.Errorf("insert failed: %v", err)
			}
		})
	}
}

// An activity log that can be erased by deleting a row elsewhere is not a
// log. This is also why a token is revoked via revoked_at, not deletion.
func TestActivityLogBlocksActorDeletion(t *testing.T) {
	db := migratedDB(t)

	userID := seedUser(t, db, "martin@example.tld", true)
	res, err := db.Exec(`INSERT INTO api_tokens (label, token_hash) VALUES (?, ?)`, "import-script", "hash-1")
	if err != nil {
		t.Fatalf("insert token: %v", err)
	}
	tokenID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("token id: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO activity_log (actor_kind, actor_user_id, action, subject_type, subject_id)
		 VALUES ('admin', ?, 'player.created', 'player', 1)`, userID,
	); err != nil {
		t.Fatalf("insert admin activity entry: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO activity_log (actor_kind, actor_token_id, action, subject_type, subject_id)
		 VALUES ('token', ?, 'result.created', 'result', 1)`, tokenID,
	); err != nil {
		t.Fatalf("insert token activity entry: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, userID); err == nil {
		t.Error("deleted a user with activity entries, want RESTRICT")
	}
	if _, err := db.Exec(`DELETE FROM api_tokens WHERE id = ?`, tokenID); err == nil {
		t.Error("deleted a token with activity entries, want RESTRICT; revocation is revoked_at")
	}

	// Revocation, the supported path, must still work.
	if _, err := db.Exec(
		`UPDATE api_tokens SET revoked_at = CURRENT_TIMESTAMP WHERE id = ?`, tokenID,
	); err != nil {
		t.Errorf("revoking a token failed: %v", err)
	}
}

// Sessions and reset tokens are disposable: deleting a user takes them with
// it, which is what makes a user with no results deletable at all.
func TestUserDeletionCascadesSessionsAndTokens(t *testing.T) {
	db := migratedDB(t)
	userID := seedUser(t, db, "martin@example.tld", false)

	if _, err := db.Exec(
		`INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
		[]byte("session-token"), userID, "2027-01-01",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		"reset-hash", userID, "2027-01-01",
	); err != nil {
		t.Fatalf("insert reset token: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	for _, q := range []string{
		`SELECT COUNT(*) FROM sessions WHERE user_id = ?`,
		`SELECT COUNT(*) FROM password_reset_tokens WHERE user_id = ?`,
	} {
		var count int
		if err := db.QueryRow(q, userID).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 0 {
			t.Errorf("%s left %d rows, want 0", q, count)
		}
	}
}

func TestAPITokenHashIsUnique(t *testing.T) {
	db := migratedDB(t)

	if _, err := db.Exec(`INSERT INTO api_tokens (label, token_hash) VALUES (?, ?)`, "import-script", "hash-1"); err != nil {
		t.Fatalf("first token: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO api_tokens (label, token_hash) VALUES (?, ?)`, "other", "hash-1"); err == nil {
		t.Error("two tokens share a hash, want a UNIQUE failure")
	}
}
