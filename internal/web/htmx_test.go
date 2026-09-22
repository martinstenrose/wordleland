package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// htmx is vendored and embedded like the stylesheet: one file under
// /static/, no build step, no request to anybody else's server. The page
// loads it before app.js, which listens for its events, and configures it
// through the meta tag htmx reads at start-up.
func TestHtmxIsServedAndLoadedOnce(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/static/htmx.min.js", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/htmx.min.js = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("htmx served as %q, want a JavaScript type", ct)
	}
	if !strings.Contains(rec.Body.String(), "htmx") {
		t.Error("the file served under /static/htmx.min.js is not htmx")
	}

	// The tags carry ?v= after the path (see serveStatic), so they are
	// matched up to the path and checked to be deferred separately.
	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	const htmxTag = `<script src="/static/htmx.min.js`
	const appTag = `<script src="/static/app.js`
	if got := strings.Count(body, htmxTag); got != 1 {
		t.Errorf("the page loads htmx %d times, want once", got)
	}
	at := strings.Index(body, htmxTag)
	if at > strings.Index(body, appTag) {
		t.Error("app.js is loaded before htmx, so its listeners have nothing to hear")
	}
	if tag := body[at:at+strings.Index(body[at:], ">")]; !strings.Contains(tag, " defer") {
		t.Errorf("htmx is not deferred: %s", tag)
	}
}

// The configuration is what keeps htmx inside the CSP and this repository's
// rules: nothing evaluated, nothing injected, nothing executed out of a
// swapped page. And a 4xx or 5xx is swapped like any other page, so a link
// to nowhere shows the error frame in place rather than doing nothing.
func TestHtmxConfigForbidsEvalAndSwapsErrorPages(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()

	const open = `<meta name="htmx-config" content='`
	at := strings.Index(body, open)
	if at < 0 {
		t.Fatal("the page carries no htmx-config meta tag")
	}
	raw := body[at+len(open):]
	raw = raw[:strings.Index(raw, "'")]

	var cfg struct {
		AllowEval              *bool `json:"allowEval"`
		IncludeIndicatorStyles *bool `json:"includeIndicatorStyles"`
		AllowScriptTags        *bool `json:"allowScriptTags"`
		GlobalViewTransitions  *bool `json:"globalViewTransitions"`
		ResponseHandling       []struct {
			Code string `json:"code"`
			Swap bool   `json:"swap"`
		} `json:"responseHandling"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("htmx-config is not JSON: %v\n%s", err, raw)
	}
	for name, flag := range map[string]*bool{
		"allowEval":              cfg.AllowEval,
		"includeIndicatorStyles": cfg.IncludeIndicatorStyles,
		"allowScriptTags":        cfg.AllowScriptTags,
	} {
		if flag == nil || *flag {
			t.Errorf("htmx-config does not turn %s off", name)
		}
	}
	errors := false
	for _, h := range cfg.ResponseHandling {
		if h.Code == "[45].." && h.Swap {
			errors = true
		}
	}
	if !errors {
		t.Error("htmx-config does not swap 4xx and 5xx responses, so an error page would arrive as nothing")
	}
	// And every swap is a view transition, which is what stops a switch
	// reading as a reload; the two swaps that are not a navigation opt out
	// where they are rendered, see below.
	if cfg.GlobalViewTransitions == nil || !*cfg.GlobalViewTransitions {
		t.Error("htmx-config does not turn globalViewTransitions on")
	}
}

// Two swaps are not a navigation and must not cross-fade the page: the live
// region redrawing itself when a result lands — the reader pressed nothing
// — and the search overlay answering a keystroke, where a transition on
// every debounced keypress would freeze the page as you type. Each says so
// on its own hx-swap, because the config makes the transition the default.
func TestALiveRegionAndTheSearchOverlayDoNotCrossFade(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	seedLogin(t, srv, "reader@example.tld", true)
	_, cookies := login(t, srv, "reader@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)

	rec, _ := getWith(t, srv, "/today", cookies)
	body := rec.Body.String()

	// The tag itself carries a ">" inside hx-select, so it is cut at an
	// attribute that follows hx-swap rather than at the closing bracket.
	main, ok := sectionOf(body, `<main id="main"`, `hx-push-url=`)
	if !ok {
		t.Fatal("the page has no main region")
	}
	if !strings.Contains(main, `sse-connect=`) {
		t.Fatal("Today's main region does not listen to the stream; this test needs a live page")
	}
	if !strings.Contains(main, `hx-swap="innerHTML transition:false"`) {
		t.Errorf("the live region's redraw is not opted out of the view transition:\n%s", main)
	}

	input, ok := sectionOf(body, `<input type="search"`, ">")
	if !ok {
		t.Fatal("the page has no search input")
	}
	if !strings.Contains(input, `hx-swap="innerHTML transition:false"`) {
		t.Errorf("the search overlay's results swap is not opted out of the view transition:\n%s", input)
	}
}
