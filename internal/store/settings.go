package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

// slugAlphabet excludes vowels and the characters that are easy to confuse
// when a link is read aloud or retyped: 0/o, 1/l/i. The slug is a capability
// posted into a group chat, so it should survive being copied by hand.
const slugAlphabet = "bcdfghjkmnpqrstvwxyz23456789"

// slugLength gives roughly 95 bits of entropy over the alphabet above, which
// is well past guessable for a link whose only job is to be unguessable.
const slugLength = 20

// ErrNoSettings reports that the settings row does not exist yet. Only the
// app creates it, so the CLI seeing this means the app has never
// run against this database.
var ErrNoSettings = errors.New("settings row does not exist")

// GenerateSlug returns a new random share slug.
func GenerateSlug() (string, error) {
	b := make([]byte, slugLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate slug: %w", err)
	}
	// Modulo bias is negligible here: 256 mod 28 leaves a bias under 2% on
	// four of the letters, which does not meaningfully narrow a 95-bit search.
	for i, v := range b {
		b[i] = slugAlphabet[int(v)%len(slugAlphabet)]
	}
	return string(b), nil
}

// ShareSlug returns the current share slug.
func ShareSlug(ctx context.Context, q Querier) (string, error) {
	var slug string
	err := q.QueryRowContext(ctx, `SELECT share_slug FROM settings WHERE id = 1`).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSettings
	}
	if err != nil {
		return "", fmt.Errorf("read share slug: %w", err)
	}
	return slug, nil
}

// EnsureShareSlug creates the settings row with a fresh slug if it does not
// exist, and reports whether it did so.
//
// Only the app calls this, at startup, for the same reason it is the
// only migrator: one writer means two processes cannot generate competing
// slugs on first run. The CLI reads and rotates, but never bootstraps.
func EnsureShareSlug(ctx context.Context, db *sql.DB) (slug string, created bool, err error) {
	err = InTx(ctx, db, func(tx *sql.Tx) error {
		existing, err := ShareSlug(ctx, tx)
		if err == nil {
			slug = existing
			return nil
		}
		if !errors.Is(err, ErrNoSettings) {
			return err
		}

		slug, err = GenerateSlug()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (id, share_slug) VALUES (1, ?)`, slug,
		); err != nil {
			return fmt.Errorf("create settings: %w", err)
		}
		// Recorded so the first slug has the same visible provenance as every
		// later rotation, rather than appearing from nowhere.
		if err := Audit(ctx, tx, SystemActor(), ActionSlugGenerated, SubjectSettings, nil, nil); err != nil {
			return err
		}
		created = true
		return nil
	})
	return slug, created, err
}

// RotateShareSlug replaces the slug with a fresh one and returns it.
//
// Rotating invalidates only the share link. Password-reset links
// live on ordinary paths and are unaffected.
func RotateShareSlug(ctx context.Context, db *sql.DB, actor Actor) (string, error) {
	var slug string
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		previous, err := ShareSlug(ctx, tx)
		if err != nil {
			return err
		}

		slug, err = GenerateSlug()
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE settings SET share_slug = ? WHERE id = 1`, slug)
		if err != nil {
			return fmt.Errorf("rotate share slug: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rotate share slug: %w", err)
		}
		if affected == 0 {
			return ErrNoSettings
		}

		// The previous slug is recorded so a link that stops working can be
		// matched to the rotation that retired it. It is a capability, but a
		// spent one, and the audit log is already admin-only.
		return Audit(ctx, tx, actor, ActionSlugRotated, SubjectSettings, nil,
			map[string]string{"previous_slug": previous})
	})
	if err != nil {
		return "", err
	}
	return slug, nil
}
