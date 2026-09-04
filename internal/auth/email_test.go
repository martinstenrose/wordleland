package auth

import (
	"errors"
	"net/smtp"
	"strings"
	"testing"
)

// recordingMailer captures what would have been sent.
func recordingMailer(t *testing.T) (*Mailer, *struct {
	Addr string
	From string
	To   []string
	Msg  string
}) {
	t.Helper()

	captured := &struct {
		Addr string
		From string
		To   []string
		Msg  string
	}{}
	m := NewMailer("smtp.example.tld", "587", "user", "pass", "wordle@example.tld")
	m.send = func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		captured.Addr, captured.From, captured.To, captured.Msg = addr, from, to, string(msg)
		return nil
	}
	return m, captured
}

// : an unconfigured mailer is the intended default, not an error state.
func TestMailerUnconfigured(t *testing.T) {
	for _, m := range []*Mailer{
		NewMailer("", "587", "", "", "wordle@example.tld"),
		NewMailer("smtp.example.tld", "587", "", "", ""),
		NewMailer("", "", "", "", ""),
	} {
		if m.Configured() {
			t.Error("Configured() = true for an incomplete configuration")
		}
		if err := m.Send("martin@example.tld", "Subject", "Body"); !errors.Is(err, ErrMailUnavailable) {
			t.Errorf("Send() error = %v, want ErrMailUnavailable", err)
		}
	}
}

func TestMailerSends(t *testing.T) {
	m, captured := recordingMailer(t)

	if err := m.Send("martin@example.tld", "Reset your password", "Follow this link."); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	if captured.Addr != "smtp.example.tld:587" {
		t.Errorf("addr = %q", captured.Addr)
	}
	if captured.From != "wordle@example.tld" {
		t.Errorf("from = %q", captured.From)
	}
	if len(captured.To) != 1 || captured.To[0] != "martin@example.tld" {
		t.Errorf("to = %v", captured.To)
	}
	for _, want := range []string{
		"From: wordle@example.tld",
		"To: martin@example.tld",
		"Subject: Reset your password",
		"Message-ID: <",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"Follow this link.",
	} {
		if !strings.Contains(captured.Msg, want) {
			t.Errorf("message is missing %q:\n%s", want, captured.Msg)
		}
	}
}

func TestMailerGeneratesUniqueMessageIDs(t *testing.T) {
	first := string(buildMessage("Wordleland <wordle@example.tld>", "reader@example.tld", "One", "Body"))
	second := string(buildMessage("Wordleland <wordle@example.tld>", "reader@example.tld", "Two", "Body"))
	id := func(message string) string {
		for _, line := range strings.Split(message, "\r\n") {
			if strings.HasPrefix(line, "Message-ID: ") {
				return strings.TrimPrefix(line, "Message-ID: ")
			}
		}
		return ""
	}
	if id(first) == "" || id(second) == "" {
		t.Fatal("a message is missing its Message-ID")
	}
	if id(first) == id(second) {
		t.Errorf("two messages share Message-ID %q", id(first))
	}
	if !strings.HasSuffix(id(first), "@example.tld>") {
		t.Errorf("Message-ID %q does not use the sender's domain", id(first))
	}
}

// A newline in a header value would let a caller append headers or a second
// recipient. Subjects are constants today; this keeps that from mattering.
func TestMailerStripsHeaderInjection(t *testing.T) {
	m, captured := recordingMailer(t)

	if err := m.Send("martin@example.tld",
		"Reset\r\nBcc: attacker@example.tld", "Body"); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	headers, _, found := strings.Cut(captured.Msg, "\r\n\r\n")
	if !found {
		t.Fatalf("message has no header/body separator:\n%s", captured.Msg)
	}

	// The text may still appear inside the Subject value — that is harmless.
	// What matters is that it is not a header in its own right, so check for
	// a line that starts one rather than for the substring anywhere.
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Errorf("an injected header became a real header:\n%s", headers)
		}
	}
	if got := strings.Count(headers, "Subject:"); got != 1 {
		t.Errorf("Subject appears %d times, want 1: the value broke across lines", got)
	}
}

// The subject is built from constants; the recipient is an address somebody
// typed into a form, which makes it the more likely way a newline arrives.
// Both message builders are covered, because only one of them was reachable
// from Send.
func TestMailerStripsHeaderInjectionFromTheRecipient(t *testing.T) {
	const injected = "victim@example.tld\r\nBcc: attacker@example.tld"

	for _, tc := range []struct {
		name string
		send func(*Mailer) error
	}{
		{"plain", func(m *Mailer) error {
			return m.Send(injected, "Reset your password", "Body")
		}},
		{"multipart", func(m *Mailer) error {
			return m.SendHTML(injected, "Reset your password", "Body", "<p>Body</p>")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, captured := recordingMailer(t)
			if err := tc.send(m); err != nil {
				t.Fatalf("send failed: %v", err)
			}

			headers, _, found := strings.Cut(captured.Msg, "\r\n\r\n")
			if !found {
				t.Fatalf("message has no header/body separator:\n%s", captured.Msg)
			}
			for _, line := range strings.Split(headers, "\r\n") {
				if strings.HasPrefix(strings.ToLower(line), "bcc:") {
					t.Errorf("an injected header became a real header:\n%s", headers)
				}
			}
			if got := strings.Count(headers, "To:"); got != 1 {
				t.Errorf("To appears %d times, want 1: the value broke across lines", got)
			}
		})
	}
}

func TestMailerDefaultsPort(t *testing.T) {
	m, captured := recordingMailer(t)
	m.port = ""

	if err := m.Send("martin@example.tld", "Subject", "Body"); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	if !strings.HasSuffix(captured.Addr, ":587") {
		t.Errorf("addr = %q, want the default submission port", captured.Addr)
	}
}
