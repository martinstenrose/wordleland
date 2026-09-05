package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/martinstenrose/wordleland/internal/wordle"
)

var (
	// ErrIdentityNotFound reports that a sender resolves to no player.
	ErrIdentityNotFound = errors.New("identity not found")
	// ErrIdentityTaken reports that an identity already maps to a player.
	ErrIdentityTaken = errors.New("identity already claimed")
	// ErrNoPendingResults reports that a sender has nothing held.
	ErrNoPendingResults = errors.New("no held results for that sender")
)

// PendingSender aggregates what is held for one unclaimed sender, which is
// what `identity pending` lists.
type PendingSender struct {
	Source      string
	ExternalID  string
	DisplayHint string
	FirstSeen   time.Time
	LastSeen    time.Time
	Count       int
}

// PendingResult is one held payload.
type PendingResult struct {
	PuzzleNo int
	Solved   bool
	Guesses  *int
	HardMode bool
}

// ResolveIdentity maps a sender to a player.
func ResolveIdentity(ctx context.Context, q Querier, source, externalID string) (Player, error) {
	var p Player
	err := q.QueryRowContext(ctx, `
		SELECT p.id, p.slug, p.name, p.user_id, p.active
		FROM player_identities i
		JOIN players p ON p.id = i.player_id
		WHERE i.source = ? AND i.external_id = ?`, source, externalID,
	).Scan(&p.ID, &p.Slug, &p.Name, &p.UserID, &p.Active)

	if errors.Is(err, sql.ErrNoRows) {
		return Player{}, ErrIdentityNotFound
	}
	if err != nil {
		return Player{}, fmt.Errorf("resolve identity: %w", err)
	}
	return p, nil
}

// RefreshDisplayHint updates the human-readable label for an identity.
//
// Cosmetic only: it is never used for resolution, which is why a sender
// changing their profile name cannot break the mapping.
func RefreshDisplayHint(ctx context.Context, q Querier, source, externalID, hint string) error {
	if hint == "" {
		return nil
	}
	if _, err := q.ExecContext(ctx, `
		UPDATE player_identities SET display_hint = ?
		WHERE source = ? AND external_id = ? AND COALESCE(display_hint, '') != ?`,
		hint, source, externalID, hint); err != nil {
		return fmt.Errorf("refresh display hint: %w", err)
	}
	return nil
}

// HoldPendingResult stores a payload from an unclaimed sender.
//
// The full result is kept rather than a sighting count, so claiming the sender
// later recovers everything that arrived meanwhile. A repost of the same
// puzzle overwrites.
func HoldPendingResult(ctx context.Context, q Querier, source, externalID, hint string, r PendingResult) error {
	if _, err := q.ExecContext(ctx, `
		INSERT INTO pending_results
			(source, external_id, display_hint, puzzle_no, solved, guesses, hard_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, external_id, puzzle_no) DO UPDATE SET
			solved       = excluded.solved,
			guesses      = excluded.guesses,
			hard_mode    = excluded.hard_mode,
			display_hint = excluded.display_hint,
			received_at  = CURRENT_TIMESTAMP`,
		source, externalID, nullIfEmpty(hint), r.PuzzleNo, r.Solved, r.Guesses, r.HardMode,
	); err != nil {
		return fmt.Errorf("hold pending result: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ListPendingSenders returns held results aggregated by sender.
func ListPendingSenders(ctx context.Context, q Querier) ([]PendingSender, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT source, external_id,
		       COALESCE(MAX(display_hint), ''),
		       MIN(received_at), MAX(received_at), COUNT(*)
		FROM pending_results
		GROUP BY source, external_id
		ORDER BY MAX(received_at) DESC`)
	if err != nil {
		return nil, fmt.Errorf("list pending senders: %w", err)
	}
	defer rows.Close()

	var senders []PendingSender
	for rows.Next() {
		var (
			s                   PendingSender
			firstSeen, lastSeen string
		)
		// Scanned as strings: an aggregate loses the column's declared type,
		// so the driver stops converting DATE and TIMESTAMP for it and hands
		// back whatever SQLite stored.
		if err := rows.Scan(&s.Source, &s.ExternalID, &s.DisplayHint,
			&firstSeen, &lastSeen, &s.Count); err != nil {
			return nil, fmt.Errorf("scan pending sender: %w", err)
		}
		if s.FirstSeen, err = parseTimestamp(firstSeen); err != nil {
			return nil, err
		}
		if s.LastSeen, err = parseTimestamp(lastSeen); err != nil {
			return nil, err
		}
		senders = append(senders, s)
	}
	return senders, rows.Err()
}

// timestampLayouts covers what SQLite may hold in a TIMESTAMP column:
// CURRENT_TIMESTAMP writes the space-separated form, while a value written
// through the driver from a time.Time arrives as RFC 3339.
var timestampLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

func parseTimestamp(v string) (time.Time, error) {
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp %q", v)
}

// ReplaySummary reports what linking an identity did with the held results.
type ReplaySummary struct {
	Replayed int
	Updated  int
	// Skipped counts held results the precedence rule refused, because a
	// human had already entered a value for that puzzle. Counted separately
	// because the pending rows are then discarded, and discarding data
	// silently is not acceptable even when it is the right thing to do.
	Skipped int
}

// LinkIdentity maps a sender to a player and replays everything held for them.
//
// The identity row, every result write, the pending deletes and the activity
// entries are one transaction. A crash midway would otherwise leave an
// identity that exists with results half-replayed — and re-running would not
// recover, because claiming refuses a sender that already resolves.
//
// Replayed rows carry entered_by NULL. They originated from a token, so they
// must remain overwritable by a later token write, and must never overwrite a
// value entered by hand. dryRun reports without writing.
func LinkIdentity(ctx context.Context, db *sql.DB, actor Actor, playerID int64,
	source, externalID, action string, dryRun bool) (ReplaySummary, error) {

	var summary ReplaySummary
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := ResolveIdentity(ctx, tx, source, externalID); err == nil {
			return ErrIdentityTaken
		} else if !errors.Is(err, ErrIdentityNotFound) {
			return err
		}

		held, hint, err := pendingResultsFor(ctx, tx, source, externalID)
		if err != nil {
			return err
		}

		if !dryRun {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO player_identities (player_id, source, external_id, display_hint)
				VALUES (?, ?, ?, ?)`, playerID, source, externalID, nullIfEmpty(hint)); err != nil {
				if isUniqueViolation(err) {
					return ErrIdentityTaken
				}
				return fmt.Errorf("create identity: %w", err)
			}
		}

		for _, held := range held {
			date, err := wordle.DateForPuzzle(held.PuzzleNo)
			if err != nil {
				return fmt.Errorf("puzzle %d: %w", held.PuzzleNo, err)
			}
			result := Result{
				PuzzleNo: held.PuzzleNo,
				Date:     date,
				PlayerID: playerID,
				Guesses:  held.Guesses,
				Solved:   held.Solved,
				HardMode: held.HardMode,
			}

			if dryRun {
				existing, err := resultFor(ctx, tx, held.PuzzleNo, playerID)
				switch {
				case errors.Is(err, ErrResultNotFound):
					summary.Replayed++
				case err != nil:
					return err
				case existing.EnteredBy != nil:
					summary.Skipped++
				default:
					summary.Updated++
				}
				continue
			}

			// entered_by nil: these came from a token originally, and
			// pretending otherwise would lock them against future corrections.
			outcome, previous, err := UpsertResult(ctx, tx, result, nil)
			if err != nil {
				return err
			}
			switch outcome {
			case OutcomeCreated:
				summary.Replayed++
			case OutcomeUpdated:
				summary.Updated++
			case OutcomeIgnored:
				summary.Skipped++
			}

			if outcome != OutcomeIgnored {
				activityAction := ActionResultCreated
				if outcome == OutcomeUpdated {
					activityAction = ActionResultUpdated
				}
				if err := LogActivity(ctx, tx, actor, activityAction, SubjectResult, &playerID,
					activityDetailFor(result, previous)); err != nil {
					return err
				}
			}
		}

		if dryRun {
			return nil
		}

		// Held rows go whether or not they applied. Results are one row per
		// puzzle and player, so a refused write can never apply later unless
		// someone runs `results unset` — keeping it would leave a claimed
		// sender listed in `identity pending` forever with something that
		// never resolves.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pending_results WHERE source = ? AND external_id = ?`,
			source, externalID); err != nil {
			return fmt.Errorf("clear held results: %w", err)
		}

		return LogActivity(ctx, tx, actor, action, SubjectIdentity, &playerID, map[string]any{
			"source": source, "external_id": externalID,
			"replayed": summary.Replayed, "updated": summary.Updated, "skipped": summary.Skipped,
		})
	})
	return summary, err
}

// pendingResultsFor reads what is held for a sender, and the latest hint.
func pendingResultsFor(ctx context.Context, q Querier, source, externalID string) ([]PendingResult, string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT puzzle_no, solved, guesses, hard_mode, COALESCE(display_hint, '')
		FROM pending_results
		WHERE source = ? AND external_id = ?
		ORDER BY puzzle_no`, source, externalID)
	if err != nil {
		return nil, "", fmt.Errorf("read held results: %w", err)
	}
	defer rows.Close()

	var (
		held []PendingResult
		hint string
	)
	for rows.Next() {
		var (
			r        PendingResult
			rowsHint string
		)
		if err := rows.Scan(&r.PuzzleNo, &r.Solved, &r.Guesses, &r.HardMode, &rowsHint); err != nil {
			return nil, "", fmt.Errorf("scan held result: %w", err)
		}
		if rowsHint != "" {
			hint = rowsHint
		}
		held = append(held, r)
	}
	return held, hint, rows.Err()
}

// DiscardPendingResults drops a sender's held results without creating a
// player, for someone who posts in the group but is not in the game.
func DiscardPendingResults(ctx context.Context, db *sql.DB, actor Actor, source, externalID string) (int, error) {
	var discarded int
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		held, _, err := pendingResultsFor(ctx, tx, source, externalID)
		if err != nil {
			return err
		}
		if len(held) == 0 {
			return ErrNoPendingResults
		}
		discarded = len(held)

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pending_results WHERE source = ? AND external_id = ?`,
			source, externalID); err != nil {
			return fmt.Errorf("discard held results: %w", err)
		}
		return LogActivity(ctx, tx, actor, ActionIdentityDiscarded, SubjectIdentity, nil, map[string]any{
			"source": source, "external_id": externalID, "discarded": discarded,
		})
	})
	return discarded, err
}

// DeleteExpiredPendingResults drops held results older than the retention
// window. Nothing schedules this yet; retention is unlimited by default.
func DeleteExpiredPendingResults(ctx context.Context, q Querier, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	res, err := q.ExecContext(ctx,
		`DELETE FROM pending_results WHERE received_at < ?`, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("delete expired held results: %w", err)
	}
	return res.RowsAffected()
}

// PendingResultsFor reads what is held for a sender.
func PendingResultsFor(ctx context.Context, q Querier, source, externalID string) ([]PendingResult, string, error) {
	return pendingResultsFor(ctx, q, source, externalID)
}
