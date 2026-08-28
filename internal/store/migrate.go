package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

// migrationsTable records which migration files have been applied. The
// migrator creates it itself rather than shipping it as migration 0001, so
// there is no chicken-and-egg problem in tracking the tracker.
const migrationsTable = "schema_migrations"

// migrationsDir is the directory inside migrationsFS holding the .sql files.
const migrationsDir = "migrations"

// migrationsFS holds the schema as plain numbered .sql files, embedded so a
// deployed binary carries its own schema and needs no files on disk.
//
// The all: prefix is what lets this compile while the directory holds only
// .gitkeep; a plain migrations/*.sql pattern is a build error when nothing
// matches yet.
//
//go:embed all:migrations
var migrationsFS embed.FS

// Migrations returns the embedded migration set. It exists so tests can
// substitute a different fs.FS.
func Migrations() fs.FS { return migrationsFS }

// Migrate applies every migration in fsys that has not been applied yet, in
// filename order, and records each one.
//
// Migrations are forward-only. There are no down migrations: rolling a schema
// backwards on a live database is a restore-from-backup operation in practice,
// and a down migration mostly provides false confidence that it is not.
//
// Only the app calls this. Having a single migrating process
// avoids two of them racing on first start without needing a lock, and matches
// how the CLI is actually run — inside the already-migrated app
// container.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS `+migrationsTable+` (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("create %s: %w", migrationsTable, err)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	names, err := migrationNames(fsys)
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := fs.ReadFile(fsys, path.Join(migrationsDir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		// Each migration is one transaction, so a failure halfway through a
		// file leaves the schema untouched rather than half-changed.
		if err := InTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO `+migrationsTable+` (version) VALUES (?)`, name,
			); err != nil {
				return fmt.Errorf("record migration %s: %w", name, err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, q Querier) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT version FROM `+migrationsTable)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", migrationsTable, err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan %s: %w", migrationsTable, err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", migrationsTable, err)
	}
	return applied, nil
}

// migrationNames lists the .sql files in fsys, sorted. Names are zero-padded
// and fixed-width by convention (0001_init.sql), so lexical order is numeric
// order.
func migrationNames(fsys fs.FS) ([]string, error) {
	entries, err := fs.Glob(fsys, path.Join(migrationsDir, "*.sql"))
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, path.Base(e))
	}
	sort.Strings(names)
	return names, nil
}
