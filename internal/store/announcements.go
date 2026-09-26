package store

import (
	"context"
	"fmt"
	"time"
)

// MonthAnnounced reports whether the Signal bridge has already posted this
// month's winner, so a restart or a replayed result cannot repost it.
func MonthAnnounced(ctx context.Context, q Querier, year int, month time.Month) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM signal_month_announcements WHERE year = ? AND month = ?`,
		year, int(month),
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check month announcement: %w", err)
	}
	return n > 0, nil
}

// RecordMonthAnnouncement marks a month as announced.
//
// Called only after the message has actually been sent: recording first and
// failing to send would silently and permanently skip that month, where
// recording after risks at worst a duplicate post, in the narrow window
// between the send succeeding and this write landing.
func RecordMonthAnnouncement(ctx context.Context, q Querier, year int, month time.Month) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO signal_month_announcements (year, month) VALUES (?, ?)`,
		year, int(month),
	); err != nil {
		return fmt.Errorf("record month announcement: %w", err)
	}
	return nil
}

// DayAnnounced reports whether the bridge has already posted a recap for
// this puzzle. A day has two triggers — the last active player filing, and
// the run just after midnight — and this is what keeps them from posting
// twice.
func DayAnnounced(ctx context.Context, q Querier, puzzleNo int) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM signal_day_announcements WHERE puzzle_no = ?`,
		puzzleNo,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check day announcement: %w", err)
	}
	return n > 0, nil
}

// AnyDayAnnounced reports whether a daily recap has ever been posted on this
// deployment. An empty table means the daily announcement has not run here
// before — a fresh install, or one upgrading to it — which is the one case
// where the days already in the database are history rather than a backlog.
func AnyDayAnnounced(ctx context.Context, q Querier) (bool, error) {
	var exists int
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM signal_day_announcements)`,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check whether any day has been announced: %w", err)
	}
	return exists == 1, nil
}

// RecordDayAnnouncement marks a puzzle as announced, after the send, for
// the same reason RecordMonthAnnouncement does.
func RecordDayAnnouncement(ctx context.Context, q Querier, puzzleNo int) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO signal_day_announcements (puzzle_no) VALUES (?)`,
		puzzleNo,
	); err != nil {
		return fmt.Errorf("record day announcement: %w", err)
	}
	return nil
}

// WeekAnnounced reports whether the bridge has already posted the recap of
// the week starting on puzzle first, which is a Monday's.
func WeekAnnounced(ctx context.Context, q Querier, first int) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM signal_week_announcements WHERE first_puzzle = ?`,
		first,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check week announcement: %w", err)
	}
	return n > 0, nil
}

// AnyWeekAnnounced is AnyDayAnnounced for the week.
func AnyWeekAnnounced(ctx context.Context, q Querier) (bool, error) {
	var exists int
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM signal_week_announcements)`,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check whether any week has been announced: %w", err)
	}
	return exists == 1, nil
}

// RecordWeekAnnouncement marks a week as announced, after the send, for the
// same reason RecordMonthAnnouncement does.
func RecordWeekAnnouncement(ctx context.Context, q Querier, first int) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO signal_week_announcements (first_puzzle) VALUES (?)`,
		first,
	); err != nil {
		return fmt.Errorf("record week announcement: %w", err)
	}
	return nil
}
