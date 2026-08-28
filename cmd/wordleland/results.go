package main

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

func runResults(e *env, args []string) error {
	return dispatch(e, "results", []subcommand{
		{"set", "record or correct a result", resultsSet},
		{"unset", "delete a result, restoring the did-not-play state", resultsUnset},
	}, args)
}

func resultsSet(e *env, args []string) error {
	fs := flagSet(e, "results set")
	player := fs.String("player", "", "slug of the player")
	puzzle := fs.Int("puzzle", 0, "Wordle puzzle number")
	guesses := fs.Int("guesses", 0, "guesses taken, 1-6")
	failed := fs.Bool("failed", false, "the player did not solve it")
	hardMode := fs.Bool("hard-mode", false, "played in hard mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*player, "player"); err != nil {
		return err
	}
	if *puzzle <= 0 {
		return errors.New("--puzzle is required")
	}
	if *failed == (*guesses > 0) {
		return errors.New("give either --guesses or --failed")
	}
	if !*failed && (*guesses < 1 || *guesses > 6) {
		return errors.New("--guesses must be between 1 and 6")
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	target, err := e.lookupPlayer(*player)
	if err != nil {
		return err
	}
	date, err := wordle.DateForPuzzle(*puzzle)
	if err != nil {
		return err
	}

	result := store.Result{
		PuzzleNo: *puzzle,
		Date:     date,
		PlayerID: target.ID,
		Solved:   !*failed,
		HardMode: *hardMode,
	}
	if !*failed {
		result.Guesses = guesses
	}

	// entered_by is the acting admin, which locks the row against later
	// automated overwrites — the whole point of correcting by hand.
	outcome, err := e.writeResult(actor, target.ID, result)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.out, "%s %s for puzzle %d: %s.\n",
		pastTense(outcome), target.Slug, *puzzle, describeResult(result))
	fmt.Fprintln(e.out, "This value now wins over anything the Signal bridge sends for that puzzle.")
	return nil
}

func describeResult(r store.Result) string {
	suffix := ""
	if r.HardMode {
		suffix = " (hard mode)"
	}
	if !r.Solved {
		return "failed" + suffix
	}
	return fmt.Sprintf("%d guesses%s", *r.Guesses, suffix)
}

func pastTense(o store.Outcome) string {
	if o == store.OutcomeCreated {
		return "Recorded"
	}
	return "Updated"
}

func resultsUnset(e *env, args []string) error {
	fs := flagSet(e, "results unset")
	player := fs.String("player", "", "slug of the player")
	puzzle := fs.Int("puzzle", 0, "Wordle puzzle number")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*player, "player"); err != nil {
		return err
	}
	if *puzzle <= 0 {
		return errors.New("--puzzle is required")
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	target, err := e.lookupPlayer(*player)
	if err != nil {
		return err
	}

	if err := store.DeleteResult(e.ctx, e.db, actor, target.ID, *puzzle); err != nil {
		if errors.Is(err, store.ErrResultNotFound) {
			return fmt.Errorf("%s has no result for puzzle %d", target.Slug, *puzzle)
		}
		return err
	}

	fmt.Fprintf(e.out, "Deleted %s's result for puzzle %d. They now count as not having played.\n",
		target.Slug, *puzzle)
	// Stated because it is the one way a hand-entered value stops winning.
	fmt.Fprintln(e.out, "Note: the Signal bridge can now write a result for that puzzle again.")
	return nil
}

// writeResult stores a hand-entered result and audits it in one transaction.
func (e *env) writeResult(actor store.Actor, playerID int64, r store.Result) (store.Outcome, error) {
	var outcome store.Outcome
	err := store.InTx(e.ctx, e.db, func(tx *sql.Tx) error {
		var (
			previous *store.Result
			err      error
		)
		userID := actor.UserID
		outcome, previous, err = store.UpsertResult(e.ctx, tx, r, userID)
		if err != nil {
			return err
		}
		action := store.ActionResultCreated
		if outcome == store.OutcomeUpdated {
			action = store.ActionResultUpdated
		}
		return store.AuditResult(e.ctx, tx, actor, action, playerID, r, previous)
	})
	return outcome, err
}
