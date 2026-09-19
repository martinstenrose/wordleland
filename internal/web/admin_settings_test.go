package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// The question this screen exists to answer is "what is this deployment
// actually doing", which previously needed a shell in the container.
func TestTheSettingsScreenNamesEveryVariableThisAppReads(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/settings", session).Body.String()

	// Every variable the code reads, whether or not it is set here. A new
	// one that nobody adds to the list is exactly what this catches.
	for _, name := range []string{
		"APP_URL", "TZ", "TOTP_KEY", "TRUSTED_PROXIES",
		"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM",
		"PENDING_RETENTION", "LOG_LEVEL", "ADMIN_EMAIL", "ADMIN_PASSWORD",
		"DEMO_MODE", "SIGNAL_ACCOUNT", "SIGNAL_GROUP_ID",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("%s is not on the settings screen", name)
		}
	}
	// A secret that is set draws as a run of dots at full strength, not as a
	// greyed word: greying it would put it in the same visual class as "Not
	// set", which is the opposite of what it means. Grey marks one thing on
	// this table — that there is no value at all.
	if !strings.Contains(body, `class="env-redacted"`) {
		t.Error("a secret that is set is not shown as a redacted value")
	}
	if !strings.Contains(body, "Set, and never shown here.") {
		t.Error("nothing says what the dots stand for, so they carry it alone")
	}
	if strings.Contains(cellAround(t, body, `class="env-redacted"`), "muted") {
		t.Error("a secret is greyed, which says it is not set")
	}
	// Grey is still doing its one job: marking the rows with no value.
	if !strings.Contains(cellAround(t, body, "Not set"), "muted") {
		t.Error("an unset variable is not greyed, so the column cannot be swept")
	}
	if !strings.Contains(body, "Set via environment variable") {
		t.Error("nothing says these cannot be changed here")
	}
}

// The screen is behind requireAdmin, but it is worth pinning: it is the one
// page that collects the whole of an installation's configuration.
func TestTheSettingsScreenIsAdminsOnly(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	member := seedLogin(t, srv, "member@example.tld", false)
	session := signIn(t, srv, member.ID)

	if got := fetchAs(t, srv, "/admin/settings", session).Code; got != http.StatusNotFound {
		t.Errorf("GET /admin/settings as a member = %d, want 404", got)
	}
	if got := fetchAs(t, srv, "/admin/settings", nil).Code; got == http.StatusOK {
		t.Error("GET /admin/settings signed out = 200, want a redirect or 404")
	}
}

// Rotating breaks every link the group already has and cannot be undone, so
// it is asked before it happens — and asked without a script, because a
// question that only appears once JavaScript has run is a question that
// sometimes does not.
func TestRotatingTheSlugIsAskedFirst(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	if _, _, err := store.EnsureShareSlug(context.Background(), srv.db); err != nil {
		t.Fatalf("EnsureShareSlug: %v", err)
	}

	plain := fetchAs(t, srv, "/admin/settings", session).Body.String()
	if strings.Contains(plain, `action="/admin/settings/slug"`) {
		t.Error("the rotate form is on the page before the question is asked")
	}
	if !strings.Contains(plain, "/admin/settings?confirm=slug") {
		t.Error("nothing leads to the question")
	}

	asked := fetchAs(t, srv, "/admin/settings?confirm=slug", session).Body.String()
	if !strings.Contains(asked, `action="/admin/settings/slug"`) {
		t.Error("the question does not carry the form that answers it")
	}
	if !strings.Contains(asked, "Replace the share link?") {
		t.Error("the question is not asked")
	}
}

func TestRotatingTheSlugReplacesIt(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	before, _, err := store.EnsureShareSlug(context.Background(), srv.db)
	if err != nil {
		t.Fatalf("EnsureShareSlug: %v", err)
	}

	rec := postAdmin(t, srv, "/admin/settings/slug", url.Values{}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/settings/slug = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	// A redirect rather than a rendered page, so a reload cannot rotate a
	// second time.
	if got := rec.Header().Get("Location"); got != "/admin/settings?notice=rotated" {
		t.Errorf("Location = %q, want the settings screen with the outcome", got)
	}

	after, err := store.ShareSlug(context.Background(), srv.db)
	if err != nil {
		t.Fatalf("ShareSlug: %v", err)
	}
	if after == before {
		t.Error("the slug is unchanged")
	}

	body := fetchAs(t, srv, "/admin/settings?notice=rotated", session).Body.String()
	if !strings.Contains(body, "no longer works") {
		t.Error("the outcome is not reported")
	}
	if !strings.Contains(body, after) {
		t.Error("the new link is not shown")
	}
}

// Without a token the write does not happen — the same guard every other
// admin action carries.
func TestRotatingTheSlugNeedsItsToken(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	before, _, err := store.EnsureShareSlug(context.Background(), srv.db)
	if err != nil {
		t.Fatalf("EnsureShareSlug: %v", err)
	}

	rec := postForm(t, srv, "/admin/settings/slug", url.Values{}, []*http.Cookie{session})
	if rec.Code == http.StatusOK {
		t.Errorf("a tokenless post was accepted (status %d)", rec.Code)
	}

	after, err := store.ShareSlug(context.Background(), srv.db)
	if err != nil {
		t.Fatalf("ShareSlug: %v", err)
	}
	if after != before {
		t.Error("the slug rotated without a token")
	}
}

// The code is in the URL, where anyone can type one. It is looked up in the
// catalogue rather than printed, so an unknown code says nothing at all.
func TestAnInventedNoticeCodeSaysNothing(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/settings?notice=surprise", session).Body.String()
	// The switchers carry the whole query forward, so the code is in their
	// hrefs; what must not happen is it becoming a sentence on the page.
	if strings.Contains(body, `role="status"`) {
		t.Error("an unknown code from the query string was reported as an outcome")
	}
	if strings.Contains(body, ">surprise<") {
		t.Error("a code from the query string was printed on the page")
	}
}

// The area's own strip leads with Settings, and the rail's Admin row lands
// there: it is the screen that answers what this installation is.
func TestTheAdminAreaOpensOnSettings(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/today", session).Body.String()
	if got := hrefFor(t, body, "Admin area"); got != "/admin/settings" {
		t.Errorf("the rail's Admin row goes to %q, want /admin/settings", got)
	}
}

// The outcome is on the page as a note, with no script involved. app.js
// raises it into the centred panel the design draws, and carries the word for
// that panel's button in the markup so the script holds no copy of its own.
func TestAnOutcomeIsRenderedBeforeAnyScriptRunsIt(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/settings?notice=rotated", session).Body.String()
	if !strings.Contains(body, `class="note" role="status"`) {
		t.Error("the outcome is not rendered as a note")
	}
	if !strings.Contains(body, `data-raise="Close"`) {
		t.Error("the outcome carries no hook, or no word for the panel's button")
	}
	// An error is not raised: it belongs beside the thing that has to be
	// fixed, which is where it can be acted on.
	bad := fetchAs(t, srv, "/admin/settings?error=failed", session).Body.String()
	if !strings.Contains(bad, `role="alert"`) {
		t.Fatal("the error is not rendered")
	}
	if strings.Contains(bad, "data-raise") {
		t.Error("an error was marked to be raised into a panel")
	}
}

// cellAround returns the <dd> that contains needle, so an assertion about one
// row of the environment table cannot pass on another row's markup.
func cellAround(t *testing.T, body, needle string) string {
	t.Helper()
	at := strings.Index(body, needle)
	if at < 0 {
		t.Fatalf("no %q on the page", needle)
	}
	open := strings.LastIndex(body[:at], "<dd>")
	close := strings.Index(body[at:], "</dd>")
	if open < 0 || close < 0 {
		t.Fatalf("%q is not inside a table cell", needle)
	}
	return body[open : at+close]
}
