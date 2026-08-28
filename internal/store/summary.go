package store

import (
	"context"
	"fmt"
)

// Summary is the group's history in aggregate.
//
// Every figure is a whole-group total: nothing here names a player. The
// sign-in page shows it to anyone who reaches the host, so it deliberately
// carries nothing that would identify who plays or how any individual is
// doing.
type Summary struct {
	Games   int
	Days    int
	Players int

	// Average counts a failure as 7, matching the board's default.
	// Nil when nothing has been logged yet.
	Average *float64

	// SolvedPercent is rounded to a whole percent.
	SolvedPercent int

	// FiledToday counts results for the given puzzle.
	FiledToday int
}

// GroupSummary aggregates in SQL rather than reading the history into
// memory. The board does the latter because it needs every row anyway; this
// runs on an unauthenticated page, so it does the least work it can.
func GroupSummary(ctx context.Context, q Querier, currentPuzzle int) (Summary, error) {
	var s Summary
	var avg float64
	var solved int
	err := q.QueryRowContext(ctx, `
		SELECT
			count(*),
			count(DISTINCT puzzle_no),
			coalesce(avg(CASE WHEN solved THEN guesses ELSE 7 END), 0),
			coalesce(sum(CASE WHEN solved THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN puzzle_no = ? THEN 1 ELSE 0 END), 0)
		FROM results`, currentPuzzle,
	).Scan(&s.Games, &s.Days, &avg, &solved, &s.FiledToday)
	if err != nil {
		return Summary{}, fmt.Errorf("summarise results: %w", err)
	}
	// avg and the solve rate mean nothing over no rows, so they stay unset
	// rather than reporting a confident 0.00.
	if s.Games > 0 {
		s.Average = &avg
		s.SolvedPercent = int(float64(solved)/float64(s.Games)*100 + 0.5)
	}

	if err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM players WHERE active`).Scan(&s.Players); err != nil {
		return Summary{}, fmt.Errorf("count players: %w", err)
	}
	return s, nil
}
