package store

import (
	"context"
	"io/fs"
	"path"
	"testing"
	"testing/fstest"
	"time"
)

// TestPendingMigrations exercises the check serve.go runs before Migrate, to
// decide whether a migration run is starting: it must name what Migrate is
// about to do, then report nothing once that work is done.
func TestPendingMigrations(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	all, err := migrationNames(Migrations())
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("need at least two migrations to apply a subset, have %d", len(all))
	}
	last := all[len(all)-1]

	pre, err := migrationsSubset(all[:len(all)-1])
	if err != nil {
		t.Fatalf("build pre-migration set: %v", err)
	}
	if err := Migrate(ctx, db, pre); err != nil {
		t.Fatalf("apply pre migrations: %v", err)
	}

	pending, err := PendingMigrations(ctx, db, Migrations())
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if len(pending) != 1 || pending[0] != last {
		t.Errorf("pending = %v, want [%s]", pending, last)
	}

	if err := Migrate(ctx, db, Migrations()); err != nil {
		t.Fatalf("apply remaining migrations: %v", err)
	}

	pending, err = PendingMigrations(ctx, db, Migrations())
	if err != nil {
		t.Fatalf("PendingMigrations after Migrate: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending after Migrate = %v, want none", pending)
	}
}

// TestPendingMigrationsOnFreshDatabase covers the case PendingMigrations
// exists for: called before Migrate has ever run, when schema_migrations
// itself does not exist yet. Every migration should read as pending, not
// error out on the missing tracking table.
func TestPendingMigrationsOnFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	want, err := migrationNames(Migrations())
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}

	pending, err := PendingMigrations(ctx, db, Migrations())
	if err != nil {
		t.Fatalf("PendingMigrations on fresh database: %v", err)
	}
	if len(pending) != len(want) {
		t.Errorf("pending = %v, want %v", pending, want)
	}
}

// TestMigrateRenamesActivityLogTable exercises the 0009 migration against a
// database that already has data in the old audit_log table, which
// migratedDB's "apply everything to a fresh database" helper never does.
// The point of the rename is that history is not lost — a row seeded before
// the migration must still be there under the new name afterward.
func TestMigrateRenamesActivityLogTable(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	// Everything up to, but not including, the rename.
	pre, err := migrationsBefore("0009_rename_activity_log.sql")
	if err != nil {
		t.Fatalf("build pre-rename migration set: %v", err)
	}
	if err := Migrate(ctx, db, pre); err != nil {
		t.Fatalf("apply pre-rename migrations: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO audit_log (actor_kind, action, subject_type, subject_id)
		 VALUES ('system', 'settings.slug_generated', 'settings', NULL)`,
	); err != nil {
		t.Fatalf("seed audit_log row: %v", err)
	}

	// The full set, including 0009. Migrate skips what pre already applied.
	if err := Migrate(ctx, db, Migrations()); err != nil {
		t.Fatalf("apply remaining migrations: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_log`).Scan(&count); err != nil {
		t.Fatalf("count activity_log: %v", err)
	}
	if count != 1 {
		t.Errorf("activity_log count = %d, want 1 — the seeded row must survive the rename", count)
	}

	var name string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'audit_log'`,
	).Scan(&name)
	if err == nil {
		t.Error("audit_log still exists after the rename")
	}

	for _, index := range []string{"idx_activity_log_at", "idx_activity_log_subject"} {
		var found string
		if err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index,
		).Scan(&found); err != nil {
			t.Errorf("index %s not found after migration: %v", index, err)
		}
	}
}

// migrationsSubset returns an fs.FS holding exactly the named migrations,
// read from the embedded set, so a test can apply the schema as it stood
// after only some of them.
func migrationsSubset(names []string) (fs.FS, error) {
	fsys := fstest.MapFS{}
	for _, n := range names {
		body, err := fs.ReadFile(Migrations(), path.Join(migrationsDir, n))
		if err != nil {
			return nil, err
		}
		fsys[path.Join(migrationsDir, n)] = &fstest.MapFile{Data: body}
	}
	return fsys, nil
}

// migrationsBefore returns an fs.FS holding every migration that sorts
// before the named one, so a test can apply the schema as it stood right
// before a given migration.
func migrationsBefore(name string) (fs.FS, error) {
	names, err := migrationNames(Migrations())
	if err != nil {
		return nil, err
	}

	var before []string
	for _, n := range names {
		if n < name {
			before = append(before, n)
		}
	}
	return migrationsSubset(before)
}

// The posting time is backfilled from the activity trail, and only from
// rows that can vouch for a Signal posting: result.created, naming the
// puzzle, with the source recorded. Anything less stays NULL rather than
// being guessed.
func TestMigrateBackfillsPostedAtFromTheActivityLog(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	pre, err := migrationsBefore("0012_result_posted_at.sql")
	if err != nil {
		t.Fatalf("build pre-backfill migration set: %v", err)
	}
	if err := Migrate(ctx, db, pre); err != nil {
		t.Fatalf("apply pre-backfill migrations: %v", err)
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO players (id, name, slug, active) VALUES (1, 'Alice', 'alice', 1), (2, 'Bob', 'bob', 1)`)
	for _, puzzle := range []int{1888, 1889, 1890, 1891} {
		exec(`INSERT INTO results (puzzle_no, date, player_id, guesses, solved) VALUES (?, '2026-08-23', 1, 4, 1)`, puzzle)
	}
	exec(`INSERT INTO results (puzzle_no, date, player_id, guesses, solved) VALUES (1888, '2026-08-23', 2, 4, 1)`)

	// 1888: posted, then re-posted — the first time is the posting.
	exec(`INSERT INTO activity_log (at, actor_kind, action, subject_type, subject_id, detail)
	      VALUES ('2026-08-23 06:12:00', 'system', 'result.created', 'result', 1,
	              '{"puzzle_no":1888,"guesses":4,"solved":true,"via":"signal"}')`)
	exec(`INSERT INTO activity_log (at, actor_kind, action, subject_type, subject_id, detail)
	      VALUES ('2026-08-23 08:00:00', 'system', 'result.updated', 'result', 1,
	              '{"puzzle_no":1888,"guesses":3,"solved":true,"via":"signal"}')`)
	// 1889: created with no recorded source — the pre-merge token era.
	exec(`INSERT INTO activity_log (at, actor_kind, action, subject_type, subject_id, detail)
	      VALUES ('2026-08-24 07:00:00', 'system', 'result.created', 'result', 1,
	              '{"puzzle_no":1889,"guesses":4,"solved":true}')`)
	// 1890: only ever corrected through the group; creation is unknown.
	exec(`INSERT INTO activity_log (at, actor_kind, action, subject_type, subject_id, detail)
	      VALUES ('2026-08-25 07:00:00', 'system', 'result.updated', 'result', 1,
	              '{"puzzle_no":1890,"guesses":4,"solved":true,"via":"signal"}')`)
	// Bob's 1888 posting must not stamp Alice's row.
	exec(`INSERT INTO activity_log (at, actor_kind, action, subject_type, subject_id, detail)
	      VALUES ('2026-08-23 05:00:00', 'system', 'result.created', 'result', 2,
	              '{"puzzle_no":1888,"guesses":4,"solved":true,"via":"signal"}')`)

	if err := Migrate(ctx, db, Migrations()); err != nil {
		t.Fatalf("apply remaining migrations: %v", err)
	}

	postedAt := func(puzzle int, player int64) *time.Time {
		t.Helper()
		var at *time.Time
		if err := db.QueryRowContext(ctx,
			`SELECT posted_at FROM results WHERE puzzle_no = ? AND player_id = ?`, puzzle, player,
		).Scan(&at); err != nil {
			t.Fatalf("read posted_at for %d/%d: %v", puzzle, player, err)
		}
		return at
	}

	if got := postedAt(1888, 1); got == nil || !got.Equal(time.Date(2026, time.August, 23, 6, 12, 0, 0, time.UTC)) {
		t.Errorf("1888 posted_at = %v, want the creation time 06:12 UTC", got)
	}
	if got := postedAt(1888, 2); got == nil || !got.Equal(time.Date(2026, time.August, 23, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("Bob's 1888 posted_at = %v, want 05:00 UTC", got)
	}
	for _, puzzle := range []int{1889, 1890, 1891} {
		if got := postedAt(puzzle, 1); got != nil {
			t.Errorf("%d posted_at = %v, want NULL: nothing vouches for a Signal posting", puzzle, got)
		}
	}

	// The held-result table carries the column too, so a replay can hand
	// the time on.
	if _, err := db.ExecContext(ctx, `SELECT posted_at FROM pending_results`); err != nil {
		t.Errorf("pending_results.posted_at: %v", err)
	}
}
