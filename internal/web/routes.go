package web

import (
	"fmt"
	"net/http"
)

// routes builds the mux.
//
// The application lives openly at its hostname: "/" serves the login
// page and the authenticated area sits on ordinary paths. There is no secret
// path prefix — the share slug is a read-only capability link under /share/,
// not a wrapper around the application.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.Handle("GET /static/", s.serveStatic())

	// The login page is the root, and the authenticated area sits on ordinary
	// paths behind it. There is no secret prefix wrapping the
	// application: the slug is a read-only share link, not a hiding place.
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /leaderboard", s.requireAuth(s.handleBoardPage))
	mux.HandleFunc("GET /today", s.requireAuth(s.handleTodayPage))
	mux.HandleFunc("GET /months", s.requireAuth(s.handleMonthsPage))
	mux.HandleFunc("GET /grid", s.requireAuth(s.handleGridPage))
	mux.HandleFunc("GET /players", s.requireAuth(s.handlePlayersPage))

	// A reader's own account. Each section posts on its own, so a rejected
	// password does not throw away a name they also typed.
	mux.HandleFunc("GET /settings", s.requireAuth(s.handleSettings))
	mux.HandleFunc("POST /settings/name", s.requireAuth(s.handleSettingsName))
	mux.HandleFunc("POST /settings/email", s.requireAuth(s.handleSettingsEmail))
	mux.HandleFunc("POST /settings/password", s.requireAuth(s.handleSettingsPassword))
	mux.HandleFunc("POST /settings/recovery-codes", s.requireAuth(s.handleSettingsRecoveryCodes))
	mux.HandleFunc("GET /p/{slug}", s.requireAuth(s.handlePlayerPage))

	// Admin. The admin UI is deliberately partial; the player slice of it is
	// pulled forward because correcting a name or a link is the change the
	// roster actually needs, and reaching for docker compose exec to rename
	// somebody is the wrong shape of chore.
	mux.HandleFunc("GET /admin/players", s.requireAdmin(s.handleAdminPlayers))
	mux.HandleFunc("GET /admin/players/{slug}", s.requireAdmin(s.handleAdminPlayers))
	mux.HandleFunc("POST /admin/players/{slug}", s.requireAdmin(s.handleAdminPlayerSubmit))
	mux.HandleFunc("POST /admin/players/{slug}/invite", s.requireAdmin(s.handleAdminInvite))

	mux.HandleFunc("GET /admin/activity", s.requireAdmin(s.handleAdminActivity))
	mux.HandleFunc("GET /admin/diagnostics", s.requireAdmin(s.handleAdminDiagnostics))
	mux.HandleFunc("GET /admin/activity/{id}", s.requireAdmin(s.handleAdminActivityDetail))
	mux.HandleFunc("GET /admin/pending", s.requireAdmin(s.handleAdminPending))
	mux.HandleFunc("POST /admin/pending/assign", s.requireAdmin(s.handleAdminPendingAssign))
	mux.HandleFunc("POST /admin/pending/discard", s.requireAdmin(s.handleAdminPendingDiscard))

	// Claiming a player. Reached from an emailed link, so it sits outside
	// authentication — the token is the credential.
	mux.HandleFunc("GET /invite", s.handleInviteForm)
	mux.HandleFunc("POST /invite", s.handleInviteSubmit)

	// The second factor and enrolment. These are reachable with a pending
	// session, which grants access to nothing else.
	mux.HandleFunc("GET /totp", s.handleTOTPForm)
	mux.HandleFunc("POST /totp", s.handleTOTPSubmit)
	mux.HandleFunc("GET /recovery", s.handleRecoveryForm)
	mux.HandleFunc("POST /recovery", s.handleRecoverySubmit)
	mux.HandleFunc("GET /enroll-totp", s.handleEnrolTOTPForm)
	mux.HandleFunc("POST /enroll-totp", s.handleEnrolTOTPSubmit)

	// Account recovery, on ordinary paths. Rotating the share slug does not
	// touch these.
	mux.HandleFunc("GET /forgot-password", s.handleForgotPasswordForm)
	mux.HandleFunc("POST /forgot-password", s.handleForgotPasswordSubmit)
	mux.HandleFunc("GET /reset-password", s.handleResetPasswordForm)
	mux.HandleFunc("POST /reset-password", s.handleResetPasswordSubmit)
	mux.HandleFunc("GET /verify-email", s.handleVerifyEmail)

	// Linked from the footer on every page, so it sits outside
	// authentication like the rest of the pages a stranger can reach.
	mux.HandleFunc("GET /privacy", s.handlePrivacy)

	// Outside the login surface entirely: protected by its own bearer token,
	// for scripts and curl. The bridge no longer comes through here — it
	// calls ingest directly — but the endpoint stays for everything else.
	mux.HandleFunc("POST /api/ingest", s.handleIngest)

	mux.HandleFunc("GET /share/{slug}/", s.handleShare)

	// Sessions are loaded for every route, including the share link and the
	// login form, so a signed-in visitor can be redirected rather than shown
	// a form they do not need.
	return s.loadSession(mux)
}

// handleRoot serves the login page.
//
// "GET /" is a catch-all in the Go 1.22 pattern syntax: it matches every path
// that no other pattern claims. Without this guard, /anything would render the
// login page under a 200 instead of a 404.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.renderError(w, r, http.StatusNotFound)
		return
	}
	s.handleLoginForm(w, r)
}

// handleHealth reports that the process is up and can reach the database.
//
// It sits outside authentication so an uptime monitor can reach it, which does
// mean the bare hostname confirms the application exists. Since the app now serves
// the login page at "/" anyway, the application is not hiding and this costs
// nothing it was not already giving away.
// handleHealth is a liveness probe, and only that.
//
// It answers one question: would restarting this process help? The database
// being unreachable and a configured Signal bridge having stopped are both
// yes. A bridge that is merely disconnected and retrying is no — signal-cli
// being down is not a reason to bounce the board, and a restart would only
// interrupt the backoff that is already fixing it.
//
// That distinction matters more since the services merged. Failing this
// probe used to take down a bridge; now it takes down the whole
// application, including the part that was working.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if err := s.db.PingContext(r.Context()); err != nil {
		s.logger.Error("health check failed", "error", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	if s.bridge != nil {
		if alive, why := s.bridge.Alive(); !alive {
			// Only ever the supervisor giving up, which is a bug it cannot
			// recover from. Everything else it retries by itself.
			http.Error(w, why, http.StatusServiceUnavailable)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

// robotsTxt asks every crawler to stay away.
//
// Nothing here is meant to be found by search: it is one group's scoreboard,
// and the share link is a capability URL that could otherwise be indexed by
// name if somebody posted it in public.
//
// It does nothing about the bots probing for WordPress installs — those
// never read it. Turning that noise away is a job for the reverse proxy,
// not the application.
const robotsTxt = `User-agent: *
Disallow: /
`

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	fmt.Fprint(w, robotsTxt)
}

// handleFavicon answers the path browsers probe for whether or not the page
// declares an icon.
//
// It serves the PNG under its real content type rather than an ICO: every
// browser that still asks for this path accepts a PNG when told that is
// what it is, and the alternative is a 404 in the log on every cold visit.
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	icon, err := templateFS.ReadFile("static/icon-180.png")
	if err != nil {
		// Only reachable if the embed and this path disagree, which is a
		// build-time mistake rather than a runtime one.
		s.logger.Error("read favicon", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := w.Write(icon); err != nil {
		s.logger.Warn("write favicon", "error", err)
	}
}
