package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strings"
)

// emailFS holds the message layout.
//
//go:embed emails
var emailFS embed.FS

// emailPanel is the boxed summary some messages carry.
type emailPanel struct {
	Label  string
	Title  string
	Detail string
}

// emailAside is the boxed note at the foot of a message.
type emailAside struct {
	Title string
	Body  string
}

// emailPage is one message, filled from localised strings.
//
// The layout is shared so every message looks like the others; only the
// copy and the one action differ. Nothing here is HTML: the template
// escapes it, which matters because some of these carry an address a person
// typed.
type emailPage struct {
	Lang    string
	AppName string
	Subject string

	// Preview is the hidden line a mail client shows beside the subject.
	Preview string

	Heading string
	Intro   string

	Panel *emailPanel

	ActionURL   string
	ActionLabel string
	ActionNote  string

	Aside *emailAside
	Meta  string

	Footer string
}

var emailTemplate = template.Must(template.ParseFS(emailFS, "emails/*.html"))

// renderEmail produces the HTML and plain-text parts of one message.
//
// Both come from the same fields, so they cannot drift: a change to the copy
// changes both, and there is no second template to forget.
func (s *Server) renderEmail(page emailPage) (html, text string, err error) {
	var buf bytes.Buffer
	if err := emailTemplate.ExecuteTemplate(&buf, "email", page); err != nil {
		return "", "", fmt.Errorf("render email: %w", err)
	}

	var b strings.Builder
	b.WriteString(page.Heading)
	b.WriteString("\n\n")
	b.WriteString(page.Intro)
	b.WriteString("\n\n")
	if page.Panel != nil {
		fmt.Fprintf(&b, "%s: %s\n%s\n\n", page.Panel.Label, page.Panel.Title, page.Panel.Detail)
	}
	// The URL on its own line and unadorned, so a client that does not
	// linkify it still leaves something a reader can copy.
	fmt.Fprintf(&b, "%s:\n%s\n\n", page.ActionLabel, page.ActionURL)
	b.WriteString(page.ActionNote)
	b.WriteString("\n\n")
	if page.Aside != nil {
		fmt.Fprintf(&b, "%s\n%s\n\n", page.Aside.Title, page.Aside.Body)
	}
	if page.Meta != "" {
		b.WriteString(page.Meta)
		b.WriteString("\n\n")
	}
	b.WriteString(strings.ReplaceAll(page.Footer, "\n", " "))
	b.WriteString("\n")

	return buf.String(), b.String(), nil
}

// send delivers a rendered message, falling back to plain text when the
// mailer cannot do better.
func (s *Server) sendEmail(to string, page emailPage) error {
	html, text, err := s.renderEmail(page)
	if err != nil {
		return err
	}
	return s.mailer.SendHTML(to, page.Subject, text, html)
}
