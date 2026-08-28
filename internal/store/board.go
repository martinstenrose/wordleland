package store

import (
	"context"
	"fmt"
	"time"
)

// BoardResult is one result, flattened for the statistics package.
//
// The whole history is read in one go and reduced in memory: it is around
// 1,300 rows growing by a dozen a day, so the query is trivial either way,
// and keeping the arithmetic out of SQL is what makes the rules
// testable against hand-built fixtures rather than against a database.
type BoardResult struct {
	PlayerID int64
	PuzzleNo int
	Date     time.Time
	// Guesses is 0 when the puzzle was failed; Solved says which.
	Guesses  int
	Solved   bool
	HardMode bool
}

// ResultsForBoard returns every result, oldest first.
func ResultsForBoard(ctx context.Context, q Querier) ([]BoardResult, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT player_id, puzzle_no, date, guesses, solved, hard_mode
		FROM results
		ORDER BY puzzle_no, player_id`)
	if err != nil {
		return nil, fmt.Errorf("read results: %w", err)
	}
	defer rows.Close()

	var out []BoardResult
	for rows.Next() {
		var (
			r       BoardResult
			guesses *int
			date    time.Time
		)
		if err := rows.Scan(&r.PlayerID, &r.PuzzleNo, &date, &guesses, &r.Solved, &r.HardMode); err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		if guesses != nil {
			r.Guesses = *guesses
		}
		// The driver hands back a DATE column at midnight UTC; the stored
		// value is a calendar date, so it is re-anchored locally.
		r.Date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountPlayedPuzzles counts the distinct puzzles anybody has played, which
// is what the top bar reports as the board's span.
//
// Its own query rather than a field on the computed board: the chrome is
// built for every page, including the ones that never compute a board, and
// they should not have to.
func CountPlayedPuzzles(ctx context.Context, q Querier) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT puzzle_no) FROM results`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count played puzzles: %w", err)
	}
	return n, nil
}
