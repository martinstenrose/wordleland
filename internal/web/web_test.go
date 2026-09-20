package web

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
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
		"Content-Security-Policy": contentSecurityPolicy,
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
}

func TestContentSecurityPolicyAllowsOnlyUsedSources(t *testing.T) {
	for _, directive := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'self'",
	} {
		if !strings.Contains(contentSecurityPolicy, directive) {
			t.Errorf("policy %q does not contain %q", contentSecurityPolicy, directive)
		}
	}
	if strings.Contains(contentSecurityPolicy, "script-src 'self' 'unsafe-inline'") ||
		strings.Contains(contentSecurityPolicy, "script-src *") {
		t.Errorf("policy permits untrusted scripts: %q", contentSecurityPolicy)
	}
	// connect-src widened from 'none' for the search overlay's fetch calls
	// (see the constant's comment), but only as far as this origin — it
	// must never grow to allow an off-origin fetch target.
	if strings.Contains(contentSecurityPolicy, "connect-src *") ||
		strings.Contains(contentSecurityPolicy, "connect-src 'self' http") {
		t.Errorf("policy permits off-origin connections: %q", contentSecurityPolicy)
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

// dict is what every struct-shaped ui/ partial (chip, badge, stat-list,
// pill-nav) relies on to build its data inline from a page template's
// pipeline; without it those partials are unreachable from any page.
func TestDictBuildsPartialData(t *testing.T) {
	got, err := dict("Label", "x", "Dashed", true)
	if err != nil {
		t.Fatalf("dict() failed: %v", err)
	}
	if got["Label"] != "x" || got["Dashed"] != true {
		t.Errorf("dict() = %#v, want Label=x Dashed=true", got)
	}

	if _, err := dict("Label"); err == nil {
		t.Error("dict() with an odd argument count should fail, not silently drop the trailing key")
	}
	if _, err := dict(1, "x"); err == nil {
		t.Error("dict() with a non-string key should fail, not silently stringify it")
	}
}

// A page passes dict's output straight into {{template "chip" ...}}; this
// confirms that round trip actually renders, not just that dict itself
// builds a map.
func TestChipPartialRendersDictData(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates() failed: %v", err)
	}
	data, err := dict("Label", "Signal only", "Dashed", true)
	if err != nil {
		t.Fatalf("dict() failed: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl["today.html"].ExecuteTemplate(&buf, "chip", data); err != nil {
		t.Fatalf("execute chip: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, `class="chip dashed"`) || !strings.Contains(body, "Signal only") {
		t.Errorf("chip did not render dashed label; got:\n%s", body)
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

	err := fs.WalkDir(templateFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := templateFS.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			key := match[1]
			_, plain := catalogue[key]
			// TN looks up key+".one" and key+".other".
			_, one := catalogue[key+".one"]
			_, other := catalogue[key+".other"]
			if !plain && !(one && other) {
				t.Errorf("%s uses %q, which is not in the English catalogue", p, key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
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
		".shell", ".sidebar", ".drawer", ".nav-row",
		".trait", ".season", ".calendar", ".dist", ".strip",
		".pill-nav", ".activity", ".pending-row",
		".recovery-codes",
		".account-menu", ".callout", ".result-row", ".form-row",
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
	if strings.Count(css, "--color-canvas: oklch(.97 .006 65)") != 2 && strings.Count(css, "--color-canvas:oklch(.97 .006 65)") != 2 {
		t.Error("the light ground is not defined in both light blocks")
	}

	// Once, six alpha steps were defined only in the light blocks and the
	// rules that used them rendered colourless in dark mode (see
	// static/README.md). The trend pair belongs to all three blocks for the
	// same reason: a form delta with no colour reads as no delta.
	for _, token := range []string{"--color-better", "--color-worse"} {
		if got := strings.Count(css, token+":"); got != 3 {
			t.Errorf("%s is defined %d times, want once per theme block", token, got)
		}
	}
}

// A guess count is the one number on the board a reader takes in without
// reading it, so every outcome gets its own fill. Four of the seven used to,
// and a 5, a 6 and a miss all fell through to the grey text ramp — which made
// the three outcomes worth telling apart at a glance the three that looked
// alike. Each tier naming its own token is what stops that closing up again.
func TestEveryScoreTierHasItsOwnFill(t *testing.T) {
	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	fills := map[string]string{}
	for tier, token := range map[string]string{
		"t1": "--score-1", "t2": "--score-2", "t3": "--score-3", "t4": "--score-4",
		"t5": "--score-5", "t6": "--score-6", "t7": "--score-x",
	} {
		for _, selector := range []string{".cell." + tier, ".cal." + tier} {
			rule, ok := ruleFor(css, selector)
			if !ok {
				t.Errorf("the stylesheet has no rule for %s", selector)
				continue
			}
			if !strings.Contains(rule, "background: var("+token+")") {
				t.Errorf("%s is not filled with %s: %s", selector, token, rule)
			}
		}
		fills[token] = tier
	}
	if len(fills) != 7 {
		t.Errorf("tiers share a fill token: %v", fills)
	}
}

// ruleFor returns the declarations of the first rule for exactly this
// selector, so a test can assert on one rule rather than on the whole file.
func ruleFor(css, selector string) (string, bool) {
	for _, at := range []string{selector + " {", selector + "{"} {
		if i := strings.Index(css, at); i >= 0 {
			rest := css[i+len(at):]
			if end := strings.Index(rest, "}"); end >= 0 {
				return strings.TrimSpace(rest[:end]), true
			}
		}
	}
	return "", false
}

// The stylesheet asks for a typeface this app serves itself. A font that 404s
// is invisible in every test that only reads markup: the page still renders,
// in the fallback, and nobody notices until they look at one. The embed is
// the thing that breaks — a file added to static/ but not reachable through
// it — so this asks the server for the bytes the @font-face names.
func TestTheTypefaceIsServed(t *testing.T) {
	srv := testServer(t)

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	const src = "/static/fonts/manrope-variable.ttf"
	if !strings.Contains(css, src) {
		t.Fatalf("the stylesheet does not reference %s", src)
	}

	rec := fetchAs(t, srv, src, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d", src, rec.Code)
	}
	// An sfnt file starts with a version tag; 0x00010000 is TrueType outlines,
	// which is what format("truetype-variations") promises the browser.
	if got := rec.Body.Bytes(); len(got) < 4 || !bytes.Equal(got[:4], []byte{0x00, 0x01, 0x00, 0x00}) {
		t.Errorf("%s is not a TrueType file (%d bytes, starts %x)", src, len(got), got[:min(4, len(got))])
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
	hidden := []string{"spark-col", "window-col", "bar-cell", "mark-col"}

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

// Switching pages in place is htmx's: the body is boosted, so every link
// and form fetches its page and swaps the body. Two things are easy to
// break by moving markup around, so both are pinned here.
//
// The first is the opt-outs. htmx's listener sits on the element itself and
// fires before anything app.js delegates from the document, so a control
// app.js takes over — the search button, the enrolment link — has to say
// hx-boost="false" or htmx fetches the page first and the script's press
// lands on a body that is being replaced. The dialog that link opens needs
// no opt-out: app.js inserts it and never hands it to htmx.
//
// The second is the registry. Enhancements that hold on to an element rather
// than delegating from the document have to be run again once the body has
// been replaced, or search, the raised outcome and the copy button stop
// working after the first switch.
func TestEveryLinkIsBoostedExceptTheOnesScriptTakes(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/settings/security", session).Body.String()
	if !strings.Contains(body, `<body hx-boost="true"`) {
		t.Fatal("the body is not boosted, so every link reloads the page")
	}
	for _, control := range []string{`class="menu-btn search-btn"`, `data-modal`} {
		at := strings.Index(body, control)
		if at < 0 {
			t.Fatalf("no %s on the security screen", control)
		}
		tag := body[at:]
		tag = tag[:strings.Index(tag, ">")]
		if !strings.Contains(tag, `hx-boost="false"`) {
			t.Errorf("%s is boosted, so htmx takes the press before the script does", control)
		}
	}
	if strings.Contains(appJS(t, srv), "htmx.process(card)") {
		t.Error("the enrolment dialog's card is handed to htmx, so its Cancel would navigate away")
	}

	script := appJS(t, srv)
	if !strings.Contains(script, "onPageChange.rerun()") || !strings.Contains(script, "htmx:afterSettle") {
		t.Error("nothing runs the re-init registry after htmx has swapped the body")
	}
	for _, enhancement := range []string{"search-overlay", "mountCopy", "[data-raise]"} {
		if !strings.Contains(script, enhancement) {
			t.Errorf("%s is gone from app.js", enhancement)
		}
	}
}

// appJS is app.js as served.
func appJS(t *testing.T, srv *Server) string {
	t.Helper()
	return fetchAs(t, srv, "/static/app.js", nil).Body.String()
}

// Every frame has one, because it is both what the skip link points at and
// where app.js puts focus once htmx has swapped a page in.
func TestEveryFrameCarriesTheMainRegion(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{
		"/share/" + slug + "/", // the application frame
		"/",                    // the sign-in frame
		"/share/" + slug + "/players/no-such-player", // no frame at all
	} {
		body := fetchAs(t, srv, path, nil).Body.String()
		if got := strings.Count(body, `<main id="main">`); got != 1 {
			t.Errorf("%s renders %d main regions, want exactly one", path, got)
		}
	}
}

// Every control on every screen is one of four things, and the four are told
// apart by two classes.
//
// They were not. A filled green anchor and a filled green button were 32px
// and 36px and stood side by side; "Discard" wore .link.danger and came out
// accent green, because .link.danger is only red inside the account menu;
// "Cancel" was an underlined green link in one place and an outlined button
// in another; and the control that rotates a two-factor secret — which
// silences every app holding the old one — was filled green while the one
// that rotates the share slug was outlined red.
//
// This pins the vocabulary rather than any one screen: a <button> or an
// action anchor carries .btn, and .link never lands on a button.
func TestEveryControlIsOneOfTheFour(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	// A <button> that is a control never wears the prose-link treatment.
	// (The account menu's rows are navigation and are drawn by being in the
	// menu, whatever element they are — see .account-menu in app.css.)
	button := regexp.MustCompile(`<button[^>]*class="([^"]*)"`)
	for _, path := range []string{
		"/", "/forgot-password", "/settings", "/settings/account", "/settings/security",
		"/admin/settings", "/admin/settings?confirm=slug", "/admin/pending", "/admin/players/harda",
	} {
		body := fetchAs(t, srv, path, session).Body.String()
		for _, m := range button.FindAllStringSubmatch(body, -1) {
			classes := strings.Fields(m[1])
			if slices.Contains(classes, "link") {
				t.Errorf("%s: a button is styled as prose: %q", path, m[1])
			}
			// Everything but the account menu's own row and the icon-only
			// controls the script builds is a .btn.
			if slices.Contains(classes, "btn") || slices.Contains(classes, "danger") {
				continue
			}
			t.Errorf("%s: a button carries no control class: %q", path, m[1])
		}
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	// One box for anchors and buttons alike, which is what stops two of them
	// standing side by side at different heights.
	box := cssRule(t, css, ".btn {")
	for _, want := range []string{"height: 36px", "font-size: var(--text-sm)", "border-radius: var(--radius-md)"} {
		if !strings.Contains(box, want) {
			t.Errorf("the control box is missing %q", want)
		}
	}

	// Four combinations, and each has to actually differ from the others.
	tones := map[string]string{
		".btn {":                  "var(--color-accent)",
		".btn.secondary {":        "var(--color-text-16)",
		".btn.danger {":           "var(--color-danger)",
		".btn.secondary.danger {": "var(--color-danger-40)",
	}
	for selector, want := range tones {
		if rule := cssRule(t, css, selector); !strings.Contains(rule, want) {
			t.Errorf("%s does not take %s: %s", selector, want, rule)
		}
	}

	// And the one that used to be a lie: .link is a prose treatment, so
	// nothing pairs it with the danger tone outside the account menu.
	if strings.Contains(css, ".link.danger") {
		t.Error("app.css still gives .link a danger tone; a prose link is not a control")
	}
}

// The three acts that cannot be undone read the same as each other: an
// outlined red control opens the question, and a filled red one commits it.
//
// Rotating a two-factor secret was the odd one out — filled green, the tone
// this app uses for "safe, and the ordinary thing to do here", on a control
// that silences every authenticator app holding the old secret and cancels
// the recovery codes.
func TestADestructiveActAsksBeforeItActs(t *testing.T) {
	srv := testServer(t)
	// seedLogin rather than seedBoard: this needs an account that can
	// actually sign in, and a share slug for the rotation to be offered.
	seedLogin(t, srv, "admin@example.tld", true)
	if _, _, err := store.EnsureShareSlug(context.Background(), srv.db); err != nil {
		t.Fatal(err)
	}
	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)

	// The openers.
	for _, tt := range []struct{ page, opens string }{
		{"/admin/settings", "/admin/settings?confirm=slug"},
		{"/settings/security", "/enroll-totp"},
	} {
		rec, _ := getWith(t, srv, tt.page, cookies)
		body := rec.Body.String()
		at := strings.Index(body, `href="`+tt.opens)
		if at < 0 {
			t.Errorf("%s does not offer %s at all", tt.page, tt.opens)
			continue
		}
		opener := body[strings.LastIndex(body[:at], "<a "):at]
		if !strings.Contains(opener, "btn secondary danger") {
			t.Errorf("%s: the control opening %s is not an outlined danger: %s", tt.page, tt.opens, opener)
		}
	}

	// And the presses that commit them, each inside the question it answers,
	// with a Cancel beside it rather than a prose link at the foot: declining
	// a form is a control too.
	for _, tt := range []struct{ page, want string }{
		{"/admin/settings?confirm=slug", `class="btn danger"`},
		{"/enroll-totp", `class="btn danger"`},
	} {
		rec, _ := getWith(t, srv, tt.page, cookies)
		if !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s is not committed by a filled danger control (%s)", tt.page, tt.want)
		}
	}

	rec, _ := getWith(t, srv, "/enroll-totp", cookies)
	if !strings.Contains(rec.Body.String(), `<a class="btn secondary" href="/settings/security">`) {
		t.Error("the replacement has no Cancel beside the control it declines")
	}

	// A first enrolment is not destructive and is not toned as though it
	// were: the same control, before there is anything to lose. It has
	// nothing to decline either, so its one button fills the card.
	seedLogin(t, srv, "player@example.tld", false)
	_, plain := login(t, srv, "player@example.tld", testPassword)
	rec, _ = getWith(t, srv, "/settings/security", plain)
	body := rec.Body.String()
	at := strings.Index(body, `href="/enroll-totp"`)
	if at < 0 {
		t.Fatal("an account without two-factor is not offered it")
	}
	if opener := body[strings.LastIndex(body[:at], "<a "):at]; strings.Contains(opener, "danger") {
		t.Errorf("setting a first secret up is toned as destructive: %s", opener)
	}
}

// Switching a page in leaves the reader at the top of it, with the bar still
// on screen.
//
// After a swap, app.js moves focus to the main region so that a reader who
// cannot see the page is told it changed. Focusing an element scrolls it into
// view, and the bar above this one is sticky — so every switched-in page
// arrived 56px down, with the bar scrolled away, on a page nobody had
// touched. It was invisible on the two views short enough not to scroll,
// which is what made it look like a bug in the other five.
//
// Focus without scroll is the whole fix, and this pins it because nothing
// that runs in CI can see a scroll position.
func TestSwitchingAPageLeavesItAtTheTop(t *testing.T) {
	srv := testServer(t)
	js := fetchAs(t, srv, "/static/app.js", nil).Body.String()

	// Scoped to the block that runs after a swap: everywhere else a focus is
	// meant to bring its target into view — a dialog's close button, the
	// next control a trapped Tab reaches, the button a closing overlay hands
	// focus back to.
	at := strings.Index(js, "// What htmx leaves to this file.")
	if at < 0 {
		t.Fatal("the after-swap block is gone")
	}
	switcher := js[at:]

	if !strings.Contains(switcher, "main.focus({ preventScroll: true })") {
		t.Error("the main region is focused with a scroll, which drags the sticky bar off screen")
	}
	for _, line := range strings.Split(switcher, "\n") {
		if strings.Contains(line, ".focus()") {
			t.Errorf("a focus that scrolls the page: %s", strings.TrimSpace(line))
		}
	}
	// Four: the region, and the three controls the switcher aims at instead
	// — a ranking row, a section bar, a rail row.
	if n := strings.Count(switcher, "preventScroll: true"); n != 4 {
		t.Errorf("%d of the switcher's focus calls prevent scrolling, want 4", n)
	}
}

// The page's title sits in the same place, at the same size, on every screen.
//
// It did not. A card whose title is a menu carried a glyph in front of it — a
// section icon, a player's initials — which pushed the heading 45px past
// where a card-head puts one, so moving between the Leaderboard and Players
// moved the title. Today had the day's headline where every other page has
// its name, at a different size again, so the title moved on the way in and
// out of Today as well.
//
// Geometry is not something CI can see, so this pins the three rules it comes
// out of: one heading treatment, one box around it, and a name in it.
func TestThePageTitleDoesNotMoveBetweenPages(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	// One heading treatment, whatever the title happens to be. Today is not
	// in this list: it is the front page and leads with the day's result
	// rather than with its own name, which is a heading of a different kind
	// and is deliberately set larger.
	head := cssRule(t, css, ".card-head h1,")
	for _, rule := range []string{".switcher-label {"} {
		got := cssRule(t, css, rule)
		for _, want := range []string{"font-size: var(--text-xl)", "font-weight: 700"} {
			if !strings.Contains(got, want) || !strings.Contains(head, want) {
				t.Errorf("%s does not carry %s the way .card-head h1 does: %s", rule, want, got)
			}
		}
		// Line height included, and by leaving it alone rather than by
		// matching a number. A shorter one moves the glyphs up inside a box
		// whose top still lines up, and takes the subtitle under them with
		// it — 1.2 here put the line under a title that is a menu 8px above
		// the line under a title that is not.
		if strings.Contains(got, "line-height") {
			t.Errorf("%s sets a line height .card-head h1 does not: %s", rule, got)
		}
	}
	// And one box around it. The bar and Today's head both take .card-head's
	// own vertical padding and the card's gutter.
	for _, rule := range []string{".switcher-bar {", ".today-head {"} {
		if got := cssRule(t, css, rule); !strings.Contains(got, "16px") {
			t.Errorf("%s does not take .card-head's vertical padding: %s", rule, got)
		}
	}
	// And one subtitle treatment under it, .kicker's, wherever there is one.
	if !strings.Contains(fetchAs(t, srv, "/grid", session).Body.String(), `<p class="kicker">`) {
		t.Error("the grid has no subtitle under its title")
	}
	// Nothing in front of the heading inside the bar: that was the 45px.
	if strings.Contains(css, ".switcher-avatar") || strings.Contains(css, ".switcher-mark") {
		t.Error("the bar still draws a glyph in front of its heading")
	}

	// Every page names itself in an <h1> — except Today, whose <h1> is the
	// day's result.
	for _, tt := range []struct{ path, want string }{
		{"/leaderboard", "The board"},
		{"/players/harda", "Harda"},
		{"/admin/pending", "Pending results"},
	} {
		body := fetchAs(t, srv, tt.path, session).Body.String()
		at := strings.Index(body, `<main id="main">`)
		if at < 0 {
			t.Fatalf("%s has no main region", tt.path)
		}
		main := body[at:]
		open := strings.Index(main, "<h1")
		if open < 0 {
			t.Errorf("%s has no title at all", tt.path)
			continue
		}
		title := main[open:]
		title = title[strings.Index(title, ">")+1 : strings.Index(title, "</h1>")]
		if strings.TrimSpace(title) != tt.want {
			t.Errorf("%s is titled %q, want %q", tt.path, strings.TrimSpace(title), tt.want)
		}
	}
}

// One press to the section or the player next door, and both ends wrap so
// neither arrow is ever the disabled control a first or last item would need.
func TestTheSectionBarStepsToItsNeighbours(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	step := regexp.MustCompile(`<a class="switcher-step" href="([^"]+)" aria-label="([^"]+)"`)

	// Settings is the first of the five, so its "previous" wraps to the last.
	got := step.FindAllStringSubmatch(fetchAs(t, srv, "/admin/settings", session).Body.String(), -1)
	if len(got) != 2 {
		t.Fatalf("got %d step arrows on the first section, want 2", len(got))
	}
	if got[0][1] != "/admin/diagnostics" || !strings.Contains(got[0][2], "Diagnostics") {
		t.Errorf("the first section's previous is %q (%q), want the last one", got[0][1], got[0][2])
	}
	if got[1][1] != "/admin/players" || !strings.Contains(got[1][2], "Players") {
		t.Errorf("the first section's next is %q (%q)", got[1][1], got[1][2])
	}

	// The roster steps in the board's order, and the arrows name who is there.
	got = step.FindAllStringSubmatch(fetchAs(t, srv, "/players/harda", session).Body.String(), -1)
	if len(got) != 2 {
		t.Fatalf("got %d step arrows on a player, want 2", len(got))
	}
	for _, m := range got {
		if !strings.HasPrefix(m[1], "/players/") {
			t.Errorf("a roster arrow goes to %q, which is not a player", m[1])
		}
		if !strings.Contains(m[2], ":") {
			t.Errorf("a roster arrow is unlabelled: %q", m[2])
		}
	}

	// A roster of one has nowhere to step, so it offers no arrows.
	only := testServer(t)
	seedLogin(t, only, "admin@example.tld", true)
	lone, _ := store.CreatePlayer(context.Background(), only.db,
		store.SystemActor(), "Solo", "solo")
	seedResult(t, only, lone.ID, currentPuzzle(), 4, false)
	_, alone := login(t, only, "admin@example.tld", testPassword)
	_, alone = enrol(t, only, alone)
	page, _ := getWith(t, only, "/players/solo", alone)
	if strings.Contains(page.Body.String(), "switcher-step") {
		t.Error("a roster of one offers a step with nowhere to go")
	}
}

// Every custom property the stylesheet reads is one the stylesheet defines.
//
// An undefined one is invisible: the declaration is simply dropped, the
// property falls back to whatever it inherits, and the page renders — wrongly
// and without complaint. --text-2xl was asked for in exactly one place and
// had never been defined, so Today's headline spent several commits at the
// body's 14px while looking like a deliberate size.
//
// --pct is the one exception and is named rather than pattern-matched: the
// progress bar sets it inline, per instance, which is the whole reason it is
// a property and not a width.
func TestEveryTokenTheStylesheetReadsIsOneItDefines(t *testing.T) {
	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	defined := map[string]bool{"--pct": true}
	for _, m := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(css, -1) {
		defined[m[1]] = true
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`var\((--[a-z0-9-]+)`).FindAllStringSubmatch(css, -1) {
		if !defined[m[1]] && !seen[m[1]] {
			seen[m[1]] = true
			t.Errorf("%s is read but never defined; every rule using it is silently dropped", m[1])
		}
	}
}
