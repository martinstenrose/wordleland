package bridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeRemote(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"ordinary text is untouched", "rate limit exceeded", "rate limit exceeded"},
		{"a puzzle number survives", "no result for 1500", "no result for 1500"},

		// Log injection: a newline lets the value close the line and write
		// a convincing fake one under it.
		{"newlines cannot forge a line",
			"failed\nlevel=INFO msg=\"all clear\"",
			"failed level=INFO msg=\"all clear\""},
		{"carriage returns and tabs go too", "a\r\tb", "a  b"},

		// Phone numbers, in the shapes signal-cli actually writes them.
		{"E.164", "unregistered: +46701234567", "unregistered: [number]"},
		{"spaced", "recipient +46 70 123 45 67 is unknown", "recipient [number] is unknown"},
		{"hyphenated", "number 070-123-45-67 failed", "number [number] failed"},
		{"bare digits", "account 46701234567 not linked", "account [number] not linked"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeRemote(tt.in); got != tt.want {
				t.Errorf("sanitizeRemote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A remote string decides its own length, and a log line is not the place to
// find that out.
func TestSanitizeRemoteIsBounded(t *testing.T) {
	got := sanitizeRemote(strings.Repeat("a", 5000))
	if len(got) > maxRemoteLen+len("…") {
		t.Errorf("length %d, want it capped near %d", len(got), maxRemoteLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a truncated value does not say it was truncated")
	}
	// Multi-byte input must not be cut into invalid UTF-8: the cap is a
	// byte count, and "ä" straddles it.
	if got := sanitizeRemote(strings.Repeat("ä", 5000)); !utf8.ValidString(got) {
		t.Errorf("truncation produced invalid UTF-8: %q", got)
	}
}

// The whole point, end to end: what signal-cli says reaches the log
// sanitised, not verbatim.
func TestDecodeDoesNotLogARemoteErrorVerbatim(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	frame, err := json.Marshal(map[string]string{
		"error": "send failed for +46701234567\nlevel=INFO msg=\"nothing wrong\"",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, ok := decode(frame, logger); ok {
		t.Fatal("an error frame decoded as a message")
	}

	logged := buf.String()
	if strings.Contains(logged, "46701234567") {
		t.Error("a phone number from signal-cli reached the log")
	}
	if strings.Contains(logged, "[number]") == false {
		t.Errorf("the number was not redacted: %s", logged)
	}
}
