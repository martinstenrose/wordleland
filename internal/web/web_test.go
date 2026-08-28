package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// testServer builds a Server backed by a migrated temporary database.
func testServer(t *testing.T) *Server {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := store.Migrate(context.Background(), db, store.Migrations()); err != nil {
		t.Fatalf("store.Migrate() failed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(&config.Config{
		DBPath: "test",
		// A valid key is required to build the server at all, which is the
		// fail-fast: a bad TOTP_KEY must not wait until someone enrols.
		TOTPKey: bytes.Repeat([]byte{0x2a}, 32),
	}, db, logger)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return srv
}

func TestHealthz(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestHealthzReportsDatabaseFailure(t *testing.T) {
	srv := testServer(t)

	// A closed pool is the closest stand-in for the database going away
	// underneath a running server.
	srv.db.Close()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// "GET /" is a catch-all in Go 1.22 pattern syntax, matching every path no
// other pattern claims. Without an explicit guard, an arbitrary path would
// render the root page under a 200.
func TestRootIsNotACatchAll(t *testing.T) {
	srv := testServer(t)

	tests := []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},
		{"/anything", http.StatusNotFound},
		{"/deeply/nested/path", http.StatusNotFound},
		{"/login", http.StatusNotFound}, // not built yet; must not fall through to root
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Errorf("GET %s status = %d, want %d", tt.path, rec.Code, tt.want)
			}
		})
	}
}

// : the share link is a capability in the URL path, so it must not be
// handed to external sites in a Referer header.
func TestSecurityHeaders(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	want := map[string]string{
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
}

func TestSecurityHeadersOnErrorResponses(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q on a 404, want no-referrer", got)
	}
}

func TestPanicRecovery(t *testing.T) {
	srv := testServer(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("deliberate panic")
	})
	handler := recoverPanic(srv.logger, requestLogger(srv.logger, srv.cfg.TrustedProxies, securityHeaders(mux)))

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()

	// The point of the middleware: a panicking handler must not take the
	// process down or drop the connection.
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestRootServesLoginForm(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="email"`) || !strings.Contains(body, `name="password"`) {
		t.Errorf("the root does not serve a login form:\n%s", body)
	}
	if !strings.Contains(body, `name="csrf_token"`) {
		t.Error("the login form has no CSRF field")
	}
}

func TestRenderErrorDoesNotLeakDetail(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "does-not-exist") {
		t.Error("the error page echoes the requested path back to the visitor")
	}
	if !strings.Contains(body, "Not Found") {
		t.Errorf("error page is missing its title; got:\n%s", body)
	}
}

func TestParseTemplates(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates() failed: %v", err)
	}

	// base.html is the layout every page composes with, not a page itself.
	if _, ok := tmpl["base.html"]; ok {
		t.Error("base.html was registered as a page")
	}
	for _, name := range []string{"error.html", "login.html", "board.html"} {
		if _, ok := tmpl[name]; !ok {
			t.Errorf("template %s was not parsed", name)
		}
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.render(rec, req, http.StatusOK, "no-such-template.html", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func timeInPast() time.Time { return time.Now().Add(-time.Hour) }

func mustDate(t *testing.T, puzzle int) time.Time {
	t.Helper()
	d, err := wordle.DateForPuzzle(puzzle)
	if err != nil {
		t.Fatalf("DateForPuzzle(%d) failed: %v", puzzle, err)
	}
	return d
}

// A key with no entry renders as the key itself, which is deliberate but
// ugly. Catching it here means a template referencing a string nobody wrote
// fails the build rather than shipping "player.hardMode" to the page.
func TestEveryTemplateKeyHasAString(t *testing.T) {
	srv := testServer(t)
	catalogue := srv.catalogues["en"]

	pattern := regexp.MustCompile(`\.TN?\s+"([a-zA-Z0-9._]+)"`)
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("read templates: %v", err)
	}

	for _, entry := range entries {
		body, err := templateFS.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			key := match[1]
			_, plain := catalogue[key]
			// TN looks up key+".one" and key+".other".
			_, one := catalogue[key+".one"]
			_, other := catalogue[key+".other"]
			if !plain && !(one && other) {
				t.Errorf("%s uses %q, which is not in the English catalogue", entry.Name(), key)
			}
		}
	}
}

// The request log carries the client address, resolved the same way the
// rate limiter resolves it.
func TestRequestLogCarriesTheClientAddress(t *testing.T) {
	srv := testServer(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := requestLogger(logger, srv.cfg.TrustedProxies, srv.routes())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:51234"
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), "client=203.0.113.7") {
		t.Errorf("the request log has no client address:\n%s", buf.String())
	}
	// An untrusted peer's forwarding header is not believed.
	buf.Reset()
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:51234"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), "198.51.100.1") {
		t.Errorf("a forwarding header from an untrusted peer was logged:\n%s", buf.String())
	}
}

// Nothing here is meant to be found by search. robots.txt turns away the
// crawlers that read it; the header covers the ones that do not.
func TestCrawlersAreTurnedAway(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	robots := fetchAs(t, srv, "/robots.txt", nil)
	if robots.Code != http.StatusOK {
		t.Fatalf("robots.txt = %d", robots.Code)
	}
	if !strings.Contains(robots.Body.String(), "Disallow: /") {
		t.Errorf("robots.txt does not disallow: %q", robots.Body.String())
	}
	if ct := robots.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("robots.txt Content-Type = %q", ct)
	}

	// The header goes on every response, the share link most of all: it is a
	// capability URL, and anybody pasting it in public would otherwise put
	// the group's names into an index.
	for _, path := range []string{"/", "/share/" + slug + "/", "/share/" + slug + "/board", "/robots.txt"} {
		got := fetchAs(t, srv, path, nil).Header().Get("X-Robots-Tag")
		if !strings.Contains(got, "noindex") {
			t.Errorf("%s: X-Robots-Tag = %q, want noindex", path, got)
		}
	}
}

// The stylesheet is the whole design. It was once truncated to a fifth of
// its size by a bad edit and still served a clean 200, so nothing failed
// until a person looked at an unstyled page.
func TestStylesheetIsWhole(t *testing.T) {
	srv := testServer(t)

	rec := fetchAs(t, srv, "/static/app.css", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("app.css = %d", rec.Code)
	}
	css := rec.Body.String()

	// One selector per major surface. A file that lost a section would still
	// parse and still serve; only a check for the rules themselves catches
	// it.
	for _, selector := range []string{
		".topbar", ".card", ".board", ".panels", ".panel-head",
		".month-chip", ".grid", ".signin", ".auth-card", ".menu-panel",
		".views-mobile", ".trait", ".season", ".calendar", ".dist", ".strip",
		".admin-tabs", ".activity", ".pending-row",
		".recovery-codes",
		".account-menu", ".picker", ".spans", ".callout",
	} {
		if !strings.Contains(css, selector+" ") && !strings.Contains(css, selector+" {") &&
			!strings.Contains(css, selector+",") && !strings.Contains(css, selector+".") {
			t.Errorf("the stylesheet has no rules for %s", selector)
		}
	}

	if open, closed := strings.Count(css, "{"), strings.Count(css, "}"); open != closed {
		t.Errorf("braces do not balance: %d open, %d closed", open, closed)
	}

	// Both light blocks carry the same ground, so a system-preference reader
	// and one who chose light see the same page.
	if strings.Count(css, "--pg: #cfd3e5") != 2 && strings.Count(css, "--pg:#cfd3e5") != 2 {
		t.Error("the light ground is not defined in both light blocks")
	}
}

// Every table must have as many header cells as its rows have cells, and
// any column hidden on a narrow screen must be hidden in both — the header
// carrying the class as well as the cells. The months table shipped with a
// classless header on its bar column, so on a phone the header kept eight
// columns while the rows had seven and everything after Player sat one
// place to the left.
func TestTableHeadersAlignWithTheirRows(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	cell := regexp.MustCompile(`<t[dh]\b[^>]*>`)
	row := regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	table := regexp.MustCompile(`(?s)<table[^>]*>(.*?)</table>`)
	classes := regexp.MustCompile(`class="([^"]*)"`)

	// Columns a media query hides. Both the header and the cells have to
	// carry the class, or they disappear separately.
	hidden := []string{"spark-col", "bar-cell", "mark-col"}

	for _, path := range []string{
		"/share/" + slug + "/board",
		"/share/" + slug + "/months",
		"/share/" + slug + "/grid",
	} {
		body := fetchAs(t, srv, path, nil).Body.String()

		for _, tbl := range table.FindAllStringSubmatch(body, -1) {
			rows := row.FindAllStringSubmatch(tbl[1], -1)
			if len(rows) < 2 {
				continue
			}

			want := len(cell.FindAllString(rows[0][1], -1))
			headClasses := strings.Join(classes.FindAllString(rows[0][1], -1), " ")

			for _, r := range rows[1:] {
				if strings.Contains(r[1], "colspan") {
					continue
				}
				if got := len(cell.FindAllString(r[1], -1)); got != want {
					t.Errorf("%s: a row has %d cells, the header has %d", path, got, want)
					break
				}
				rowClasses := strings.Join(classes.FindAllString(r[1], -1), " ")
				for _, h := range hidden {
					if strings.Contains(rowClasses, h) != strings.Contains(headClasses, h) {
						t.Errorf("%s: %q is on the %s but not the other, so a narrow screen hides them separately",
							path, h, map[bool]string{true: "cells", false: "header"}[strings.Contains(rowClasses, h)])
					}
				}
				break
			}
		}
	}
}

// The mark doubles as the app icon, so a tab shows the same thing the home
// link does.
func TestFaviconIsServed(t *testing.T) {
	srv := testServer(t)

	for _, tc := range []struct{ path, contentType string }{
		{"/static/icon.svg", "image/svg+xml"},
		{"/static/icon-180.png", "image/png"},
		// Browsers probe this whether or not the page declares an icon.
		{"/favicon.ico", "image/png"},
	} {
		rec := fetchAs(t, srv, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", tc.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
			t.Errorf("%s Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", tc.path)
		}
	}

	// And every page points at them.
	page := fetchAs(t, srv, "/", nil).Body.String()
	for _, want := range []string{`rel="icon" type="image/svg+xml"`, `rel="apple-touch-icon"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not declare %s", want)
		}
	}
}
