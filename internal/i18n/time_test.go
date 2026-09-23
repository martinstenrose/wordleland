package i18n

import (
	"testing"
	"time"
)

// The database hands times back in UTC; what a person reads is the
// server's clock, with its offset.
func TestTimestampIsOnTheServersClock(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	at := time.Date(2026, time.September, 23, 3, 29, 14, 67e6, time.UTC)
	if got, want := timestampIn(at, cest), "2026-09-23 05:29:14 +0200"; got != want {
		t.Errorf("Timestamp = %q, want %q", got, want)
	}
	if got, want := clockIn(at, cest), "05:29:14 +0200"; got != want {
		t.Errorf("ClockTime = %q, want %q", got, want)
	}
}
