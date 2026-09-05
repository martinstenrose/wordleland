package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Freshness is when results last arrived, and how much is waiting.
//
// The failure this exists to catch is not a dropped connection — that is
// loud and self-healing — but a bridge watching the wrong group: connected,
// answering, delivering nothing. A connection indicator is green throughout
// that. "Last result three days ago" is not.
type Freshness struct {
	// LastResultAt is when a result was most recently written — arrival
	// time, not the puzzle's date. Read from the activity log, because results
	// carry only the day they belong to, and a backfill of last month would
	// otherwise read as nothing having arrived since.
	LastResultAt time.Time

	// LatestPuzzle is the highest puzzle the board holds, which says how
	// current it is rather than when it was last touched.
	LatestPuzzle int

	// PendingSenders and PendingResults are what is held for senders nobody
	// has claimed. A bridge working perfectly against an unclaimed roster
	// looks healthy and puts nothing on the board.
	PendingSenders int
	PendingResults int
}

// ReadFreshness answers the diagnostics page's first question.
func ReadFreshness(ctx context.Context, q Querier) (Freshness, error) {
	var f Freshness

	// The column itself rather than MAX(at): an aggregate loses the declared
	// type the driver keys its time parsing off, and comes back a string.
	var at sql.NullTime
	err := q.QueryRowContext(ctx,
		`SELECT at FROM activity_log WHERE action IN (?, ?) ORDER BY at DESC LIMIT 1`,
		ActionResultCreated, ActionResultUpdated,
	).Scan(&at)
	if err != nil && err != sql.ErrNoRows {
		return f, fmt.Errorf("read last result: %w", err)
	}
	if at.Valid {
		f.LastResultAt = at.Time
	}

	var puzzle sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MAX(puzzle_no) FROM results`).Scan(&puzzle); err != nil {
		return f, fmt.Errorf("read latest puzzle: %w", err)
	}
	if puzzle.Valid {
		f.LatestPuzzle = int(puzzle.Int64)
	}

	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT source || external_id), COUNT(*) FROM pending_results`,
	).Scan(&f.PendingSenders, &f.PendingResults); err != nil {
		return f, fmt.Errorf("count pending results: %w", err)
	}
	return f, nil
}
