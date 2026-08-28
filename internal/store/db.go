// Package store owns the SQLite database: connection setup, schema
// migrations, and query helpers.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; no cgo, so the build stays static
)

// driverName is the name modernc.org/sqlite registers itself under.
const driverName = "sqlite"

// pragmas are applied on every connection the pool opens, not just the first.
//
//   - journal_mode(WAL) lets the CLI read while the app writes.
//   - busy_timeout is load-bearing rather than tuning: the CLI runs as a second
//     process against the same file, and without it a concurrent write fails
//     immediately with SQLITE_BUSY instead of waiting its turn.
//   - foreign_keys is off by default in SQLite, and the schema leans on FK
//     enforcement to make the lifecycle rules real rather than
//     advisory.
//   - synchronous(NORMAL) is the usual companion to WAL: durable across
//     process crashes, trading only a power-loss window. A scoreboard that
//     can be re-imported from the group chat does not need to defend
//     against that; a payments ledger would.
var pragmas = []string{
	"journal_mode(WAL)",
	"busy_timeout(5000)",
	"foreign_keys(1)",
	"synchronous(NORMAL)",
}

// Open connects to the SQLite database at path and verifies the connection.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	// One connection, for two reasons that belong together.
	//
	// Why: SQLite allows a single writer at a time, and this application
	// writes a handful of rows a day. Serialising in the pool turns lock
	// contention into a queue, rather than into SQLITE_BUSY errors that every
	// caller would have to recognise and retry.
	//
	// What it implies: while a transaction is open it holds the only
	// connection, so any query issued against this *sql.DB before that
	// transaction finishes waits for a connection that cannot be freed until
	// it does. That is a permanent deadlock, not a slow query or a timeout a
	// retry would clear — the request hangs until the process dies. This is
	// why query helpers take a Querier (see querier.go): inside InTx the
	// Querier in scope is the *sql.Tx, so the pool is not reachable by
	// accident. TestQueryOnPoolDuringTransactionDeadlocks pins the hazard.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database %s: %w", path, err)
	}
	return db, nil
}

// dsn builds a modernc.org/sqlite connection string. That driver takes pragmas
// as repeated _pragma query parameters, applying each to every new connection.
func dsn(path string) string {
	params := make(url.Values, len(pragmas))
	for _, p := range pragmas {
		params.Add("_pragma", p)
	}
	return "file:" + path + "?" + params.Encode()
}

// OpenMigrated opens the database and fails if it has not been migrated yet.
//
// The app is the only process that migrates (see Migrate), so the CLI
// uses this: running against an empty file would otherwise produce a confusing
// "no such table" from whichever query happened to run first.
func OpenMigrated(ctx context.Context, path string) (*sql.DB, error) {
	db, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}

	if err := checkMigrated(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return db, nil
}

// checkMigrated reports whether the migration tracking table exists yet.
func checkMigrated(ctx context.Context, q Querier) error {
	var name string
	err := q.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		migrationsTable,
	).Scan(&name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New(
				"database is not initialized — start the app service first, " +
					"which creates the schema on startup")
		}
		return fmt.Errorf("check schema state: %w", err)
	}
	return nil
}

// InTx runs fn inside a transaction, rolling back on error or panic.
//
// Audit entries are written through the same transaction as the change they
// describe, so a crash between the two cannot leave a mutation with
// no record of it. Taking the transaction as a parameter throughout is what
// makes that guarantee mechanical rather than a matter of remembering.
func InTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !strings.Contains(rbErr.Error(), "transaction has already been committed or rolled back") {
				err = fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
