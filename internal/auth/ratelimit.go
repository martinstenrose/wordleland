package auth

import (
	"context"
	"sync"
	"time"
)

// Limiter defaults. Six digits is a million TOTP possibilities and passwords
// are worse, so the point is to make guessing take longer than anyone will
// wait — not to be unguessable in a single window.
const (
	DefaultMaxAttempts = 10
	DefaultWindow      = 15 * time.Minute
)

// maxConcurrentHashes bounds simultaneous argon2 calls.
//
// Each costs 64 MiB (see argonMemory), so an unthrottled login endpoint is a
// memory-exhaustion DoS regardless of whether any credential is ever guessed:
// a few hundred concurrent requests would be several gigabytes. The rate
// limiter alone does not cover this, because a flood of requests against many
// distinct accounts passes every per-key check.
const maxConcurrentHashes = 4

// Limiter is a fixed-window counter, keyed by account and by client address.
//
// State is in memory rather than in the database. DB-backed counters
// would turn every failed attempt into a write, and writes serialise in
// SQLite, so a flood could stall ingest or a CLI command behind busy_timeout —
// an attacker could deny service without guessing anything. Losing the counts
// on restart is the accepted cost; a restart is not a tool an attacker has.
type Limiter struct {
	maxAttempts int
	window      time.Duration
	now         func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket

	// Concurrency is bounded separately from the rate limit because they
	// defend different things: the limiter stops guessing, the semaphore
	// stops memory exhaustion.
	hashSlots chan struct{}
}

type bucket struct {
	count       int
	windowStart time.Time
}

// NewLimiter builds a limiter. A zero maxAttempts or window uses the defaults.
func NewLimiter(maxAttempts int, window time.Duration) *Limiter {
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return &Limiter{
		maxAttempts: maxAttempts,
		window:      window,
		now:         time.Now,
		buckets:     make(map[string]*bucket),
		hashSlots:   make(chan struct{}, maxConcurrentHashes),
	}
}

// Allow reports whether an attempt against every one of keys may proceed, and
// counts it against all of them.
//
// Callers pass both the account and the client address, so neither a single
// address spraying many accounts nor many addresses targeting one account
// slips through. It must be called before any expensive work: checking after
// hashing means a blocked attempt still costs 64 MiB and the CPU time, which
// is exactly what an attacker wants.
func (l *Limiter) Allow(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	// Every key is checked before any is incremented, so a request rejected
	// on one key does not consume budget on the others.
	for _, key := range keys {
		b, ok := l.buckets[key]
		if !ok || now.Sub(b.windowStart) >= l.window {
			continue
		}
		if b.count >= l.maxAttempts {
			return false
		}
	}

	for _, key := range keys {
		b, ok := l.buckets[key]
		if !ok || now.Sub(b.windowStart) >= l.window {
			l.buckets[key] = &bucket{count: 1, windowStart: now}
			continue
		}
		b.count++
	}
	return true
}

// Reset clears the counters for keys, called after a successful attempt so a
// legitimate user is not punished for earlier typos.
func (l *Limiter) Reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.buckets, key)
	}
}

// Cleanup drops expired buckets.
//
// Without it the map grows with every distinct key ever seen, which for the
// client-address key is attacker-controlled: a slow scan from many addresses
// would otherwise be a memory leak with no upper bound.
func (l *Limiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for key, b := range l.buckets {
		if now.Sub(b.windowStart) >= l.window {
			delete(l.buckets, key)
		}
	}
}

// WithHashSlot runs fn while holding one of the bounded hashing slots,
// waiting until one is free or ctx is done.
//
// Waiting rather than rejecting is deliberate: under honest load this adds
// latency to a login, while under attack it caps memory. Returning an error to
// real users the moment four logins overlap would be the worse trade.
func (l *Limiter) WithHashSlot(ctx context.Context, fn func() error) error {
	select {
	case l.hashSlots <- struct{}{}:
		defer func() { <-l.hashSlots }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bucketCount reports how many buckets are held, for tests.
func (l *Limiter) bucketCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
