package announce

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// The schedule checks before it ever sleeps, so an app that comes up at 08:00
// having missed 00:01 recovers there and then rather than waiting for
// somebody to file a result. An already-cancelled context makes the first
// sleep return immediately, leaving exactly the start check behind.
func TestTheScheduleChecksOnStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	RunMidnight(ctx, func(context.Context, time.Time) error {
		calls++
		return nil
	}, logger)

	if calls != 1 {
		t.Errorf("check ran %d times on a cancelled start, want the 1 start check", calls)
	}
}

// After the start check it sleeps until just after midnight and checks
// again, under a deadline.
func TestTheScheduleRunsJustAfterMidnightWithDeadline(t *testing.T) {
	ctx := context.Background()
	evening := time.Date(2026, time.September, 30, 23, 45, 0, 0, time.Local)
	midnight := time.Date(2026, time.October, 1, 0, 1, 0, 0, time.Local)
	current := evening
	waits := 0
	wait := func(_ context.Context, d time.Duration) bool {
		waits++
		if waits == 1 {
			if d != 16*time.Minute {
				t.Errorf("first wait = %v, want 16m", d)
			}
			current = midnight
			return true
		}
		return false
	}

	var at []time.Time
	check := func(ctx context.Context, now time.Time) error {
		at = append(at, now)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("scheduled check has no deadline")
		}
		return nil
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runSchedule(ctx, check, logger, func() time.Time { return current }, wait)
	if len(at) != 2 || !at[0].Equal(evening) || !at[1].Equal(midnight) {
		t.Errorf("checked at %v, want on start and at %v", at, midnight)
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
