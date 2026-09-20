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

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	const htmxTag = `<script src="/static/htmx.min.js" defer></script>`
	const appTag = `<script src="/static/app.js" defer></script>`
	if got := strings.Count(body, htmxTag); got != 1 {
		t.Errorf("the page loads htmx %d times, want once", got)
	}
	if strings.Index(body, htmxTag) > strings.Index(body, appTag) {
		t.Error("app.js is loaded before htmx, so its listeners have nothing to hear")
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
}
