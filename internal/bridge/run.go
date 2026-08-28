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
)

// Bridge is a running Signal bridge.
type Bridge struct {
	health *health
	source *websocketSource
	filer  *filer
	logger *slog.Logger
}

// New builds a bridge. It does not connect: that happens in Run.
func New(cfg config.Bridge, deliver Deliverer, logger *slog.Logger) (*Bridge, error) {
	h := newHealth(time.Now)
	source, err := newWebsocketSource(cfg.SignalAPIURL, cfg.SignalAccount, logger, h)
	if err != nil {
		return nil, fmt.Errorf("signal source: %w", err)
	}
	return &Bridge{
		health: h,
		source: source,
		filer:  newFiler(cfg.SignalGroupID, deliver, logger, h),
		logger: logger,
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

	err := b.source.Run(ctx, queue)

	close(queue)
	select {
	case <-done:
	case <-time.After(drainTimeout):
		b.logger.Warn("gave up draining queued results", "timeout", drainTimeout)
	}
	return err
}
