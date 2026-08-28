package auth

import (
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// ErrMailUnavailable reports that SMTP is not configured.
//
// Not a failure to be logged as an error: a deployment with no mail server
// is a supported way to run this. The flows that need mail become
// unavailable and everything else runs normally, and the CLI's reset-password
// remains the path that always works.
var ErrMailUnavailable = errors.New("email is not configured")

// Mailer sends the two messages Wordleland has: a password reset and an
// address verification.
type Mailer struct {
	host string
	port string
	user string
	pass string
	from string

	// send is swapped in tests. There is no fake SMTP server in the suite,
	// because what matters is the message built, not the wire protocol.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// NewMailer builds a Mailer. Empty host or from yields one that reports
// ErrMailUnavailable from every Send.
func NewMailer(host, port, user, pass, from string) *Mailer {
	return &Mailer{
		host: host, port: port, user: user, pass: pass, from: from,
		send: smtp.SendMail,
	}
}

// Configured reports whether mail can be sent.
func (m *Mailer) Configured() bool {
	return m != nil && m.host != "" && m.from != ""
}

// Send delivers a plain-text message.
func (m *Mailer) Send(to, subject, body string) error {
	return m.deliver(to, buildMessage(m.from, to, subject, body))
}

// SendHTML delivers a message with both a plain-text and an HTML part.
//
// Both are sent, always. A mail client that will not render HTML — or a
// reader who has turned it off — still gets a message with the link in it,
// which for a sign-in or reset mail is the whole point.
func (m *Mailer) SendHTML(to, subject, text, html string) error {
	return m.deliver(to, buildMultipart(m.from, to, subject, text, html))
}

func (m *Mailer) deliver(to string, msg []byte) error {
	if !m.Configured() {
		return ErrMailUnavailable
	}

	port := m.port
	if port == "" {
		port = "587"
	}

	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}

	if err := m.send(net.JoinHostPort(m.host, port), auth, m.from, []string{to}, msg); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// buildMessage assembles RFC 5322 headers and body.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	// Header injection defence: a newline in the subject would otherwise let
	// a caller add headers or a second recipient. Subjects here are built
	// from constants, but the sanitiser makes that a property rather than a
	// coincidence that a later change could break.
	fmt.Fprintf(&b, "Subject: %s\r\n", sanitizeHeader(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// buildMultipart assembles a multipart/alternative message.
//
// The plain part comes first, which is the order the standard asks for: a
// client picks the last part it can render, so HTML wins where it is
// wanted and text is what is left where it is not.
func buildMultipart(from, to, subject, text, html string) []byte {
	// A boundary that cannot appear in either part, since both are ours and
	// neither contains this.
	const boundary = "----=_wordleland_alt_boundary"

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", sanitizeHeader(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(text)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(html)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

// sanitizeHeader strips CR and LF from a header value.
func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// SetSender swaps the transport, for tests. What matters in the suite is the
// message built, not the wire protocol, so there is no fake SMTP server.
func (m *Mailer) SetSender(send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error) {
	m.send = send
}
