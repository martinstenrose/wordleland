package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

func runToken(e *env, args []string) error {
	return dispatch(e, "token", []subcommand{
		{"create", "issue an ingest token", tokenCreate},
		{"list", "list tokens", tokenList},
		{"revoke", "revoke a token", tokenRevoke},
	}, args)
}

func tokenCreate(e *env, args []string) error {
	fs := flagSet(e, "token create")
	label := fs.String("label", "", "what this token is for, e.g. import-script")
	expires := fs.Duration("expires", 0, "how long it lasts; omit for no expiry")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*label, "label"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}

	var expiresAt *time.Time
	if *expires > 0 {
		t := time.Now().Add(*expires)
		expiresAt = &t
	}

	plaintext, token, err := store.CreateAPIToken(e.ctx, e.db, actor, *label, expiresAt)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Created token %d (%s).\n\n  %s\n\n", token.ID, token.Label, plaintext)
	// Only the hash is stored, so this is genuinely the only chance to copy
	// it — worth saying plainly rather than letting it be discovered.
	fmt.Fprintln(e.out, "This is shown once and cannot be recovered: only its hash is stored.")
	if expiresAt == nil {
		fmt.Fprintln(e.out, "It does not expire. Revoke it with 'wordleland token revoke'.")
	} else {
		fmt.Fprintf(e.out, "It expires %s.\n", expiresAt.Local().Format(time.RFC1123))
	}
	return nil
}

func tokenList(e *env, args []string) error {
	fs := flagSet(e, "token list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tokens, err := store.ListAPITokens(e.ctx, e.db)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Fprintln(e.out, "No tokens.")
		return nil
	}

	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLABEL\tSTATUS")
	for _, t := range tokens {
		status := "active"
		switch {
		case t.RevokedAt != nil:
			status = "revoked " + t.RevokedAt.Local().Format(time.DateOnly)
		case t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt):
			status = "expired " + t.ExpiresAt.Local().Format(time.DateOnly)
		case t.ExpiresAt != nil:
			status = "expires " + t.ExpiresAt.Local().Format(time.DateOnly)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\n", t.ID, t.Label, status)
	}
	return w.Flush()
}

func tokenRevoke(e *env, args []string) error {
	fs := flagSet(e, "token revoke")
	id := fs.Int64("id", 0, "token id, from 'token list'")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id <= 0 {
		return fmt.Errorf("--id is required")
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	if err := store.RevokeAPIToken(e.ctx, e.db, actor, *id); err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Revoked token %d. It can no longer write results.\n", *id)
	// Revocation is by flag because audit_log references tokens under
	// RESTRICT: what the token did stays on the record.
	fmt.Fprintln(e.out, "The row is kept so its history in the audit log stays intact.")
	return nil
}
