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

func TestDayAnnouncedIsFalseUntilRecorded(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	got, err := DayAnnounced(ctx, db, 1554)
	if err != nil {
		t.Fatalf("DayAnnounced: %v", err)
	}
	if got {
		t.Fatalf("DayAnnounced() = true before anything was recorded")
	}

	if err := RecordDayAnnouncement(ctx, db, 1554); err != nil {
		t.Fatalf("RecordDayAnnouncement: %v", err)
	}

	got, err = DayAnnounced(ctx, db, 1554)
	if err != nil {
		t.Fatalf("DayAnnounced: %v", err)
	}
	if !got {
		t.Fatalf("DayAnnounced() = false after recording")
	}
}

// The neighbouring puzzles are exactly the ones the daily check looks at, so
// a row leaking one either way would announce the wrong day or skip a day.
func TestDayAnnouncedIsScopedToItsPuzzle(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if err := RecordDayAnnouncement(ctx, db, 1554); err != nil {
		t.Fatalf("RecordDayAnnouncement: %v", err)
	}

	for _, puzzle := range []int{1553, 1555} {
		got, err := DayAnnounced(ctx, db, puzzle)
		if err != nil {
			t.Fatalf("DayAnnounced(%d): %v", puzzle, err)
		}
		if got {
			t.Errorf("DayAnnounced(%d) = true, want false", puzzle)
		}
	}
}

// The difference between "this deployment has never posted a recap" and "it
// has, and simply has nothing for this particular day" is what decides
// whether the days already in the database are a backlog or history.
func TestAnyDayAnnouncedSeesTheWholeTable(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	any, err := AnyDayAnnounced(ctx, db)
	if err != nil {
		t.Fatalf("AnyDayAnnounced: %v", err)
	}
	if any {
		t.Fatal("AnyDayAnnounced() = true on an empty table")
	}

	if err := RecordDayAnnouncement(ctx, db, 1554); err != nil {
		t.Fatalf("RecordDayAnnouncement: %v", err)
	}

	any, err = AnyDayAnnounced(ctx, db)
	if err != nil {
		t.Fatalf("AnyDayAnnounced: %v", err)
	}
	if !any {
		t.Error("AnyDayAnnounced() = false after a day was recorded")
	}
	// Still true for a day that is not the recorded one: the question is
	// about the table, not about a puzzle.
	if done, _ := DayAnnounced(ctx, db, 1999); done {
		t.Error("DayAnnounced(1999) = true; AnyDayAnnounced must not widen it")
	}
}

// Same reasoning as the monthly case: recording a day twice must fail rather
// than pass silently, because that write is the whole duplicate guard.
func TestRecordDayAnnouncementTwiceFails(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if err := RecordDayAnnouncement(ctx, db, 1554); err != nil {
		t.Fatalf("first RecordDayAnnouncement: %v", err)
	}
	if err := RecordDayAnnouncement(ctx, db, 1554); err == nil {
		t.Fatalf("second RecordDayAnnouncement succeeded, want a unique-constraint error")
	}
}
