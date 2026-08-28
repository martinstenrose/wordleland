package main

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// swapStdin replaces os.Stdin with a pipe holding content, and restores it.
//
// The password prompt reads from the terminal by design, so there is no flag
// to pass one. Under test stdin is not a terminal, which is the path that
// accepts a single line — the same path a script would use.
func swapStdin(t *testing.T, content string) func() {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	go func() {
		defer w.Close()
		w.WriteString(content)
	}()

	original := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = original
		r.Close()
	}
}

// openForAssert opens the test database for direct inspection.
func openForAssert(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open database for assertions: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
