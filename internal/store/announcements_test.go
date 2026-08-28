package store

import (
	"context"
	"testing"
	"time"
)

func TestMonthAnnouncedIsFalseUntilRecorded(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	got, err := MonthAnnounced(ctx, db, 2026, time.January)
	if err != nil {
		t.Fatalf("MonthAnnounced: %v", err)
	}
	if got {
		t.Fatalf("MonthAnnounced() = true before anything was recorded")
	}

	if err := RecordMonthAnnouncement(ctx, db, 2026, time.January); err != nil {
		t.Fatalf("RecordMonthAnnouncement: %v", err)
	}

	got, err = MonthAnnounced(ctx, db, 2026, time.January)
	if err != nil {
		t.Fatalf("MonthAnnounced: %v", err)
	}
	if !got {
		t.Fatalf("MonthAnnounced() = false after recording")
	}
}

// A neighbouring month, or the same month a year apart, must not read as
// announced: the key is the pair, not either half of it.
func TestMonthAnnouncedIsScopedToYearAndMonth(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if err := RecordMonthAnnouncement(ctx, db, 2026, time.February); err != nil {
		t.Fatalf("RecordMonthAnnouncement: %v", err)
	}

	for _, tt := range []struct {
		year  int
		month time.Month
	}{
		{2026, time.January},
		{2026, time.March},
		{2025, time.February},
		{2027, time.February},
	} {
		got, err := MonthAnnounced(ctx, db, tt.year, tt.month)
		if err != nil {
			t.Fatalf("MonthAnnounced(%d, %s): %v", tt.year, tt.month, err)
		}
		if got {
			t.Errorf("MonthAnnounced(%d, %s) = true, want false", tt.year, tt.month)
		}
	}
}

// This is the mechanism a restart or a replayed message relies on: a second
// recording for a month already marked must not succeed silently as if
// nothing had happened before it, or a caller relying on it to detect "have
// I already sent this" would have no way to tell the two apart.
func TestRecordMonthAnnouncementTwiceFails(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if err := RecordMonthAnnouncement(ctx, db, 2026, time.March); err != nil {
		t.Fatalf("first RecordMonthAnnouncement: %v", err)
	}
	if err := RecordMonthAnnouncement(ctx, db, 2026, time.March); err == nil {
		t.Fatalf("second RecordMonthAnnouncement succeeded, want a unique-constraint error")
	}
}
