package store

import (
	"context"
	"database/sql"
)

// Querier is the read/write surface shared by *sql.DB and *sql.Tx.
//
// Every query helper in this package takes a Querier rather than a *sql.DB,
// which is a deadlock defence rather than a style preference. The pool is
// capped at one connection (see Open), so a helper that reached for *sql.DB
// while a transaction held that connection would wait forever for a
// connection only the transaction can release. Taking a Querier means code
// inside InTx is handed the *sql.Tx and has no route back to the pool: the
// call simply does not compile unless a Querier is passed, and the one in
// scope inside a transaction is the transaction.
//
// The one hole this cannot close is a helper that captures a *sql.DB in a
// closure instead of accepting it as a parameter. Nothing in Go prevents
// that, so it stays a review point — but it is a visibly odd thing to write,
// where "pass the pool because it was in scope" is an easy accident.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Both the pool and a transaction satisfy Querier; if either ever stops
// doing so, this fails at compile time rather than at the first call site.
var (
	_ Querier = (*sql.DB)(nil)
	_ Querier = (*sql.Tx)(nil)
)
