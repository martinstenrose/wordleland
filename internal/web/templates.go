package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
)

// templateFS holds the server-rendered pages. html/template's contextual
// auto-escaping is the primary XSS defence, so all output goes
// through it rather than through string concatenation.
//
//go:embed templates static
var templateFS embed.FS

// serveStatic serves the embedded stylesheet and script.
//
// Both are embedded, with a long cache lifetime keyed by build: there is no
// asset pipeline and wants none.
func (s *Server) serveStatic() http.Handler {
	sub, err := fs.Sub(templateFS, "static")
	if err != nil {
		// Only reachable if the embed directive and this path disagree,
		// which is a build-time mistake rather than a runtime one.
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// templates maps a page name to its parsed template. Parsing happens once at
// startup: a malformed template becomes a boot failure rather than a 500 for
// whoever visits that page first.
type templates map[string]*template.Template

// templateFuncs are available to every page and partial.
var templateFuncs = template.FuncMap{"dict": dict}

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
