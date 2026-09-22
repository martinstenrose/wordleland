package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
)

// templateFS holds the server-rendered pages. html/template's contextual
// auto-escaping is the primary XSS defence, so all output goes
// through it rather than through string concatenation.
//
//go:embed templates static
var templateFS embed.FS

// serveStatic serves the embedded stylesheet, scripts, icons and font.
//
// Everything under /static/ is embedded and cached by content. Each file
// carries an ETag that is its own digest, so a browser that already has it
// asks once and gets a 304 back — an embedded file has no modification
// time, so without this the file server would send every file in full on
// every page load. And a page links each file with ?v= set to that digest
// (see asset), so a request that names the digest it wants is answered as
// immutable: the URL changes when the file does, and the copy a browser
// holds can be kept for as long as it likes. Any other request for the same
// file gets an hour, so a page cached with an older ?v= cannot pin an older
// file forever. The font is immutable on its path alone: app.css names it by
// a literal URL it cannot version, and the convention there is to rename a
// font file rather than change one in place.
//
// Digests, not the build's version: version.Commit is empty for any build
// made outside CI, and a developer's build has to cache the same way. There
// is no asset pipeline and wants none.
func (s *Server) serveStatic() http.Handler {
	sub, err := fs.Sub(templateFS, "static")
	if err != nil {
		// Only reachable if the embed directive and this path disagree,
		// which is a build-time mistake rather than a runtime one.
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	digests := assetDigests()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		digest, known := digests[r.URL.Path]
		if known {
			// Set before the file server runs: ServeContent reads the
			// response's ETag for If-None-Match, so the 304 comes with it.
			w.Header().Set("ETag", `"`+digest+`"`)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/static/fonts/"),
			known && r.URL.Query().Get("v") == digest:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
}

// assetDigests maps each embedded file's URL path to the first twelve hex
// characters of its SHA-256, computed once on first use. Twelve is plenty to
// tell one build's file from another's and short enough to read in a URL.
var assetDigests = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	err := fs.WalkDir(templateFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := templateFS.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out["/"+p] = hex.EncodeToString(sum[:])[:12]
		return nil
	})
	if err != nil {
		// The embedded tree is fixed at build time; a walk that fails is a
		// build-time mistake, not a runtime one.
		panic(err)
	}
	return out
})

// asset is the template function that links a static file: its path with
// ?v= set to the file's digest, so the URL changes when the file does and
// serveStatic can answer it as immutable. A path this build does not embed
// comes back as it is, so a typo is a 404 on that file rather than a page
// that fails to render.
func asset(p string) string {
	if digest, ok := assetDigests()[p]; ok {
		return p + "?v=" + digest
	}
	return p
}

// templates maps a page name to its parsed template. Parsing happens once at
// startup: a malformed template becomes a boot failure rather than a 500 for
// whoever visits that page first.
type templates map[string]*template.Template

// templateFuncs are available to every page and partial.
var templateFuncs = template.FuncMap{"dict": dict, "asset": asset}

// dict builds a map from alternating key/value arguments, so a page can
// construct a partial's data inline — {{template "chip" (dict "Label" .
// "Dashed" true)}} — instead of every partial call site needing its own
// named Go type just to satisfy html/template's one-argument pipeline.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// parseTemplates pairs every page with the shared base layout and the
// partial library: every file under templates/ui/ (domain-agnostic) and
// templates/app/ (Wordleland-specific) — see
// internal/web/templates/README.md for the rule that sorts a partial into
// one or the other.
func parseTemplates() (templates, error) {
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	partials, err := fs.Glob(templateFS, "templates/ui/*.html")
	if err != nil {
		return nil, fmt.Errorf("list ui partials: %w", err)
	}
	appPartials, err := fs.Glob(templateFS, "templates/app/*.html")
	if err != nil {
		return nil, fmt.Errorf("list app partials: %w", err)
	}
	partials = append(partials, appPartials...)

	const base = "templates/base.html"
	parsed := make(templates)
	for _, page := range pages {
		if page == base {
			continue
		}
		files := append([]string{base, page}, partials...)
		t, err := template.New(path.Base(base)).Funcs(templateFuncs).ParseFS(templateFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", page, err)
		}
		parsed[path.Base(page)] = t
	}
	return parsed, nil
}

// render writes a page, buffering first so a template error mid-execution
// cannot emit a half-written page under a 200 status.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	s.renderBlock(w, r, status, name, "base", data)
}

// renderBlock executes one named block of a page's template rather than the
// whole "base" layout — the command palette's overlay is the one caller
// that wants this: it fetches the exact same page's results block, so the
// overlay is never a second copy of what a query matches, only a smaller
// render of it. See search.go's handleSearchPage.
func (s *Server) renderBlock(w http.ResponseWriter, r *http.Request, status int, name, block string, data any) {
	// r.URL.Path is logged below at every branch; see docs/decisions.md,
	// "CI and security scanning", for why that's safe.
	t, ok := s.templates[name]
	if !ok {
		s.logger.Error("unknown template", "template", name, "path", r.URL.Path)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		s.logger.Error("render template", "template", name, "block", block, "path", r.URL.Path, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		s.logger.Warn("write response", "path", r.URL.Path, "error", err)
	}
}

// wantsPartial reports whether this request asked for the page's content
// without the page around it — see app.js, where the ⌘K overlay and the
// enrolment dialog both put a card of somebody else's page inside their own.
//
// A stale "?partial=1" in a bookmark must not hand a reader a bare fragment,
// so only the handlers that have something to offer a fragment of look at
// this; everywhere else the parameter is inert. urlWith drops it, so it
// cannot survive into a link either.
func wantsPartial(r *http.Request) bool {
	return r.URL.Query().Get("partial") == "1"
}

// errorPage is the data for error.html.
type errorPage struct {
	chrome

	Title   string
	Message string
}

// renderError shows a plain error page. Messages are deliberately generic:
// nothing here should tell an unauthenticated visitor whether a given path,
// slug or account exists.
//
// Three messages, not one per status: a bad request and a server fault both
// say "something went wrong", because what a reader can do about either is
// the same — nothing — and a title naming the fault would only be read by
// somebody probing. The words come from the catalogue like every other
// sentence on the site; this page had been the one that spoke English to
// everybody, which four languages made a fault rather than a habit.
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int) {
	kind := "generic"
	switch status {
	case http.StatusNotFound:
		kind = "notFound"
	case http.StatusForbidden:
		kind = "forbidden"
	}
	ch := s.newChrome(w, r, "", "", true)
	title := ch.T.T("error." + kind + ".title")
	msg := ch.T.T("error." + kind + ".body")
	// No frame at all. An error page is chrome for a stranger — renderError
	// builds it with readOnly:true for any visitor, signed in or not — and
	// wrapping "there is nothing at this address" in the whole navigation
	// offers the application to somebody who has not got it. The same
	// reasoning the SearchPath field comment records, one control further
	// out.
	ch.Frame = frameBare
	s.render(w, r, status, "error.html", errorPage{
		chrome:  ch,
		Title:   title,
		Message: msg,
	})
}
