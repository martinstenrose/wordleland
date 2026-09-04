package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// quietLogger keeps panic stacks out of the test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// superviseFunc runs an arbitrary body under the supervisor, standing in for
// a bridge so a panic can be provoked without a websocket.
func superviseFunc(t *testing.T, body func(context.Context) error) *Supervisor {
	t.Helper()
	s := &Supervisor{logger: quietLogger(), sleep: func(context.Context, time.Duration) {}}
	s.bridge = &Bridge{logger: quietLogger(), health: newHealth(time.Now)}
	s.run = body
	return s
}

// A panic in the bridge must not escape. Unrecovered it would take the web
// server down with it, which is the whole reason the supervisor exists.
func TestSupervisorContainsAPanic(t *testing.T) {
	var attempts int
	var mu sync.Mutex

	s := superviseFunc(t, func(ctx context.Context) error {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			panic("boom")
		}
		<-ctx.Done()
		return ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()

	// It should recover and get as far as a second attempt.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the supervisor did not restart after a panic; attempts = %d", n)
		case <-time.After(5 * time.Millisecond):
		}
	}

	if alive, _ := s.Alive(); !alive {
		t.Error("the bridge should be alive again after one panic")
	}
	cancel()
	<-done
}

// Panicking over and over is a bug, not bad luck. The supervisor gives up,
// and giving up is what makes it visible: a configured bridge that is not
// running fails the liveness probe.
func TestSupervisorGivesUpOnAPanicLoop(t *testing.T) {
	var attempts int
	var mu sync.Mutex

	s := superviseFunc(t, func(context.Context) error {
		mu.Lock()
		attempts++
		mu.Unlock()
		panic("always")
	})

	done := make(chan struct{})
	go func() { defer close(done); s.Run(context.Background()) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never gave up on a bridge that always panics")
	}

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != maxPanics {
		t.Errorf("tried %d times, want %d", got, maxPanics)
	}

	alive, reason := s.Alive()
	if alive {
		t.Error("a bridge that was abandoned still reports itself alive")
	}
	if reason == "" {
		t.Error("giving up recorded no reason, so nothing can explain the unhealthy probe")
	}
}

// An ordinary shutdown is not a fault: nothing should be reported as a
// reason, or every deploy would look like a crash.
func TestSupervisorShutdownIsNotAFault(t *testing.T) {
	s := superviseFunc(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the supervisor did not stop when the context was cancelled")
	}

	alive, reason := s.Alive()
	if alive {
		t.Error("still alive after shutdown")
	}
	if reason != "" {
		t.Errorf("shutdown recorded %q as a fault", reason)
	}
}

// A bridge that returns an error rather than panicking is restarted too:
// the websocket giving up is not a reason to stop bridging for ever.
func TestSupervisorRestartsOnError(t *testing.T) {
	var attempts int
	var mu sync.Mutex

	s := superviseFunc(t, func(ctx context.Context) error {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n < 3 {
			return errors.New("connection lost")
		}
		<-ctx.Done()
		return ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the supervisor stopped restarting after an error; attempts = %d", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// The delay between a panic and the attempt that follows it is not a fault.
// No attempt is running through that window, and answering the liveness
// probe with "dead" would have the container bounced for something the
// supervisor was already recovering from — on a 503 carrying no reason,
// which reads exactly like the case where it had given up for good.
//
// The other half of the contract, flipping to dead once it does give up, is
// TestSupervisorGivesUpOnAPanicLoop.
func TestSupervisorStaysAliveDuringTheRestartDelay(t *testing.T) {
	var attempts int
	var mu sync.Mutex

	s := superviseFunc(t, func(ctx context.Context) error {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			panic("boom")
		}
		<-ctx.Done()
		return ctx.Err()
	})

	// Hold the supervisor inside the restart delay so the window can be
	// observed at all; the stub sleep it comes with returns immediately.
	sleeping := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.sleep = func(context.Context, time.Duration) {
		once.Do(func() {
			close(sleeping)
			<-resume
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()

	select {
	case <-sleeping:
	case <-time.After(2 * time.Second):
		t.Fatal("the supervisor never reached the restart delay after a panic")
	}

	if alive, reason := s.Alive(); !alive || reason != "" {
		t.Errorf("mid-restart Alive() = (%v, %q), want (true, %q)", alive, reason, "")
	}

	close(resume)
	cancel()
	<-done
}
