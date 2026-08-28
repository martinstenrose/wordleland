// Package web serves the HTTP interface: the login flow, the read-only share
// board, and the ingest API.
package web

import (
	"reflect"

	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/config"
)

// Server holds everything the handlers need.
type Server struct {
	cfg        *config.Config
	db         *sql.DB
	logger     *slog.Logger
	templates  templates
	catalogues catalogues
	// localeCodes is every loaded locale, English first and the rest
	// alphabetical, so the language switcher has a stable order that does
	// not depend on map iteration.
	localeCodes []string
	limiter     *auth.Limiter
	cipher      *auth.Cipher
	mailer      *auth.Mailer

	// bridge is the Signal bridge, nil when none is configured. The
	// server reads its state for the liveness probe and the diagnostics
	// page; it never drives it.
	bridge Bridge

	// secureCookies marks cookies Secure.
	//
	// It is derived from APP_URL's scheme rather than being its own setting,
	// and deliberately so: an https origin gets Secure cookies, a plain-http
	// local run does not. Hardcoding Secure would make login untestable
	// outside TLS, because the browser would drop every cookie and the
	// symptom — a form that silently returns to itself — gives no hint why.
	//
	// So if this looks like Secure was forgotten: it was not. Deployments
	// set APP_URL to an https origin, which turns it on.
	secureCookies bool
}

// New builds a Server, parsing templates up front so a broken one is a boot
// failure rather than a 500 for the first visitor to hit that page.
func New(cfg *config.Config, db *sql.DB, logger *slog.Logger) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	// Built here rather than lazily so a bad TOTP_KEY fails at boot. Losing
	// or mistyping the key makes every enrolled secret unrecoverable, and
	// discovering that when someone first tries to log in is the worst
	// possible moment.
	cipher, err := auth.NewCipher(cfg.TOTPKey)
	if err != nil {
		return nil, fmt.Errorf("TOTP_KEY: %w", err)
	}

	locales, err := loadCatalogues()
	if err != nil {
		return nil, fmt.Errorf("load locales: %w", err)
	}

	return &Server{
		cfg:         cfg,
		catalogues:  locales,
		localeCodes: localeOrder(locales),
		db:          db,
		logger:      logger,
		templates:   tmpl,
		limiter:     auth.NewLimiter(0, 0),
		cipher:      cipher,
		mailer: auth.NewMailer(cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.User,
			cfg.SMTP.Pass, cfg.SMTP.From),
		secureCookies: secureCookies(cfg.AppURL),
	}, nil
}

// secureCookies decides whether cookies carry the Secure attribute.
//
// It fails closed: only an APP_URL that explicitly says http:// turns it
// off. APP_URL is optional — a deployment with no mail configured need not
// set it — and reading an unset value as "not https" would silently drop
// Secure from the session cookie on a stack that is in fact behind TLS,
// which is the one case where the flag matters most and nothing would say
// it had happened.
//
// Local development over plain http is the case this costs, and it costs
// nothing: browsers treat http://localhost as a secure context and send
// Secure cookies to it. Anything else plaintext can set APP_URL to its
// http:// origin and opt out deliberately.
func secureCookies(appURL string) bool {
	return !strings.HasPrefix(appURL, "http://")
}

// Handler returns the fully wrapped HTTP handler.
//
// Middleware order matters: panic recovery is outermost so it also catches a
// panic raised inside the logger, and the logger sits outside the routes so
// every request is recorded including 404s.
func (s *Server) Handler() http.Handler {
	return recoverPanic(s.logger, requestLogger(s.logger, s.cfg.TrustedProxies, securityHeaders(s.routes())))
}

// Bridge is what the server needs from the Signal bridge: whether it is
// running, and what it is doing. An interface so the web package does not
// depend on the bridge, and so a test can report a state without
// standing up a websocket.
type Bridge interface {
	// Alive reports whether the bridge is running, and why not if it is
	// not. Only "not running" is a reason to restart the process.
	Alive() (bool, string)
	Status() bridge.Status
}

// SetBridge attaches the bridge. Passing nil, or not calling it, means
// no bridge is configured — which is a valid deployment, not a fault.
func (s *Server) SetBridge(b Bridge) {
	// A nil *bridge.Supervisor in a non-nil interface would satisfy
	// every check below and panic on first use, so the untyped nil is
	// normalised here rather than at each call site.
	if b == nil || reflect.ValueOf(b).Kind() == reflect.Ptr && reflect.ValueOf(b).IsNil() {
		s.bridge = nil
		return
	}
	s.bridge = b
}
