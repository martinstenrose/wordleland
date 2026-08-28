package bridge

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a manually advanced clock, so thresholds measured in hours are
// tested without waiting for them.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestHealthStatus(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*health, *clock)
		wantOK     bool
		wantReason string
	}{
		{
			// Before the first connection there is nothing wrong yet.
			name:   "just started",
			setup:  func(*health, *clock) {},
			wantOK: true,
		},
		{
			name:   "connected",
			setup:  func(h *health, _ *clock) { h.connected() },
			wantOK: true, wantReason: "connected",
		},
		{
			// Reconnection is in progress; failing here would make every
			// upstream restart look like an outage.
			name: "briefly disconnected",
			setup: func(h *health, c *clock) {
				h.connected()
				h.disconnected()
				c.advance(30 * time.Second)
			},
			wantOK: true, wantReason: "reconnecting",
		},
		{
			// Backoff caps at a minute, so this long means repeated
			// failures rather than one blip — and every message arriving
			// meanwhile is being dropped upstream.
			name: "disconnected past the threshold",
			setup: func(h *health, c *clock) {
				h.connected()
				h.disconnected()
				c.advance(maxDisconnected + time.Minute)
			},
			wantOK: false, wantReason: "disconnected from signal",
		},
		{
			name: "connected and receiving",
			setup: func(h *health, c *clock) {
				h.connected()
				h.received()
				c.advance(2 * time.Hour)
			},
			wantOK: true,
		},
		{
			// The socket is open and answering pings but nothing arrives:
			// the failure that would otherwise be completely invisible.
			name: "connected but silent for too long",
			setup: func(h *health, c *clock) {
				h.connected()
				h.received()
				c.advance(maxSilence + time.Hour)
			},
			wantOK: false, wantReason: "nothing received",
		},
		{
			// A filer deployed an hour ago has no baseline. Failing on
			// that would make every deploy look broken until the next post.
			name: "connected, nothing received yet",
			setup: func(h *health, c *clock) {
				h.connected()
				c.advance(maxSilence + time.Hour)
			},
			wantOK: true,
		},
		{
			name: "reconnected after a long outage",
			setup: func(h *health, c *clock) {
				h.connected()
				h.disconnected()
				c.advance(time.Hour)
				h.connected()
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			h := newHealth(c.Now)
			tt.setup(h, c)

			ok, reason := h.status()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v (%s), want %v", ok, reason, tt.wantOK)
			}
			if tt.wantReason != "" && !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to mention %q", reason, tt.wantReason)
			}
		})
	}
}

// Status is what the diagnostics page and the liveness probe both read, so
// it has to say what is wrong as well as that something is.
func TestStatusSnapshot(t *testing.T) {
	c := newClock()
	h := newHealth(c.Now)
	h.connected()

	if got := h.snapshot(); !got.OK || !got.Connected {
		t.Errorf("snapshot while connected = %+v, want OK and connected", got)
	}

	h.disconnected()
	c.advance(maxDisconnected + time.Minute)

	got := h.snapshot()
	if got.OK {
		t.Error("still OK after being disconnected past the threshold")
	}
	if got.Connected {
		t.Error("reports itself connected while disconnected")
	}
	if !strings.Contains(got.Reason, "disconnected") {
		t.Errorf("Reason = %q, want it to say why", got.Reason)
	}
	if got.Since.IsZero() {
		t.Error("Since is zero, so the page cannot say how long")
	}
}

// The status reaches an admin page and a probe body, so it must not carry
// anything about who is in the group.
func TestStatusSaysNothingIdentifying(t *testing.T) {
	c := newClock()
	h := newHealth(c.Now)
	h.connected()
	h.received()
	c.advance(maxSilence + time.Hour)

	got := h.snapshot()
	for _, secret := range []string{testUUID, testName, testGroupID} {
		if strings.Contains(got.Reason, secret) {
			t.Errorf("the status reason leaks %q", secret)
		}
	}
}

// The gap this closes: a filer that receives perfectly and cannot reach
// the store loses every result it handles, and used to report itself
// healthy throughout. A misconfigured network is exactly that shape.
func TestHealthCoversDelivery(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*health, *clock)
		wantOK     bool
		wantReason string
	}{
		{
			name:   "delivering normally",
			setup:  func(h *health, _ *clock) { h.connected(); h.deliverySucceeded() },
			wantOK: true,
		},
		{
			// A restart is ordinary, and one result's retry
			// budget is about half a minute.
			name: "failing briefly",
			setup: func(h *health, c *clock) {
				h.connected()
				h.deliveryFailed()
				c.advance(20 * time.Second)
			},
			wantOK: true,
		},
		{
			name: "failing past the threshold",
			setup: func(h *health, c *clock) {
				h.connected()
				h.deliveryFailed()
				c.advance(maxDeliveryFailure + time.Minute)
			},
			wantOK: false, wantReason: "cannot file results",
		},
		{
			// Nothing redelivers a dropped result, so this stays reportable
			// until something gets through again.
			name: "a result was lost",
			setup: func(h *health, c *clock) {
				h.connected()
				h.deliveryDropped()
			},
			wantOK: false, wantReason: "result(s) lost",
		},
		{
			name: "recovers once delivery works again",
			setup: func(h *health, c *clock) {
				h.connected()
				h.deliveryFailed()
				c.advance(maxDeliveryFailure + time.Minute)
				h.deliverySucceeded()
			},
			wantOK: true,
		},
		{
			// Losing the Signal connection and failing to file are
			// different failures; both must be reportable.
			name: "signal fine, store unreachable",
			setup: func(h *health, c *clock) {
				h.connected()
				h.received()
				h.deliveryFailed()
				c.advance(maxDeliveryFailure + time.Minute)
			},
			wantOK: false, wantReason: "cannot file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			h := newHealth(c.Now)
			tt.setup(h, c)

			ok, reason := h.status()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v (%s), want %v", ok, reason, tt.wantOK)
			}
			if tt.wantReason != "" && !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to mention %q", reason, tt.wantReason)
			}
		})
	}
}

// The failure clock measures how long delivery has been broken, not how
// long ago the most recent attempt was — otherwise a retry every few
// seconds would keep resetting it and the threshold would never be reached.
func TestHealthDeliveryClockDoesNotResetOnEachFailure(t *testing.T) {
	c := newClock()
	h := newHealth(c.Now)
	h.connected()

	for i := 0; i < 10; i++ {
		h.deliveryFailed()
		c.advance(30 * time.Second)
	}

	ok, reason := h.status()
	if ok {
		t.Fatalf("healthy after five minutes of failures: %s", reason)
	}
	if !strings.Contains(reason, "cannot file results") {
		t.Errorf("reason = %q", reason)
	}
}
