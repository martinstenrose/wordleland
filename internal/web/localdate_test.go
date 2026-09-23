package web

import (
	"testing"
	"time"
)

// Half past one in the morning in Sweden is still the previous day in UTC,
// which is how the database hands it back. Tested through dateIn rather
// than by changing time.Local, which other tests' goroutines read.
func TestLocalDateIsTheServersDay(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	at := time.Date(2026, time.September, 22, 23, 30, 0, 0, time.UTC)
	if got := dateIn(at, cest); got != "2026-09-23" {
		t.Errorf("dateIn = %q, want 2026-09-23", got)
	}
}
