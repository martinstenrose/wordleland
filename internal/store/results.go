package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Outcome reports what an upsert did, so callers can map it to a status code
// and a summary line without inferring it.
type Outcome string

const (
	// OutcomeCreated: no row existed.
	OutcomeCreated Outcome = "created"
	// OutcomeUpdated: a row existed and was overwritten.
	OutcomeUpdated Outcome = "updated"
	// OutcomeIgnored: a row existed and the precedence rule refused the
	// write. This is not an error — a human's value winning is the design.
	OutcomeIgnored Outcome = "ignored"
)

// Result is one player's outcome for one puzzle.
type Result struct {
	PuzzleNo   int
	Date       time.Time
	PlayerID   int64
	Guesses    *int
	Solved     bool
	HardMode   bool
	EnteredBy  *int64
	IdentityID *int64

	// PostedAt is when the result was posted in the Signal group; nil when
	// it was not, or when that is not known. It is the first known posting
	// and never moves: see UpsertResult.
	PostedAt *time.Time
}

// ErrResultNotFound is returned when no row matches.
var ErrResultNotFound = errors.New("result not found")

// UpsertResult writes a result, applying the precedence rule.
//
// A token write may overwrite an existing row only when entered_by IS NULL.
// Anything a human entered — through the CLI, the web UI, or the backfill —
// always wins over an automated value, because the automated source is the
// one that can be wrong at scale: replaying old Signal history would
// otherwise silently revert every correction ever made.
//
// enteredBy nil marks the write as automated. A human write passes the acting
// user and is never refused.
//
// identityID names which claimed identity produced an automated write, so a
// later `identity reassign --move-results` can move exactly the results one
// identity wrote. It is nil for a human write and for automated writes with
// no identity to attribute (there are none today, but the parameter mirrors
// enteredBy's convention regardless).
//
// r.PostedAt is written on a create and, on an update, only into a row that
// has none yet. A player's first posting is a fact about the day that a
// later re-post or correction does not change, but a row first written by
// the API or the CLI and then posted in the group did get posted, and the
// gap is filled rather than left for ever.
func UpsertResult(ctx context.Context, q Querier, r Result, enteredBy, identityID *int64) (Outcome, *Result, error) {
	previous, err := resultFor(ctx, q, r.PuzzleNo, r.PlayerID)
	switch {
	case errors.Is(err, ErrResultNotFound):
		previous = nil
	case err != nil:
		return "", nil, err
	}

	if previous != nil && enteredBy == nil && previous.EnteredBy != nil {
		// A token write against a human-entered row. Refused, and reported as
		// such rather than as a failure.
		return OutcomeIgnored, previous, nil
	}

	if previous == nil {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO results (puzzle_no, date, player_id, guesses, solved, hard_mode, entered_by, identity_id, posted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.PuzzleNo, r.Date.Format(time.DateOnly), r.PlayerID,
			r.Guesses, r.Solved, r.HardMode, enteredBy, identityID, r.PostedAt,
		); err != nil {
			return "", nil, fmt.Errorf("insert result: %w", err)
		}
		return OutcomeCreated, nil, nil
	}

	if _, err := q.ExecContext(ctx, `
		UPDATE results
		SET date = ?, guesses = ?, solved = ?, hard_mode = ?, entered_by = ?, identity_id = ?,
		    posted_at = COALESCE(posted_at, ?)
		WHERE puzzle_no = ? AND player_id = ?`,
		r.Date.Format(time.DateOnly), r.Guesses, r.Solved, r.HardMode, enteredBy, identityID,
		r.PostedAt, r.PuzzleNo, r.PlayerID,
	); err != nil {
		return "", nil, fmt.Errorf("update result: %w", err)
	}
	return OutcomeUpdated, previous, nil
}

// resultFor reads one result.
func resultFor(ctx context.Context, q Querier, puzzleNo int, playerID int64) (*Result, error) {
	// Scanned as time.Time, not string: the driver converts columns declared
	// DATE, so a string scan yields RFC 3339 rather than the YYYY-MM-DD that
	// was written.
	var (
		r    Result
		date time.Time
	)
	err := q.QueryRowContext(ctx, `
		SELECT puzzle_no, date, player_id, guesses, solved, hard_mode, entered_by, identity_id, posted_at
		FROM results WHERE puzzle_no = ? AND player_id = ?`, puzzleNo, playerID,
	).Scan(&r.PuzzleNo, &date, &r.PlayerID, &r.Guesses, &r.Solved, &r.HardMode, &r.EnteredBy,
		&r.IdentityID, &r.PostedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrResultNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read result: %w", err)
	}
	// Re-anchored in the local zone: the stored value is a calendar date, and
	// the driver hands it back at midnight UTC.
	r.Date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
	return &r, nil
}

// ResultFor reads one result.
func ResultFor(ctx context.Context, q Querier, puzzleNo int, playerID int64) (*Result, error) {
	return resultFor(ctx, q, puzzleNo, playerID)
}

// activityDetailFor describes a result change, carrying the previous value on an
// overwrite. That is what makes the log a correction trail rather than a list
// of events.
func activityDetailFor(r Result, previous *Result) map[string]any {
	detail := map[string]any{
		"puzzle_no": r.PuzzleNo,
		"solved":    r.Solved,
		"hard_mode": r.HardMode,
	}
	if r.Guesses != nil {
		detail["guesses"] = *r.Guesses
	}
	if previous != nil {
		prev := map[string]any{"solved": previous.Solved, "hard_mode": previous.HardMode}
		if previous.Guesses != nil {
			prev["guesses"] = *previous.Guesses
		}
		if previous.EnteredBy != nil {
			prev["entered_by"] = *previous.EnteredBy
		}
		detail["previous"] = prev
	}
	return detail
}

// DeleteResult removes a row, restoring the "did not play" state.
//
// Deleting rather than blanking is deliberate: a missed day is the
// absence of a row, distinct from a failure. The consequence, stated, is
// that entered_by no longer protects that puzzle and player, so a later token
// write for it will be accepted.
func DeleteResult(ctx context.Context, db *sql.DB, actor Actor, playerID int64, puzzleNo int) error {
	return InTx(ctx, db, func(tx *sql.Tx) error {
		previous, err := resultFor(ctx, tx, puzzleNo, playerID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM results WHERE puzzle_no = ? AND player_id = ?`, puzzleNo, playerID,
		); err != nil {
			return fmt.Errorf("delete result: %w", err)
		}
		// The row is gone, so the activity entry is the only remaining record of
		// what it held and who removed it.
		return LogActivity(ctx, tx, actor, ActionResultDeleted, SubjectResult, &playerID,
			activityDetailFor(Result{PuzzleNo: puzzleNo}, previous))
	})
}

// LogResultActivity records a result change, carrying the previous value on
// an overwrite so the log reads as a correction trail.
func LogResultActivity(ctx context.Context, q Querier, actor Actor, action string,
	playerID int64, r Result, previous *Result) error {
	return LogResultActivityVia(ctx, q, actor, action, playerID, r, previous, "")
}

// LogResultActivityVia records where the result came in from as well.
//
// The bridge writes as the application itself rather than as a token
// holder — since the services merged it is not an API client any more — so
// "who" alone no longer distinguishes a score that arrived from Signal from
// one the app wrote for another reason. This carries that, without a schema
// change and without pretending a credential was involved.
func LogResultActivityVia(ctx context.Context, q Querier, actor Actor, action string,
	playerID int64, r Result, previous *Result, via string) error {

	detail := activityDetailFor(r, previous)
	if via != "" {
		detail["via"] = via
	}
	return LogActivity(ctx, q, actor, action, SubjectResult, &playerID, detail)
}
