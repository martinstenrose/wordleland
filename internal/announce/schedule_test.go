package announce

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestNextMonthlyNoon(t *testing.T) {
	zone := time.FixedZone("test", 2*60*60)
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before noon on the first",
			now:  time.Date(2026, time.September, 1, 6, 14, 0, 0, zone),
			want: time.Date(2026, time.September, 1, 12, 0, 0, 0, zone),
		},
		{
			name: "at noon waits for next month",
			now:  time.Date(2026, time.September, 1, 12, 0, 0, 0, zone),
			want: time.Date(2026, time.October, 1, 12, 0, 0, 0, zone),
		},
		{
			name: "crosses the year",
			now:  time.Date(2026, time.December, 20, 8, 0, 0, 0, zone),
			want: time.Date(2027, time.January, 1, 12, 0, 0, 0, zone),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextMonthlyNoon(tt.now); !got.Equal(tt.want) {
				t.Errorf("nextMonthlyNoon(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

func TestRunMonthlyCallsAtNoonWithDeadline(t *testing.T) {
	ctx := context.Background()
	before := time.Date(2026, time.September, 1, 11, 45, 0, 0, time.Local)
	noon := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.Local)
	current := before
	waits := 0
	wait := func(_ context.Context, d time.Duration) bool {
		waits++
		if waits == 1 {
			if d != 15*time.Minute {
				t.Errorf("first wait = %v, want 15m", d)
			}
			current = noon
			return true
		}
		return false
	}

	var calls int
	check := func(ctx context.Context, now time.Time) error {
		calls++
		if !now.Equal(noon) {
			t.Errorf("check time = %v, want %v", now, noon)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("scheduled check has no deadline")
		}
		return nil
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runSchedule(ctx, check, logger, func() time.Time { return current }, wait,
		nextMonthlyNoon, false, "monthly")
	if calls != 1 {
		t.Errorf("check called %d times, want 1", calls)
	}
}

// The daily schedule checks before it ever sleeps, so an app that comes up at
// 08:00 having missed 00:01 recovers there and then rather than waiting for
// somebody to file a result.
// Driven through the exported RunDaily rather than runSchedule, so the wiring
// that decides this — the one argument RunDaily passes — is what is under
// test. An already-cancelled context makes the first sleep return
// immediately, leaving exactly the start check behind.
func TestTheDailyScheduleChecksOnStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	RunDaily(ctx, func(context.Context, time.Time) error {
		calls++
		return nil
	}, logger)

	if calls != 1 {
		t.Errorf("check ran %d times on a cancelled start, want the 1 start check", calls)
	}
}

// The month deliberately does not: noon on the first is a grace period, and a
// restart at 06:00 must not cut it short.
func TestTheMonthlyScheduleDoesNotCheckOnStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	RunMonthly(ctx, func(context.Context, time.Time) error {
		calls++
		return nil
	}, logger)

	if calls != 0 {
		t.Errorf("check ran %d times on start, want 0", calls)
	}
}

// The daily run wakes just after midnight, not at it: a result filed at
// 23:59 still has to cross the bridge and land in the store.
func TestNextDailyRunIsJustAfterMidnight(t *testing.T) {
	for _, tt := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "mid-morning waits for tomorrow",
			now:  time.Date(2026, time.September, 20, 9, 30, 0, 0, time.Local),
			want: time.Date(2026, time.September, 21, 0, 1, 0, 0, time.Local),
		},
		{
			name: "a second before the run waits for it",
			now:  time.Date(2026, time.September, 20, 0, 0, 59, 0, time.Local),
			want: time.Date(2026, time.September, 20, 0, 1, 0, 0, time.Local),
		},
		{
			name: "exactly on the run goes to tomorrow, so it never fires twice",
			now:  time.Date(2026, time.September, 20, 0, 1, 0, 0, time.Local),
			want: time.Date(2026, time.September, 21, 0, 1, 0, 0, time.Local),
		},
		{
			name: "the last day of a month rolls into the next",
			now:  time.Date(2026, time.September, 30, 23, 0, 0, 0, time.Local),
			want: time.Date(2026, time.October, 1, 0, 1, 0, 0, time.Local),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextDailyRun(tt.now); !got.Equal(tt.want) {
				t.Errorf("nextDailyRun(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}
