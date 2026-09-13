package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// A stranger reaching /privacy must not be offered the view pills: with no
// prefix they point at "/today" and friends, which need a session and would
// just bounce back to login.
func TestPrivacyPageForAStranger(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	body := fetchAs(t, srv, "/privacy", nil).Body.String()
	if strings.Contains(body, `class="views-mobile"`) || strings.Contains(body, `class="pill`) {
		t.Error("an anonymous visitor is offered a view pill that needs a session")
	}
	if strings.Contains(body, `href="/today"`) {
		t.Error("an anonymous visitor is linked to Today, which requires a session")
	}
	if !strings.Contains(body, `<a class="brand" href="/">`) {
		t.Error("the anonymous brand link does not lead directly to sign-in")
	}
	if !strings.Contains(body, `<a class="btn-primary" href="/" aria-label="Sign in">`) {
		t.Error("no sign-in button for an anonymous visitor")
	}
	if strings.Contains(body, "account-menu") {
		t.Error("an anonymous visitor is offered the account menu")
	}
}

// A signed-in reader gets the same chrome as everywhere else: the view
// pills work because a session exists to carry them, and the account menu
// names them.
func TestPrivacyPageForASignedInReader(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")

	body := fetchAs(t, srv, "/privacy", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(body, `href="/today"`) {
		t.Error("a signed-in reader lost the view pills")
	}
	if !strings.Contains(body, `<a class="brand" href="/today">`) {
		t.Error("the signed-in brand link does not lead to Today")
	}
	if !strings.Contains(body, "account-menu") {
		t.Error("a signed-in reader has no account menu")
	}
}

func TestPrivacyPageIsPublic(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	if got := fetchAs(t, srv, "/privacy", nil).Code; got != http.StatusOK {
		t.Errorf("GET /privacy with no session = %d, want 200", got)
	}
}
