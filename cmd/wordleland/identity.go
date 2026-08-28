package main

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

func runIdentity(e *env, args []string) error {
	return dispatch(e, "identity", []subcommand{
		{"pending", "list senders whose results are held", identityPending},
		{"claim", "map a held sender to a player and replay their results", identityClaim},
		{"discard", "drop a sender's held results without creating a player", identityDiscard},
		{"add", "map a sender to a player directly", identityAdd},
	}, args)
}

func identityPending(e *env, args []string) error {
	fs := flagSet(e, "identity pending")
	if err := fs.Parse(args); err != nil {
		return err
	}

	senders, err := store.ListPendingSenders(e.ctx, e.db)
	if err != nil {
		return err
	}
	if len(senders) == 0 {
		fmt.Fprintln(e.out, "No unclaimed senders.")
		return nil
	}

	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tEXTERNAL ID\tSEEN AS\tHELD\tFIRST\tLAST")
	for _, s := range senders {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			s.Source, s.ExternalID, orDash(s.DisplayHint), s.Count,
			s.FirstSeen.Local().Format(time.DateOnly),
			s.LastSeen.Local().Format(time.DateOnly))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(e.out, "\nClaim one with: wordleland identity claim --player <slug> --source <source> --external-id <id>")
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func identityClaim(e *env, args []string) error {
	return linkIdentity(e, args, "claim", store.ActionIdentityClaimed, true)
}

func identityAdd(e *env, args []string) error {
	return linkIdentity(e, args, "add", store.ActionIdentityAdded, false)
}

// linkIdentity backs both claim and add. They differ only in whether held
// results are required: claim is for a sender seen in the group, add is for
// one whose id is already known. Both replay, because replay is a property of
// creating the mapping — otherwise add would orphan anything held.
func linkIdentity(e *env, args []string, verb, action string, requireHeld bool) error {
	fs := flagSet(e, "identity "+verb)
	player := fs.String("player", "", "slug of the player")
	source := fs.String("source", "signal", "identity source")
	externalID := fs.String("external-id", "", "the sender's stable id; for Signal, their account UUID")
	dryRun := fs.Bool("dry-run", false, "report what would happen without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*player, "player"); err != nil {
		return err
	}
	if err := requireFlag(*externalID, "external-id"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	target, err := e.lookupPlayer(*player)
	if err != nil {
		return err
	}

	if requireHeld {
		held, _, err := store.PendingResultsFor(e.ctx, e.db, *source, *externalID)
		if err != nil {
			return err
		}
		if len(held) == 0 {
			return fmt.Errorf("nothing held for %s/%s.\n"+
				"Run 'wordleland identity pending' to see who has posted, "+
				"or use 'identity add' if you already know the id",
				*source, *externalID)
		}
	}

	summary, err := store.LinkIdentity(e.ctx, e.db, actor, target.ID, *source, *externalID, action, *dryRun)
	if errors.Is(err, store.ErrIdentityTaken) {
		return fmt.Errorf("%s/%s is already mapped to a player", *source, *externalID)
	}
	if err != nil {
		return err
	}

	prefix := ""
	if *dryRun {
		prefix = "Would have "
	}
	fmt.Fprintf(e.out, "%slinked %s/%s to %s.\n", prefix, *source, *externalID, target.Slug)
	fmt.Fprintf(e.out, "  %d result(s) replayed, %d updated, %d skipped.\n",
		summary.Replayed, summary.Updated, summary.Skipped)

	if summary.Skipped > 0 {
		// Skipped rows are discarded, so say why rather than leaving a
		// number to be puzzled over later. The tense follows the mode: a dry
		// run has dropped nothing yet.
		dropped := "Their held copies have been dropped, since they can never apply."
		if *dryRun {
			dropped = "Their held copies would be dropped, since they can never apply."
		}
		fmt.Fprintln(e.out,
			"  Skipped results already had a hand-entered value, which wins.\n  "+dropped)
	}
	if *dryRun {
		fmt.Fprintln(e.out, "\nNothing was written. Re-run without --dry-run to apply.")
	}
	return nil
}

func identityDiscard(e *env, args []string) error {
	fs := flagSet(e, "identity discard")
	source := fs.String("source", "signal", "identity source")
	externalID := fs.String("external-id", "", "the sender's stable id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*externalID, "external-id"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}

	discarded, err := store.DiscardPendingResults(e.ctx, e.db, actor, *source, *externalID)
	if errors.Is(err, store.ErrNoPendingResults) {
		return fmt.Errorf("nothing held for %s/%s", *source, *externalID)
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Discarded %d held result(s) for %s/%s. No player was created.\n",
		discarded, *source, *externalID)
	return nil
}
