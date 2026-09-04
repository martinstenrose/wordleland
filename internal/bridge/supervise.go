package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Restart policy for a bridge that panics.
//
// Merging the bridge into the server made a panic here everyone's
// problem: unrecovered, it takes the board down with it. Recovering and
// retrying for ever is the other mistake — a bridge that panics on every
// message would spin silently, and the log would be the only witness.
//
// So: recover and retry, but count. Panicking repeatedly inside one window
// is a bug rather than bad luck, and the supervisor stops. Stopping is what
// makes it visible, because a configured bridge that is not running fails
// the liveness probe and the container is restarted by something that can
// actually be seen.
const (
	restartDelay = 5 * time.Second
	// panicWindow and maxPanics: more than this many crashes this close
	// together is not a transient fault.
	panicWindow = 5 * time.Minute
	maxPanics   = 5
)

// Supervisor keeps a bridge running and reports whether it is.
type Supervisor struct {
	bridge *Bridge
	logger *slog.Logger

	mu sync.Mutex
	// exited is set once the supervising loop has returned, whether it gave
	// up or the process is shutting down. It is deliberately not "is an
	// attempt running right now": see Alive.
	exited bool
	// stopped is why the supervisor gave up, empty while it has not and
	// empty for an ordinary shutdown, which is not a fault.
	stopped string

	// sleep is swapped in tests so a restart costs no real time.
	sleep func(context.Context, time.Duration)
	// run is what gets supervised. It is the bridge in production and a
	// stub in tests, so a panic can be provoked without a websocket.
	run func(context.Context) error
}

// Supervise wraps a bridge. Call Run to start it.
func Supervise(b *Bridge, logger *slog.Logger) *Supervisor {
	return &Supervisor{bridge: b, logger: logger, sleep: sleepContext, run: b.Run}
}

// Alive reports whether the bridge is running, and why not if it is not.
//
// This is the only bridge state the liveness probe consults. A bridge
// that is disconnected and retrying is alive: restarting the process would
// interrupt its backoff and fix nothing. A bridge whose supervisor has
// returned is not, and a restart is exactly the right response.
//
// What is reported is the supervisor's state, not the current attempt's.
// Between a panic and the restart that follows it there is no attempt
// running at all, and answering the probe with "dead" through that window
// would have the container bounced for a fault the supervisor was already
// recovering from — and bounced on a 503 carrying no reason, which reads
// exactly like the case where it had given up for good.
func (s *Supervisor) Alive() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.exited, s.stopped
}

// Status reports what the bridge is doing, for the diagnostics page.
func (s *Supervisor) Status() Status { return s.bridge.Status() }

// Run supervises until the context is cancelled or the bridge has panicked
// too often to be worth restarting.
func (s *Supervisor) Run(ctx context.Context) {
	var panics []time.Time

	for {
		if ctx.Err() != nil {
			s.setStopped("")
			return
		}

		err, panicked := s.runOnce(ctx)

		switch {
		case ctx.Err() != nil:
			// An ordinary shutdown. Not a fault, so nothing is recorded.
			s.setStopped("")
			return

		case panicked:
			now := time.Now()
			panics = append(panics, now)
			// Only crashes inside the window count towards giving up.
			kept := panics[:0]
			for _, at := range panics {
				if now.Sub(at) <= panicWindow {
					kept = append(kept, at)
				}
			}
			panics = kept

			if len(panics) >= maxPanics {
				s.setStopped(fmt.Sprintf(
					"the Signal bridge panicked %d times in %s and has been stopped",
					len(panics), panicWindow))
				s.logger.Error("the Signal bridge keeps panicking and will not be restarted; "+
					"this is a bug. The health probe now reports the process as unhealthy",
					"panics", len(panics), "window", panicWindow)
				return
			}
			s.logger.Warn("restarting the Signal bridge after a panic",
				"panics_in_window", len(panics), "in", restartDelay)

		case err != nil && !errors.Is(err, context.Canceled):
			s.logger.Warn("the Signal bridge stopped, restarting",
				"error", err, "in", restartDelay)

		default:
			s.logger.Info("the Signal bridge stopped cleanly, restarting", "in", restartDelay)
		}

		s.sleep(ctx, restartDelay)
	}
}

// runOnce runs the bridge, converting a panic into a return value.
func (s *Supervisor) runOnce(ctx context.Context) (err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			// The stack is the whole value of catching this: without it the
			// log says a panic happened and nothing about where.
			s.logger.Error("the Signal bridge panicked",
				"panic", r, "stack", string(debug.Stack()))
		}
	}()

	return s.run(ctx), false
}

func (s *Supervisor) setStopped(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exited = true
	s.stopped = reason
}
