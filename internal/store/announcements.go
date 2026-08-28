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
