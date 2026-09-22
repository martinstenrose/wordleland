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
	// PostedAt is carried through the wait so the replayed result keeps
	// the time it was posted in the group. See Result.PostedAt.
	PostedAt *time.Time
}

// ResolveIdentity maps a sender to a player, also returning the identity
// row's own id (distinct from the player's), so a caller that writes an
// automated result can attribute it to this specific identity.
func ResolveIdentity(ctx context.Context, q Querier, source, externalID string) (Player, int64, error) {
	var (
		p          Player
		identityID int64
	)
	err := q.QueryRowContext(ctx, `
		SELECT p.id, p.slug, p.name, p.user_id, p.active, i.id
		FROM player_identities i
		JOIN players p ON p.id = i.player_id
		WHERE i.source = ? AND i.external_id = ?`, source, externalID,
	).Scan(&p.ID, &p.Slug, &p.Name, &p.UserID, &p.Active, &identityID)

	if errors.Is(err, sql.ErrNoRows) {
		return Player{}, 0, ErrIdentityNotFound
	}
	if err != nil {
		return Player{}, 0, fmt.Errorf("resolve identity: %w", err)
	}
	return p, identityID, nil
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
			(source, external_id, display_hint, puzzle_no, solved, guesses, hard_mode, posted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, external_id, puzzle_no) DO UPDATE SET
			solved       = excluded.solved,
			guesses      = excluded.guesses,
			hard_mode    = excluded.hard_mode,
			display_hint = excluded.display_hint,
			received_at  = CURRENT_TIMESTAMP,
			posted_at    = COALESCE(pending_results.posted_at, excluded.posted_at)`,
		source, externalID, nullIfEmpty(hint), r.PuzzleNo, r.Solved, r.Guesses, r.HardMode, r.PostedAt,
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
		if _, _, err := ResolveIdentity(ctx, tx, source, externalID); err == nil {
			return ErrIdentityTaken
		} else if !errors.Is(err, ErrIdentityNotFound) {
			return err
		}

		held, hint, err := pendingResultsFor(ctx, tx, source, externalID)
		if err != nil {
			return err
		}

		// identityID attributes replayed results to this identity, so a later
		// `identity reassign --move-results` can move exactly these. Left zero
		// under dryRun, where nothing is inserted and no result write happens
		// through UpsertResult anyway (see the dryRun branch below).
		var identityID int64
		if !dryRun {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO player_identities (player_id, source, external_id, display_hint)
				VALUES (?, ?, ?, ?)`, playerID, source, externalID, nullIfEmpty(hint))
			if err != nil {
				if isUniqueViolation(err) {
					return ErrIdentityTaken
				}
				return fmt.Errorf("create identity: %w", err)
			}
			if identityID, err = res.LastInsertId(); err != nil {
				return fmt.Errorf("read new identity id: %w", err)
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
				PostedAt: held.PostedAt,
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
			outcome, previous, err := UpsertResult(ctx, tx, result, nil, &identityID)
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

// ClaimedIdentity is one player_identities row, joined to the player it maps
// to, which is what `identity list` shows.
type ClaimedIdentity struct {
	Source      string
	ExternalID  string
	DisplayHint string
	PlayerSlug  string
	PlayerName  string
}

// ListClaimedIdentities returns every claimed identity, or only those mapped
// to playerID when it is non-nil.
func ListClaimedIdentities(ctx context.Context, q Querier, playerID *int64) ([]ClaimedIdentity, error) {
	query := `
		SELECT i.source, i.external_id, COALESCE(i.display_hint, ''), p.slug, p.name
		FROM player_identities i
		JOIN players p ON p.id = i.player_id`
	args := []any{}
	if playerID != nil {
		query += " WHERE i.player_id = ?"
		args = append(args, *playerID)
	}
	query += " ORDER BY p.slug, i.source, i.external_id"

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list claimed identities: %w", err)
	}
	defer rows.Close()

	var claimed []ClaimedIdentity
	for rows.Next() {
		var c ClaimedIdentity
		if err := rows.Scan(&c.Source, &c.ExternalID, &c.DisplayHint, &c.PlayerSlug, &c.PlayerName); err != nil {
			return nil, fmt.Errorf("scan claimed identity: %w", err)
		}
		claimed = append(claimed, c)
	}
	return claimed, rows.Err()
}

// ReassignSummary reports what reassigning an identity did with its
// previously-written results.
type ReassignSummary struct {
	OldPlayerSlug string
	NewPlayerSlug string
	// Moved counts results moved to the new player.
	Moved int
	// Left counts results that stayed with the old player because the new
	// player already had a result for that puzzle — either hand-entered
	// (which always wins) or written by some other automated source (which
	// cannot be merged: only one result exists per puzzle and player).
	Left int
	// Untracked counts the old player's automated results with no
	// identity_id at all — written before this column existed, or by some
	// other automated path that never set it. Never guessed at and never
	// moved: attributing one to this identity could just as easily be wrong
	// as right. Surfaced so an operator knows to check them and move any
	// that belong here by hand, with `results set`.
	Untracked int
}

// ReassignIdentity repoints a claimed identity to a different player.
//
// When moveResults is true, every result this specific identity wrote (found
// via results.identity_id, set by UpsertResult when a result comes from an
// identity) is moved to the new player, except where the new player already
// has a result for that puzzle — that existing row is left untouched and the
// moved-from row stays with the old player, so no result is ever silently
// dropped. Results the old player has from a different identity, or entered
// by hand, are never touched: identity_id scopes the move to this identity
// alone.
//
// The old player may also have automated results with no identity_id at
// all — written before that column existed (see migration 0010), or by some
// other automated path that never set it. These are never guessed at or
// moved; ReassignSummary.Untracked counts them so an operator knows history
// may remain behind that needs checking by hand, with `results set`.
//
// dryRun reports without writing.
func ReassignIdentity(ctx context.Context, db *sql.DB, actor Actor,
	source, externalID string, newPlayerID int64, moveResults, dryRun bool) (ReassignSummary, error) {

	var summary ReassignSummary
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		oldPlayer, identityID, err := ResolveIdentity(ctx, tx, source, externalID)
		if err != nil {
			return err
		}
		if oldPlayer.ID == newPlayerID {
			return fmt.Errorf("%s/%s is already mapped to that player", source, externalID)
		}

		newPlayer, err := PlayerByID(ctx, tx, newPlayerID)
		if err != nil {
			return err
		}
		summary.OldPlayerSlug = oldPlayer.Slug
		summary.NewPlayerSlug = newPlayer.Slug

		if moveResults {
			rows, err := tx.QueryContext(ctx, `
				SELECT id, puzzle_no FROM results WHERE identity_id = ? AND player_id = ?`,
				identityID, oldPlayer.ID)
			if err != nil {
				return fmt.Errorf("read identity's results: %w", err)
			}
			type row struct {
				id, puzzleNo int64
			}
			var toMove []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.id, &r.puzzleNo); err != nil {
					rows.Close()
					return fmt.Errorf("scan result row: %w", err)
				}
				toMove = append(toMove, r)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			rows.Close()

			for _, r := range toMove {
				_, err := resultFor(ctx, tx, int(r.puzzleNo), newPlayerID)
				switch {
				case errors.Is(err, ErrResultNotFound):
					// No conflict: falls through to the move below.
				case err != nil:
					return err
				default:
					// The new player already has a result for this puzzle,
					// hand-entered or from another identity. Either way it
					// wins, and the moved-from row stays with the old player
					// rather than being dropped.
					summary.Left++
					continue
				}

				if !dryRun {
					if _, err := tx.ExecContext(ctx,
						`UPDATE results SET player_id = ? WHERE id = ?`, newPlayerID, r.id); err != nil {
						return fmt.Errorf("move result: %w", err)
					}
				}
				summary.Moved++
			}

			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM results
				WHERE player_id = ? AND entered_by IS NULL AND identity_id IS NULL`,
				oldPlayer.ID).Scan(&summary.Untracked); err != nil {
				return fmt.Errorf("count untracked results: %w", err)
			}
		}

		if !dryRun {
			if _, err := tx.ExecContext(ctx,
				`UPDATE player_identities SET player_id = ? WHERE id = ?`, newPlayerID, identityID); err != nil {
				return fmt.Errorf("reassign identity: %w", err)
			}
		}

		if dryRun {
			return nil
		}

		return LogActivity(ctx, tx, actor, ActionIdentityReassigned, SubjectIdentity, &newPlayerID, map[string]any{
			"source": source, "external_id": externalID,
			"old_player_id": oldPlayer.ID, "new_player_id": newPlayerID,
			"moved": summary.Moved, "left": summary.Left,
		})
	})
	return summary, err
}

// pendingResultsFor reads what is held for a sender, and the latest hint.
func pendingResultsFor(ctx context.Context, q Querier, source, externalID string) ([]PendingResult, string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT puzzle_no, solved, guesses, hard_mode, posted_at, COALESCE(display_hint, '')
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
		if err := rows.Scan(&r.PuzzleNo, &r.Solved, &r.Guesses, &r.HardMode, &r.PostedAt, &rowsHint); err != nil {
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
// window. Called on a schedule by the janitor in cmd/wordleland, which
// passes PENDING_RETENTION; retention is unlimited by default.
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
