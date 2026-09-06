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
