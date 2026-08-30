package web

import (
	"regexp"
	"testing"
	"time"
)

// A clock time alone doesn't say which zone it's in, and the admin reading
// Diagnostics may not be on the server's clock. sinceText needs to carry the
// offset along with the time it renders.
func TestSinceTextIncludesUTCOffset(t *testing.T) {
	tr := translator{strings: catalogue{
		"activity.todayAt":     "today at %s",
		"activity.yesterdayAt": "yesterday at %s",
	}}
	now := time.Now()

	timestampPattern := regexp.MustCompile(`\d{2}:\d{2}:\d{2} [+-]\d{2}:\d{2}$`)

	if got := sinceText(tr, now.Add(-time.Minute), now); !timestampPattern.MatchString(got) {
		t.Errorf("today's rendering does not include seconds and a UTC offset: %q", got)
	}
	if got := sinceText(tr, now.Add(-25*time.Hour), now); !timestampPattern.MatchString(got) {
		t.Errorf("yesterday's rendering does not include seconds and a UTC offset: %q", got)
	}
}

func TestAbsoluteTimeIncludesSeconds(t *testing.T) {
	at := time.Date(2026, time.August, 30, 12, 34, 56, 0, time.Local)
	if got, want := absoluteTime(at), "2026-08-30 12:34:56 "+at.Format("-0700"); got != want {
		t.Errorf("absoluteTime() = %q, want %q", got, want)
	}
}
