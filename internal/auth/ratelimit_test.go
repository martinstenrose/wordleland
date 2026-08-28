package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testLimiter returns a limiter with a clock the test controls, so window
// behaviour is asserted rather than slept through.
func testLimiter(t *testing.T, maxAttempts int, window time.Duration) (*Limiter, func(time.Duration)) {
	t.Helper()

	l := NewLimiter(maxAttempts, window)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	l.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return l, func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
}

func TestLimiterAllowsUpToMax(t *testing.T) {
	l, _ := testLimiter(t, 3, time.Minute)

	for i := 0; i < 3; i++ {
		if !l.Allow("user:martin") {
			t.Fatalf("attempt %d was blocked, want allowed", i+1)
		}
	}
	if l.Allow("user:martin") {
		t.Error("the fourth attempt was allowed, want blocked")
	}
}

func TestLimiterWindowExpires(t *testing.T) {
	l, advance := testLimiter(t, 2, time.Minute)

	l.Allow("user:martin")
	l.Allow("user:martin")
	if l.Allow("user:martin") {
		t.Fatal("attempt was allowed past the limit")
	}

	advance(time.Minute)
	if !l.Allow("user:martin") {
		t.Error("attempt was blocked after the window expired")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _ := testLimiter(t, 2, time.Minute)

	l.Allow("user:martin")
	l.Allow("user:martin")
	if l.Allow("user:martin") {
		t.Fatal("martin should be blocked")
	}
	if !l.Allow("user:alex") {
		t.Error("a different account was blocked by martin's attempts")
	}
}

// Both keys are checked so neither one address spraying many accounts nor
// many addresses targeting one account slips through.
func TestLimiterBlocksIfAnyKeyIsExhausted(t *testing.T) {
	l, _ := testLimiter(t, 2, time.Minute)

	// The address exhausts its budget against two different accounts.
	l.Allow("user:a", "ip:203.0.113.7")
	l.Allow("user:b", "ip:203.0.113.7")

	if l.Allow("user:c", "ip:203.0.113.7") {
		t.Error("a third account from the same address was allowed; the address budget was not enforced")
	}
}

// A request rejected on one key must not consume budget on the others, or a
// blocked address would silently burn down every account it named.
func TestLimiterDoesNotChargeWhenBlocked(t *testing.T) {
	l, _ := testLimiter(t, 2, time.Minute)

	l.Allow("ip:203.0.113.7")
	l.Allow("ip:203.0.113.7")

	// Blocked by the address, but the account must be untouched.
	if l.Allow("user:martin", "ip:203.0.113.7") {
		t.Fatal("attempt was allowed past the address limit")
	}

	for i := 0; i < 2; i++ {
		if !l.Allow("user:martin") {
			t.Errorf("account attempt %d was blocked; the account was charged for an address-blocked request", i+1)
		}
	}
}

// A legitimate user who mistypes twice and then succeeds should not be
// punished for the rest of the window.
func TestLimiterReset(t *testing.T) {
	l, _ := testLimiter(t, 3, time.Minute)

	l.Allow("user:martin")
	l.Allow("user:martin")
	l.Reset("user:martin")

	for i := 0; i < 3; i++ {
		if !l.Allow("user:martin") {
			t.Errorf("attempt %d after reset was blocked", i+1)
		}
	}
}

// The address key is attacker-controlled, so expired buckets must be
// reclaimed or a slow scan is an unbounded memory leak.
func TestLimiterCleanup(t *testing.T) {
	l, advance := testLimiter(t, 5, time.Minute)

	for _, key := range []string{"ip:1.1.1.1", "ip:2.2.2.2", "ip:3.3.3.3"} {
		l.Allow(key)
	}
	if got := l.bucketCount(); got != 3 {
		t.Fatalf("buckets = %d, want 3", got)
	}

	l.Cleanup()
	if got := l.bucketCount(); got != 3 {
		t.Errorf("buckets = %d after cleaning a live window, want 3", got)
	}

	advance(time.Minute)
	l.Cleanup()
	if got := l.bucketCount(); got != 0 {
		t.Errorf("buckets = %d after the window expired, want 0", got)
	}
}

// Each argon2 call costs 64 MiB, so concurrency is capped independently of the
// rate limit: a flood across many distinct accounts passes every per-key check
// but would still exhaust memory.
func TestWithHashSlotBoundsConcurrency(t *testing.T) {
	l := NewLimiter(0, 0)

	var (
		concurrent atomic.Int32
		peak       atomic.Int32
		wg         sync.WaitGroup
	)
	release := make(chan struct{})

	for i := 0; i < maxConcurrentHashes*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.WithHashSlot(context.Background(), func() error {
				n := concurrent.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				<-release
				concurrent.Add(-1)
				return nil
			})
		}()
	}

	// Let the first wave pile up against the semaphore before releasing.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := peak.Load(); got > maxConcurrentHashes {
		t.Errorf("peak concurrent hashes = %d, want at most %d", got, maxConcurrentHashes)
	}
}

// Under honest load the semaphore adds latency; it must not turn a slow login
// into a lost one when the caller is still waiting.
func TestWithHashSlotHonoursContext(t *testing.T) {
	l := NewLimiter(0, 0)

	// Fill every slot and hold them.
	release := make(chan struct{})
	defer close(release)
	for i := 0; i < maxConcurrentHashes; i++ {
		go func() {
			_ = l.WithHashSlot(context.Background(), func() error {
				<-release
				return nil
			})
		}()
	}
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := l.WithHashSlot(ctx, func() error {
		t.Error("the function ran despite every slot being held")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestWithHashSlotPropagatesError(t *testing.T) {
	l := NewLimiter(0, 0)
	sentinel := errors.New("inner failure")

	if err := l.WithHashSlot(context.Background(), func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want the callback's error", err)
	}

	// The slot must be returned even when the callback fails, or a few errors
	// would permanently starve the endpoint.
	for i := 0; i < maxConcurrentHashes+1; i++ {
		if err := l.WithHashSlot(context.Background(), func() error { return nil }); err != nil {
			t.Fatalf("slot %d was not released: %v", i, err)
		}
	}
}
