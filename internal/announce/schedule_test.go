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
	runMonthly(ctx, check, logger, func() time.Time { return current }, wait)
	if calls != 1 {
		t.Errorf("check called %d times, want 1", calls)
	}
}
