package announce

import (
	"context"
	"log/slog"
	"time"
)

// scheduledCheckTimeout bounds the database work and Signal request made by
// the schedulers. Live-result checks carry their own equivalent deadline in
// the bridge.
const scheduledCheckTimeout = 20 * time.Second

// dailyRunMinute is how far past midnight the day's recap runs.
//
// Not midnight exactly. A result posted at 23:59 has to reach signal-cli,
// cross the websocket and be filed before the recap reads the store, and a
// recap that lands a minute late is worth more than one that omits the last
// score of the day. It also keeps the run clear of the boundary itself,
// where a timer firing a hair early would compute the closing day as the
// current one and find nothing to close.
const dailyRunMinute = 1

type waitFunc func(context.Context, time.Duration) bool

// RunMonthly calls check at local noon on the first day of every month. A
// successful check records the month itself, so later live-result checks are
// harmless. A failure remains unrecorded and is retried by the next live
// result rather than by a tight scheduler loop.
func RunMonthly(ctx context.Context, check func(context.Context, time.Time) error, logger *slog.Logger) {
	runSchedule(ctx, check, logger, time.Now, waitContext, nextMonthlyNoon, false,
		"could not announce the month's winner; will retry on the next live message")
}

// RunDaily checks once on start, then just after every local midnight, which
// is the "whichever comes first" backstop: a day whose players have all filed
// is already posted by then and recorded, so the midnight run finds nothing.
//
// The check on start is what recovers a missed midnight. An app that was down
// at 00:01 and comes up at 08:00 would otherwise have to wait for somebody to
// file a result before noticing, and on a day nobody plays the missed recap
// would fall out of the one-day-back window and never be posted at all.
//
// It is not a first-run backlog: SkipDailyBacklog runs before this and marks
// the day before startup as done when nothing has ever been announced, so a
// fresh deployment opens with the puzzle the group is currently playing.
//
// The week's check rides on the same run, after the day's, so check may be
// both joined; a week closes at a midnight like any day.
//
// Same failure handling as RunMonthly — an unrecorded day is retried by the
// next live result, not by a tight loop here.
func RunDaily(ctx context.Context, check func(context.Context, time.Time) error, logger *slog.Logger) {
	runSchedule(ctx, check, logger, time.Now, waitContext, nextDailyRun, true,
		"could not post the day's or the week's recap; will retry on the next live message")
}

// runSchedule is the loop both announcements share: sleep until next says,
// check once under a deadline, log a failure and go round again. They differ
// in when they wake, whether they also check on start, and what a failure is
// called.
//
// The month does not check on start: it is already caught up by any live
// result, its window is the whole month rather than a few hours, and noon on
// the first is a grace period that a restart at 06:00 should not cut short.
func runSchedule(ctx context.Context, check func(context.Context, time.Time) error,
	logger *slog.Logger, now func() time.Time, wait waitFunc,
	next func(time.Time) time.Time, onStart bool, warning string) {

	if onStart {
		runOnce(ctx, check, logger, now(), warning)
	}
	for {
		current := now()
		if !wait(ctx, next(current).Sub(current)) {
			return
		}
		runOnce(ctx, check, logger, now(), warning)
	}
}

func runOnce(ctx context.Context, check func(context.Context, time.Time) error,
	logger *slog.Logger, runAt time.Time, warning string) {

	checkCtx, cancel := context.WithTimeout(ctx, scheduledCheckTimeout)
	defer cancel()
	if err := check(checkCtx, runAt); err != nil {
		logger.Warn(warning, "error", err)
	}
}

func nextMonthlyNoon(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, now.Location())
	if !now.Before(next) {
		next = time.Date(now.Year(), now.Month()+1, 1, 12, 0, 0, 0, now.Location())
	}
	return next
}

// nextDailyRun uses AddDate rather than adding 24 hours, for the reason
// internal/wordle documents: across a daylight-saving boundary a day is 23
// or 25 hours, and adding a fixed duration would walk the run off midnight
// and eventually onto the wrong side of it.
func nextDailyRun(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, dailyRunMinute, 0, 0, now.Location())
	if !now.Before(next) {
		next = time.Date(now.Year(), now.Month(), now.Day(), 0, dailyRunMinute, 0, 0,
			now.Location()).AddDate(0, 0, 1)
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
