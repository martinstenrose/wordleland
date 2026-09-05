// Package ingest files one Wordle result, however it was named.
//
// It exists because two callers need the same rules: the HTTP endpoint that
// token holders POST to, and the Signal bridge, which since the services
// were merged calls this directly rather than talking to itself over the
// network. Keeping the rules here is what stops the two paths drifting —
// the precedence rule and the held-result behaviour are the parts that
// quietly lose scores when they disagree.
package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// ValidationError is a submission the caller got wrong — a missing field, an
// impossible score, two ways of naming one player. Typed so an HTTP caller
// can be told what it did rather than given a bare 500, and so the bridge
// can log it as a parser bug rather than retrying it forever.
type ValidationError struct{ err error }

func (e *ValidationError) Error() string { return e.err.Error() }
func (e *ValidationError) Unwrap() error { return e.err }

func invalid(format string, args ...any) error {
	return &ValidationError{err: fmt.Errorf(format, args...)}
}

// ErrNoSuchPlayer is a player named directly — by id or slug — that does not
// exist. Distinct from an unrecognised sender, which is not an error: see
// StatusPending.
var ErrNoSuchPlayer = errors.New("no such player")

// Status is what happened to the result.
type Status string

const (
	StatusCreated Status = "created"
	StatusUpdated Status = "updated"
	// StatusIgnored: a row existed and the precedence rule kept it. A
	// human-entered value always beats an automated one.
	StatusIgnored Status = "ignored"
	// StatusPending: the sender resolves to no player yet, so the payload is
	// held in full and replayed when somebody claims them. Nothing is lost,
	// which is why this is not an error.
	StatusPending Status = "pending"
)

// Submission is one result and the way its player was named.
//
// Exactly one naming method: the sender pair, an id, or a slug. Ambiguity is
// refused rather than resolved by precedence — a caller sending two
// identifiers has a bug, and picking one would hide it.
type Submission struct {
	Source, ExternalID string
	PlayerID           *int64
	Slug               string

	// DisplayHint is the name the sender currently posts under. Kept only so
	// a human can tell which identity is whom; never used to resolve one.
	DisplayHint string

	PuzzleNo int
	Solved   bool
	Guesses  *int
	HardMode bool

	// Via names where the result came from, recorded in the activity detail so
	// the activity log can say how a score arrived. Empty for a direct API
	// call, which is already attributed to its token.
	Via string
}

// Method reports how the player was named, and refuses a submission that
// names none or several.
func (s Submission) Method() (string, error) {
	var methods []string
	if s.Source != "" || s.ExternalID != "" {
		methods = append(methods, "sender")
	}
	if s.PlayerID != nil {
		methods = append(methods, "player_id")
	}
	if s.Slug != "" {
		methods = append(methods, "slug")
	}

	switch len(methods) {
	case 0:
		return "", invalid("name the player with source and external_id, or with player_id or slug")
	case 1:
		if methods[0] == "sender" && (s.Source == "" || s.ExternalID == "") {
			return "", invalid("source and external_id must be given together")
		}
		return methods[0], nil
	default:
		return "", invalid("name the player only once, got %s", joinAnd(methods))
	}
}

// Validate checks the score itself, independently of who it belongs to.
func (s Submission) Validate() error {
	if s.PuzzleNo <= 0 {
		return invalid("puzzle_no must be a positive number")
	}
	if s.Solved {
		if s.Guesses == nil {
			return invalid("guesses is required when solved is true")
		}
		if *s.Guesses < 1 || *s.Guesses > wordle.MaxGuesses {
			return invalid("guesses must be between 1 and %d", wordle.MaxGuesses)
		}
		return nil
	}
	if s.Guesses != nil {
		return invalid("guesses must be omitted when solved is false")
	}
	return nil
}

// Apply files the result.
//
// mayReactivate says whether this arrival is evidence that a retired player
// has returned. True only for a live post: replayed and backfilled results
// are historical and say nothing about the present.
func Apply(ctx context.Context, db *sql.DB, actor store.Actor, sub Submission, mayReactivate bool) (Status, error) {
	method, err := sub.Method()
	if err != nil {
		return "", err
	}
	if err := sub.Validate(); err != nil {
		return "", err
	}

	if method == "sender" {
		return applyFromSender(ctx, db, actor, sub, mayReactivate)
	}

	var player store.Player
	if method == "player_id" {
		player, err = store.PlayerByID(ctx, db, *sub.PlayerID)
	} else {
		player, err = store.PlayerBySlug(ctx, db, sub.Slug)
	}
	if errors.Is(err, store.ErrPlayerNotFound) {
		// Nothing is stored: a bad id or a mistyped slug is a mistake by the
		// caller, not a sender we have yet to meet.
		return "", ErrNoSuchPlayer
	}
	if err != nil {
		return "", err
	}

	// Naming a player directly is an admin or a script, which says nothing
	// about whether that person has rejoined the group.
	return write(ctx, db, actor, player, sub, false)
}

func applyFromSender(ctx context.Context, db *sql.DB, actor store.Actor,
	sub Submission, mayReactivate bool) (Status, error) {

	player, err := store.ResolveIdentity(ctx, db, sub.Source, sub.ExternalID)
	if errors.Is(err, store.ErrIdentityNotFound) {
		held := store.PendingResult{
			PuzzleNo: sub.PuzzleNo, Solved: sub.Solved,
			Guesses: sub.Guesses, HardMode: sub.HardMode,
		}
		if err := store.HoldPendingResult(ctx, db,
			sub.Source, sub.ExternalID, sub.DisplayHint, held); err != nil {
			return "", err
		}
		return StatusPending, nil
	}
	if err != nil {
		return "", err
	}

	if err := store.RefreshDisplayHint(ctx, db, sub.Source, sub.ExternalID, sub.DisplayHint); err != nil {
		// Cosmetic, so it must not cost a result. Reported to the caller's
		// logger is not worth the plumbing; the next post refreshes it.
		_ = err
	}

	return write(ctx, db, actor, player, sub, mayReactivate)
}

func write(ctx context.Context, db *sql.DB, actor store.Actor,
	player store.Player, sub Submission, mayReactivate bool) (Status, error) {

	date, err := wordle.DateForPuzzle(sub.PuzzleNo)
	if err != nil {
		return "", &ValidationError{err: err}
	}

	result := store.Result{
		PuzzleNo: sub.PuzzleNo,
		Date:     date,
		PlayerID: player.ID,
		Guesses:  sub.Guesses,
		Solved:   sub.Solved,
		HardMode: sub.HardMode,
	}

	var outcome store.Outcome
	err = store.InTx(ctx, db, func(tx *sql.Tx) error {
		if mayReactivate && !player.Active {
			if err := store.ReactivatePlayer(ctx, tx, actor, player.ID); err != nil {
				return err
			}
		}

		var previous *store.Result
		outcome, previous, err = store.UpsertResult(ctx, tx, result, nil)
		if err != nil {
			return err
		}
		if outcome == store.OutcomeIgnored {
			return nil
		}

		action := store.ActionResultCreated
		if outcome == store.OutcomeUpdated {
			action = store.ActionResultUpdated
		}
		return store.LogResultActivityVia(ctx, tx, actor, action, player.ID, result, previous, sub.Via)
	})
	if err != nil {
		return "", err
	}
	return Status(outcome), nil
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return parts[0] + " and " + joinAnd(parts[1:])
}
