package web

import (
	"log/slog"
	"net"
	"net/http"

	"github.com/martinstenrose/wordleland/internal/auth"
	"time"
)

// statusRecorder captures the status code for request logging, since
// http.ResponseWriter does not expose what was written.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// securityHeaders sets headers that apply to every response.
//
// Referrer-Policy is required by: the share link is a capability in
// the URL path, so without this it would leak to any external site the
// board links to and show up in that site's logs.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// Belt and braces with robots.txt. A crawler that respects
		// robots.txt never fetches these pages; one that ignores it and
		// fetches anyway is told here not to index what it found. The share
		// link is the reason this matters: it is a capability URL, and
		// anybody who pastes it somewhere public would otherwise put the
		// group's names into a search index.
		h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs one line per request after it completes.
//
// It deliberately logs only the path, never the query string or headers: the
// share slug is a capability and reset tokens arrive as query parameters, so
// logging full URLs would write credentials to disk.
func requestLogger(logger *slog.Logger, trusted []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		// The client address goes through the same resolution the rate
		// limiter uses, so a proxied deployment logs the reader rather than
		// the proxy — and an unconfigured one logs the peer rather than a
		// header anybody can set.
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration", time.Since(start),
			"client", auth.ClientIP(r, trusted),
		)
	})
}

// recoverPanic turns a panicking handler into a 500 instead of dropping the
// connection and taking the process down with it.
func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				// http.ErrAbortHandler is the documented way to abandon a
				// response on purpose; re-panic so net/http handles it.
				if p == http.ErrAbortHandler {
					panic(p)
				}
				logger.Error("panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", p,
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
