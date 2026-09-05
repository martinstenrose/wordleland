package store

import (
	"context"
	"io/fs"
	"path"
	"testing"
	"testing/fstest"
)

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

// migrationsBefore returns an fs.FS holding every migration that sorts
// before the named one, so a test can apply the schema as it stood right
// before a given migration.
func migrationsBefore(name string) (fs.FS, error) {
	names, err := migrationNames(Migrations())
	if err != nil {
		return nil, err
	}

	fsys := fstest.MapFS{}
	for _, n := range names {
		if n >= name {
			continue
		}
		body, err := fs.ReadFile(Migrations(), path.Join(migrationsDir, n))
		if err != nil {
			return nil, err
		}
		fsys[path.Join(migrationsDir, n)] = &fstest.MapFile{Data: body}
	}
	return fsys, nil
}
