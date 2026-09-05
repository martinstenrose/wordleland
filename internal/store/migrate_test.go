package store

import (
	"context"
	"io/fs"
	"path"
	"testing"
	"testing/fstest"
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
