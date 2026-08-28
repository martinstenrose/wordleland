package announce

import (
	"context"
	"log/slog"
	"time"
)

// scheduledCheckTimeout bounds the database work and Signal request made by
// the monthly scheduler. Live-result checks carry their own equivalent
// deadline in the bridge.
const scheduledCheckTimeout = 20 * time.Second

type waitFunc func(context.Context, time.Duration) bool

// RunMonthly calls check at local noon on the first day of every month. A
// successful check records the month itself, so later live-result checks are
// harmless. A failure remains unrecorded and is retried by the next live
// result rather than by a tight scheduler loop.
func RunMonthly(ctx context.Context, check func(context.Context, time.Time) error, logger *slog.Logger) {
	runMonthly(ctx, check, logger, time.Now, waitContext)
}

func runMonthly(ctx context.Context, check func(context.Context, time.Time) error,
	logger *slog.Logger, now func() time.Time, wait waitFunc) {

	for {
		current := now()
		if !wait(ctx, nextMonthlyNoon(current).Sub(current)) {
			return
		}

		runAt := now()
		checkCtx, cancel := context.WithTimeout(ctx, scheduledCheckTimeout)
		err := check(checkCtx, runAt)
		cancel()
		if err != nil {
			logger.Warn("could not announce the month's winner; will retry on the next live message",
				"error", err)
		}
	}
}

func nextMonthlyNoon(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, now.Location())
	if !now.Before(next) {
		next = time.Date(now.Year(), now.Month()+1, 1, 12, 0, 0, 0, now.Location())
	}
	return next
}

func waitContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
