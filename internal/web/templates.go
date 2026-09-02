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

// parseTemplates pairs every page with the shared base layout.
func parseTemplates() (templates, error) {
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}

	const base = "templates/base.html"
	parsed := make(templates)
	for _, page := range pages {
		if page == base {
			continue
		}
		t, err := template.ParseFS(templateFS, base, page)
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
	// r.URL.Path is logged below at every branch; see the go/log-injection
	// note on requestLogger in middleware.go for why that's safe.
	t, ok := s.templates[name]
	if !ok {
		s.logger.Error("unknown template", "template", name, "path", r.URL.Path) // codeql[go/log-injection]
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		s.logger.Error("render template", "template", name, "path", r.URL.Path, "error", err) // codeql[go/log-injection]
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		s.logger.Warn("write response", "path", r.URL.Path, "error", err) // codeql[go/log-injection]
	}
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
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int) {
	title := http.StatusText(status)
	if title == "" {
		title = "Error"
	}
	msg := "Something went wrong."
	switch status {
	case http.StatusNotFound:
		msg = "There is nothing at this address."
	case http.StatusForbidden:
		msg = "You do not have access to this page."
	}
	s.render(w, r, status, "error.html", errorPage{
		chrome:  s.newChrome(w, r, "", "", true),
		Title:   title,
		Message: msg,
	})
}
