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

	offsetPattern := regexp.MustCompile(`[+-]\d{2}:\d{2}$`)

	if got := sinceText(tr, now.Add(-time.Minute), now); !offsetPattern.MatchString(got) {
		t.Errorf("today's rendering has no UTC offset: %q", got)
	}
	if got := sinceText(tr, now.Add(-25*time.Hour), now); !offsetPattern.MatchString(got) {
		t.Errorf("yesterday's rendering has no UTC offset: %q", got)
	}
}
