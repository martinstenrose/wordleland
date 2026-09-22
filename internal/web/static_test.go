package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// getStatic fetches one static file with whatever request headers the case
// needs — the conditional request is the point of the first test below.
func getStatic(t *testing.T, srv *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// Every static file is cached by its content: an ETag that is the file's own
// digest, a 304 for a browser that already has it, and a year — immutable —
// for a request that names the digest it wants, which is how every page
// links them. An embedded file has no modification time, so without any of
// this the file server sent every file in full on every page load, and the
// comment on serveStatic promised a cache it did not have.
func TestStaticAssetsAreCachedByContent(t *testing.T) {
	srv := testServer(t)

	plain := getStatic(t, srv, "/static/app.css", nil)
	if plain.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css = %d", plain.Code)
	}
	etag := plain.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) < 4 {
		t.Fatalf("app.css carries ETag %q, want a quoted digest", etag)
	}
	// Asked for without a version, it is good for an hour: long enough that
	// a page's reload costs nothing, short enough that a page cached with an
	// older ?v= cannot keep an older file alive for good.
	if got := plain.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("an unversioned request is cached as %q", got)
	}

	// The digest the page links it by is the ETag without its quotes.
	digest := strings.Trim(etag, `"`)
	versioned := getStatic(t, srv, "/static/app.css?v="+digest, nil)
	if got := versioned.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("a request naming the current digest is cached as %q, want immutable", got)
	}
	stale := getStatic(t, srv, "/static/app.css?v=000000000000", nil)
	if got := stale.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("a request naming an old digest is cached as %q; it must not be immutable", got)
	}

	// A browser that has it asks with the tag and gets nothing back but yes.
	again := getStatic(t, srv, "/static/app.css", map[string]string{"If-None-Match": etag})
	if again.Code != http.StatusNotModified {
		t.Errorf("a conditional request with the current ETag = %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("the 304 carried %d bytes of body", again.Body.Len())
	}

	// The font is named by a literal URL in app.css, which cannot carry a
	// digest, so it is immutable on its path: the convention is to rename a
	// font file rather than change one in place.
	font := getStatic(t, srv, "/static/fonts/manrope-variable.ttf", nil)
	if font.Code != http.StatusOK {
		t.Fatalf("GET the font = %d", font.Code)
	}
	if got := font.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("the font is cached as %q, want immutable", got)
	}

	// Two different files, two different digests — the map is per file, so
	// a change to the script does not throw away the cached font.
	js := getStatic(t, srv, "/static/app.js", nil)
	if js.Header().Get("ETag") == etag {
		t.Error("app.js and app.css carry the same ETag")
	}
}

// Every static file a page links is linked by its digest, and the digest is
// the one the server would answer as immutable — a link with the wrong one,
// or none, would be served but not kept.
func TestThePageLinksStaticFilesByTheirDigest(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()

	links := regexp.MustCompile(`(?:href|src)="(/static/[^"?]+)(\?v=([0-9a-f]*))?"`).FindAllStringSubmatch(body, -1)
	if len(links) < 4 {
		t.Fatalf("found only %d static links in the page; expected the stylesheet, the scripts and the icons", len(links))
	}
	for _, m := range links {
		path, version := m[1], m[3]
		if m[2] == "" {
			t.Errorf("%s is linked without a version", path)
			continue
		}
		etag := getStatic(t, srv, path, nil).Header().Get("ETag")
		if want := strings.Trim(etag, `"`); version != want {
			t.Errorf("%s is linked as v=%s but is served with ETag %s", path, version, etag)
		}
	}
}
