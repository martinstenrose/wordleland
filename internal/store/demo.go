package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ClearSummary reports what demo clear did, or would do under a dry run.
type ClearSummary struct {
	Deleted int
	// PendingCleared counts held results for senders nobody ever claimed.
	// pending_results has no foreign key to players at all — a claimed
	// sender's rows are replayed into results and deleted at claim time, so
	// anything left here is, by construction, unclaimed — it is removed
	// directly, not as a cascade.
	PendingCleared int
	// Blocked names players a pending invitation kept from being deleted —
	// invitations.player_id is ON DELETE RESTRICT, so it must be reported
	// rather than allowed to fail the whole run.
	Blocked []string
}

// errClearDryRun rolls the transaction back at the end of a dry run, the
// same belt-and-suspenders reason backfill's dry run does: nothing should be
// able to survive a dry run even if a write slipped in above.
var errClearDryRun = errors.New("dry run")

// ClearDemoData deletes every player, taking their results and
// player_identities with them via ON DELETE CASCADE, and separately clears
// pending_results — which has no foreign key to players, since it only ever
// holds results for senders nobody has claimed yet — leaving users and the
// activity log untouched.
//
// This is the only place in the codebase that deletes a player rather than
// retiring them. It exists only for DEMO_MODE: see docs/decisions.md for why
// deletion is acceptable there and nowhere else. No provenance tracking
// decides who is "demo data" — on a DEMO_MODE instance every player is, and
// so is every held result waiting on a sender nobody will ever claim.
//
// A player with a pending invitation blocks on the ON DELETE RESTRICT from
// invitations.player_id. Rather than let that fail the whole run, such
// players are found first and reported as blocked instead of deleted.
func ClearDemoData(ctx context.Context, db *sql.DB, actor Actor, dryRun bool) (ClearSummary, error) {
	var summary ClearSummary
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		players, err := ListPlayers(ctx, tx)
		if err != nil {
			return err
		}

		blocked, err := playersWithInvitation(ctx, tx)
		if err != nil {
			return err
		}

		for _, p := range players {
			if blocked[p.ID] {
				summary.Blocked = append(summary.Blocked, p.Slug)
				continue
			}

			summary.Deleted++
			if dryRun {
				continue
			}

			if _, err := tx.ExecContext(ctx, `DELETE FROM players WHERE id = ?`, p.ID); err != nil {
				return fmt.Errorf("delete player %s: %w", p.Slug, err)
			}
			// The row is gone; this and the "#id" rendering already used for
			// a vanished subject are the only remaining record it existed.
			if err := LogActivity(ctx, tx, actor, ActionPlayerDeleted, SubjectPlayer, &p.ID,
				map[string]any{"slug": p.Slug, "name": p.Name}); err != nil {
				return err
			}
		}

		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_results`).Scan(&summary.PendingCleared); err != nil {
			return fmt.Errorf("count pending results: %w", err)
		}
		if !dryRun && summary.PendingCleared > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM pending_results`); err != nil {
				return fmt.Errorf("clear pending results: %w", err)
			}
		}

		if dryRun {
			return errClearDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errClearDryRun) {
		return ClearSummary{}, err
	}
	return summary, nil
}

// playersWithInvitation returns the ids of players any invitation row still
// names, pending or not.
//
// ON DELETE RESTRICT blocks on the presence of a referencing row, not on
// what its columns say — an expired or already-accepted invitation blocks
// deletion exactly as a live one does, and nothing in this codebase ever
// sweeps the invitations table. Filtering to only the currently-pending ones
// here would let a used or expired row reach the DELETE undetected and turn
// into the RESTRICT violation this function exists to head off.
func playersWithInvitation(ctx context.Context, q Querier) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT player_id FROM invitations`)
	if err != nil {
		return nil, fmt.Errorf("find players with an invitation: %w", err)
	}
	defer rows.Close()

	blocked := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan invitation player: %w", err)
		}
		blocked[id] = true
	}
	return blocked, rows.Err()
}
