package announce

import (
	"context"
	"log/slog"
	"time"
)

// scheduledCheckTimeout bounds the database work and Signal requests made by
// the scheduler. Live-result checks carry their own equivalent deadline in
// the bridge.
const scheduledCheckTimeout = 20 * time.Second

// dailyRunMinute is how far past midnight the announcements run.
//
// Not midnight exactly. A result posted at 23:59 has to reach signal-cli,
// cross the websocket and be filed before the recap reads the store, and a
// recap that lands a minute late is worth more than one that omits the last
// score of the day. It also keeps the run clear of the boundary itself,
// where a timer firing a hair early would compute the closing day as the
// current one and find nothing to close.
const dailyRunMinute = 1

type waitFunc func(context.Context, time.Duration) bool

// RunMidnight checks once on start, then just after every local midnight,
// which is the "whichever comes first" backstop: a day, week or month whose
// players have all filed its last puzzle is already posted by then and
// recorded, so the midnight run finds nothing.
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
// check is the day's, the week's and the month's checks joined, in that
// order: a week and a month close at a midnight like any day, and when they
// close together the smallest goes first.
//
// A failure is retried by the next live result, not by a tight loop here:
// a successful check records what it posted, so a later one is harmless.
func RunMidnight(ctx context.Context, check func(context.Context, time.Time) error, logger *slog.Logger) {
	runSchedule(ctx, check, logger, time.Now, waitContext)
}

// runSchedule is RunMidnight's loop with the clock and the sleep passed in:
// check once, sleep until just after the next midnight, check again under a
// deadline, log a failure and go round.
func runSchedule(ctx context.Context, check func(context.Context, time.Time) error,
	logger *slog.Logger, now func() time.Time, wait waitFunc) {

	runOnce(ctx, check, logger, now())
	for {
		current := now()
		if !wait(ctx, nextDailyRun(current).Sub(current)) {
			return
		}
		runOnce(ctx, check, logger, now())
	}
}

func runOnce(ctx context.Context, check func(context.Context, time.Time) error,
	logger *slog.Logger, runAt time.Time) {

	checkCtx, cancel := context.WithTimeout(ctx, scheduledCheckTimeout)
	defer cancel()
	if err := check(checkCtx, runAt); err != nil {
		logger.Warn("could not post an announcement; will retry on the next live message", "error", err)
	}
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
