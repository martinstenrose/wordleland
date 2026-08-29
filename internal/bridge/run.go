package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/martinstenrose/wordleland/internal/config"
)

const (
	// queueSize buffers between the reader and the worker, so a slow write
	// cannot stall reading from the websocket.
	queueSize = 256
	// drainTimeout bounds how long shutdown waits for queued results.
	drainTimeout = 10 * time.Second

	// Verification retries because signal-cli routinely takes a few seconds
	// longer to start than the app does; the app connecting first is the
	// ordinary case, not a fault.
	minVerifyDelay    = 2 * time.Second
	maxVerifyDelay    = 30 * time.Second
	maxVerifyAttempts = 8
)

// Bridge is a running Signal bridge.
type Bridge struct {
	health   *health
	source   *websocketSource
	filer    *filer
	verifier *verifier
	logger   *slog.Logger
}

// New builds a bridge. It does not connect: that happens in Run.
func New(cfg config.Bridge, deliver Deliverer, logger *slog.Logger) (*Bridge, error) {
	h := newHealth(time.Now)
	source, err := newWebsocketSource(cfg.SignalAPIURL, cfg.SignalAccount, logger, h)
	if err != nil {
		return nil, fmt.Errorf("signal source: %w", err)
	}
	h.describe(cfg.SignalAccount, cfg.SignalGroupID)
	return &Bridge{
		health:   h,
		source:   source,
		filer:    newFiler(cfg.SignalGroupID, deliver, logger, h),
		verifier: newVerifier(cfg.SignalAPIURL, cfg.SignalAccount, cfg.SignalGroupID),
		logger:   logger,
	}, nil
}

// Status reports what the bridge is doing, for the diagnostics page.
func (b *Bridge) Status() Status { return b.health.snapshot() }

// Run reads until the context is cancelled, then drains what is in hand.
//
// It returns only when reading has stopped. The caller supervises it: a
// panic in here must not take the web server with it, and a bridge that
// exits while still configured is a fault the health probe reports.
func (b *Bridge) Run(ctx context.Context) error {
	queue := make(chan Message, queueSize)
	done := make(chan struct{})

	// The worker owns everything slow. The reader hands over and moves on.
	go func() {
		defer close(done)
		for m := range queue {
			// Deliberately not ctx: on shutdown the queue is drained with
			// its own deadline, so results already in hand still get a
			// chance to land rather than being cancelled mid-flight.
			b.filer.handle(context.WithoutCancel(ctx), m)
		}
	}()

	// Alongside the reader, not before it: a configuration that cannot work
	// is worth saying loudly, but signal-cli is often still starting when
	// we are, and refusing to read until it answers would turn a slow
	// dependency into an outage.
	go b.verify(ctx)

	err := b.source.Run(ctx, queue)

	close(queue)
	select {
	case <-done:
	case <-time.After(drainTimeout):
		b.logger.Warn("gave up draining queued results", "timeout", drainTimeout)
	}
	return err
}

// verify asks signal-cli whether this configuration can work, retrying
// while it is still starting.
//
// The result is reported, never fatal. A wrong account or group is not
// fixed by restarting, so failing the liveness probe would only cost the
// board its uptime — the same reasoning that keeps a disconnected bridge
// green. What it must not do is stay quiet: silence is exactly what this
// failure already looks like.
func (b *Bridge) verify(ctx context.Context) {
	delay := minVerifyDelay
	for attempt := 1; ; attempt++ {
		result, err := b.verifier.check(ctx)
		if err == nil {
			b.health.verified(result)
			switch {
			case result.OK():
				b.logger.Info("signal configuration verified",
					"account_registered", true, "group_matched", true)
			default:
				// Error, not Warn: the bridge will look perfectly healthy
				// and file nothing, and this line is the only warning
				// anybody gets.
				b.logger.Error("the signal configuration cannot work; "+
					"the bridge will connect and receive nothing",
					"problem", result.Problem)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		if attempt >= maxVerifyAttempts {
			b.logger.Warn("could not verify the signal configuration; "+
				"signal-cli did not answer, so a misconfiguration would go unnoticed",
				"attempts", attempt, "error", err)
			return
		}
		b.logger.Debug("signal not ready to verify against yet, retrying",
			"attempt", attempt, "in", delay, "error", err)
		sleepContext(ctx, delay)
		if delay < maxVerifyDelay {
			delay *= 2
		}
	}
}
