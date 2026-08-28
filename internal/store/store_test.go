package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// testDB opens a fresh database in a temporary directory.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAppliesPragmas(t *testing.T) {
	db := testDB(t)

	tests := []struct {
		pragma string
		want   string
	}{
		// WAL is what makes a second process (the CLI) safe alongside the
		// server holding the file open.
		{"journal_mode", "wal"},
		// Off by default in SQLite; the schema's lifecycle rules depend on it.
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
	}
	for _, tt := range tests {
		t.Run(tt.pragma, func(t *testing.T) {
			var got string
			if err := db.QueryRow("PRAGMA " + tt.pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s failed: %v", tt.pragma, err)
			}
			if !strings.EqualFold(got, tt.want) {
				t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
			}
		})
	}
}

func TestMigrateAppliesInOrder(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Deliberately out of lexical order in the map to prove ordering comes
	// from the sort, not from iteration order: 0002 depends on 0001's table.
	fsys := fstest.MapFS{
		"migrations/0002_add_row.sql": &fstest.MapFile{
			Data: []byte(`INSERT INTO widgets (name) VALUES ('one');`),
		},
		"migrations/0001_init.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`),
		},
	}

	if err := Migrate(ctx, db, fsys); err != nil {
		t.Fatalf("Migrate() failed: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("query widgets: %v", err)
	}
	if count != 1 {
		t.Errorf("widgets row count = %d, want 1", count)
	}

	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + migrationsTable).Scan(&applied); err != nil {
		t.Fatalf("query %s: %v", migrationsTable, err)
	}
	if applied != 2 {
		t.Errorf("%s row count = %d, want 2", migrationsTable, applied)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// An INSERT rather than a pure CREATE: a second application would be
	// silently harmless with CREATE TABLE IF NOT EXISTS, but shows up as a
	// duplicate row here.
	fsys := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE widgets (id INTEGER PRIMARY KEY);
			              INSERT INTO widgets (id) VALUES (1);`),
		},
	}

	for i := 0; i < 3; i++ {
		if err := Migrate(ctx, db, fsys); err != nil {
			t.Fatalf("Migrate() run %d failed: %v", i+1, err)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("query widgets: %v", err)
	}
	if count != 1 {
		t.Errorf("widgets row count = %d after three runs, want 1", count)
	}
}

func TestMigrateAppliesOnlyNewFiles(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	first := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE widgets (id INTEGER PRIMARY KEY);`),
		},
	}
	if err := Migrate(ctx, db, first); err != nil {
		t.Fatalf("first Migrate() failed: %v", err)
	}

	// A later release adds a migration; the existing one must not re-run.
	second := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE widgets (id INTEGER PRIMARY KEY);`),
		},
		"migrations/0002_gadgets.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE gadgets (id INTEGER PRIMARY KEY);`),
		},
	}
	if err := Migrate(ctx, db, second); err != nil {
		t.Fatalf("second Migrate() failed: %v", err)
	}

	for _, table := range []string{"widgets", "gadgets"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// The second statement is invalid, so the whole file must roll back and
	// leave no half-applied schema behind.
	fsys := fstest.MapFS{
		"migrations/0001_broken.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE widgets (id INTEGER PRIMARY KEY);
			              CREATE TABLE nonsense (id INTEGER PRIMARY KEY, );`),
		},
	}

	if err := Migrate(ctx, db, fsys); err == nil {
		t.Fatal("Migrate() succeeded on a broken migration, want an error")
	}

	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='widgets'`,
	).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("widgets table exists after a failed migration; want the whole file rolled back (err=%v)", err)
	}

	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + migrationsTable).Scan(&applied); err != nil {
		t.Fatalf("query %s: %v", migrationsTable, err)
	}
	if applied != 0 {
		t.Errorf("%s recorded %d migrations after a failure, want 0", migrationsTable, applied)
	}
}

func TestMigrateEmptySet(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// The real migrations directory is empty at this point in the build; the
	// migrator must still create its tracking table and succeed.
	if err := Migrate(ctx, db, Migrations()); err != nil {
		t.Fatalf("Migrate() with the embedded set failed: %v", err)
	}

	var name string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, migrationsTable,
	).Scan(&name); err != nil {
		t.Fatalf("%s was not created: %v", migrationsTable, err)
	}
}

func TestOpenMigratedRejectsUnmigratedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fresh.db")

	// The CLI must refuse a database the server has never touched, rather
	// than failing later with an opaque "no such table".
	_, err := OpenMigrated(ctx, path)
	if err == nil {
		t.Fatal("OpenMigrated() succeeded on an unmigrated database, want an error")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("error = %v, want it to mention the database is not initialized", err)
	}
	if !strings.Contains(err.Error(), "start the app service") {
		t.Errorf("error = %v, want it to point at starting the app service", err)
	}
}

func TestOpenMigratedAcceptsMigratedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migrated.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	if err := Migrate(ctx, db, Migrations()); err != nil {
		t.Fatalf("Migrate() failed: %v", err)
	}
	db.Close()

	db2, err := OpenMigrated(ctx, path)
	if err != nil {
		t.Fatalf("OpenMigrated() failed on a migrated database: %v", err)
	}
	db2.Close()
}

func TestInTxRollsBackOnError(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sentinel := errors.New("deliberate failure")
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO widgets (id) VALUES (1)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx() error = %v, want the callback's error", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("query widgets: %v", err)
	}
	if count != 0 {
		t.Errorf("widgets row count = %d after a rolled-back transaction, want 0", count)
	}
}

func TestInTxCommitsOnSuccess(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := InTx(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO widgets (id) VALUES (1)`)
		return err
	}); err != nil {
		t.Fatalf("InTx() failed: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("query widgets: %v", err)
	}
	if count != 1 {
		t.Errorf("widgets row count = %d, want 1", count)
	}
}

func TestInTxRollsBackOnPanic(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate out of InTx")
			}
		}()
		_ = InTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO widgets (id) VALUES (1)`); err != nil {
				return err
			}
			panic("deliberate panic")
		})
	}()

	// A panic must not leave the transaction open; with MaxOpenConns(1) an
	// abandoned transaction would deadlock every later query.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("query widgets after panic: %v", err)
	}
	if count != 0 {
		t.Errorf("widgets row count = %d after a panicking transaction, want 0", count)
	}
}

// TestQueryOnPoolDuringTransactionDeadlocks pins the hazard that Querier
// exists to prevent (see querier.go).
//
// With MaxOpenConns(1), a transaction holds the only connection. A query
// issued against the pool before that transaction finishes waits for a
// connection that cannot be released until it does — a permanent deadlock,
// not a slow query a retry would clear. The context deadline here is what
// keeps the test from hanging forever; in production there is no deadline and
// the request never returns.
func TestQueryOnPoolDuringTransactionDeadlocks(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	err := InTx(ctx, db, func(tx *sql.Tx) error {
		// Correct: the transaction is the Querier in scope.
		if _, err := tx.ExecContext(ctx, `INSERT INTO widgets (id) VALUES (1)`); err != nil {
			return err
		}

		// Wrong, and the whole point: reaching past the transaction to the
		// pool. Bounded here only so the test can report rather than hang.
		blockedCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()

		var count int
		err := db.QueryRowContext(blockedCtx, `SELECT COUNT(*) FROM widgets`).Scan(&count)
		if err == nil {
			return errors.New("querying the pool inside a transaction succeeded; " +
				"the single-connection deadlock this package is designed around no longer applies, " +
				"so revisit Querier and the comment on SetMaxOpenConns")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("got %v, want a deadline: the query should block on the connection the transaction holds", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestQuerierAcceptsBothPoolAndTransaction is the compile-time guarantee in
// executable form: one helper, called with each.
func TestQuerierAcceptsBothPoolAndTransaction(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	countWidgets := func(ctx context.Context, q Querier) (int, error) {
		var n int
		err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM widgets`).Scan(&n)
		return n, err
	}

	if _, err := countWidgets(ctx, db); err != nil {
		t.Errorf("helper failed against the pool: %v", err)
	}

	if err := InTx(ctx, db, func(tx *sql.Tx) error {
		_, err := countWidgets(ctx, tx)
		return err
	}); err != nil {
		t.Errorf("helper failed against a transaction: %v", err)
	}
}
