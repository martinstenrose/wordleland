package web

import (
	"strconv"
	"time"

	"errors"
	"github.com/martinstenrose/wordleland/internal/wordle"
	"net/http"
	"strings"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

// loginPage is the data for login.html.
type loginPage struct {
	chrome

	Error string
	Email string

	Stats signInStats
}

// signInStats is the group summary beside the sign-in form.
//
// Aggregates only. The page is reachable by anyone who finds the host,
// so it says how much has been played without saying who plays or how they
// are doing.
type signInStats struct {
	Games int
	Days  int
	Rows  []signInStat
}

type signInStat struct {
	Key   string
	Value string
}

// signInSummary reads the group totals, returning a zero value on error.
//
// A failure here must not stop anyone signing in: the panel is decoration
// beside the form, and a blank panel is a far better outcome than a login
// page that will not render.
func (s *Server) signInSummary(r *http.Request) signInStats {
	summary, err := store.GroupSummary(r.Context(), s.db, wordle.PuzzleForDate(time.Now()))
	if err != nil {
		s.logger.Error("group summary", "error", err)
		return signInStats{}
	}
	if summary.Games == 0 {
		return signInStats{}
	}

	out := signInStats{Games: summary.Games, Days: summary.Days}
	out.Rows = append(out.Rows,
		signInStat{Key: "signin.stat.players", Value: strconv.Itoa(summary.Players)},
		signInStat{Key: "signin.stat.solved", Value: strconv.Itoa(summary.SolvedPercent) + "%"},
	)
	if summary.Average != nil {
		out.Rows = append(out.Rows, signInStat{
			Key:   "signin.stat.average",
			Value: strconv.FormatFloat(*summary.Average, 'f', 2, 64),
		})
	}
	out.Rows = append(out.Rows, signInStat{
		Key:   "signin.stat.today",
		Value: strconv.Itoa(summary.FiledToday),
	})
	return out
}

// handleLoginForm serves the login page at "/".
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := authenticated(r); ok {
		http.Redirect(w, r, landingPath, http.StatusSeeOther)
		return
	}
	if session, ok := sessionFrom(r); ok && session.PendingTOTP {
		http.Redirect(w, r, "/totp", http.StatusSeeOther)
		return
	}

	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", func() loginPage {
		ch := s.signedOutChrome(w, r, token)
		return loginPage{chrome: ch, Stats: s.signInSummary(r)}
	}())
}

// handleLoginSubmit checks a password and starts a session.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		// Re-rendering with a fresh token rather than erroring: the usual
		// cause is a form left open until the cookie expired.
		s.renderLoginError(w, r, http.StatusForbidden, "", "Your session expired. Please try again.")
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")

	// Rate limited before hashing, so a blocked attempt costs nothing.
	// Both keys are counted: one address spraying accounts and many addresses
	// targeting one account are both throttled.
	clientIP := auth.ClientIP(r, s.cfg.TrustedProxies)
	if !s.limiter.Allow("login:user:"+store.NormalizeEmail(email), "login:ip:"+clientIP) {
		s.logger.Warn("login rate limited", "ip", clientIP)
		s.renderLoginError(w, r, http.StatusTooManyRequests, email,
			"Too many attempts. Please wait a few minutes and try again.")
		return
	}

	user, err := s.authenticate(r, email, password)
	if err != nil {
		// One message for every failure: a wrong address, a wrong password
		// and a disabled account must be indistinguishable, or the form
		// becomes a way to enumerate who has an account.
		if !errors.Is(err, errBadCredentials) {
			s.logger.Error("authenticate", "error", err)
		}
		s.renderLoginError(w, r, http.StatusUnauthorized, email,
			"That email address and password do not match.")
		return
	}

	// Only the account key is cleared. A successful login proves this
	// requester holds these credentials, so the failures before it were
	// almost certainly typos and should not keep the owner locked out.
	//
	// The address key keeps counting: it defends against blind spraying
	// across many accounts, and someone who holds one valid account is
	// exactly the attacker who would otherwise reset their own budget at
	// will. The cost is that a legitimate user who fails ten times stays
	// throttled for the window — but they now hold a session and do not
	// need to log in again.
	s.limiter.Reset("login:user:" + store.NormalizeEmail(email))

	// 2FA is mandatory for admins and optional for players. A session
	// starts pending whenever a second step is owed, and grants nothing but
	// the prompt until it is cleared.
	needsTOTP := user.HasTOTP || user.IsAdmin

	session, err := store.CreateSession(r.Context(), s.db, user.ID, needsTOTP)
	if err != nil {
		s.logger.Error("create session", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, session)

	switch {
	case user.IsAdmin && !user.HasTOTP:
		// An admin without TOTP cannot reach anything until they enrol.
		http.Redirect(w, r, "/enroll-totp", http.StatusSeeOther)
	case needsTOTP:
		http.Redirect(w, r, "/totp", http.StatusSeeOther)
	default:
		http.Redirect(w, r, landingPath, http.StatusSeeOther)
	}
}

// errBadCredentials covers every reason a login was refused. Callers must not
// learn which one applied.
var errBadCredentials = errors.New("invalid credentials")

// authenticate verifies an address and password.
func (s *Server) authenticate(r *http.Request, email, password string) (store.User, error) {
	user, err := store.UserByEmail(r.Context(), s.db, email)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			// Hash anyway, so a missing account does not answer faster than a
			// wrong password and reveal which addresses are registered.
			s.wasteTime(r)
			return store.User{}, errBadCredentials
		}
		return store.User{}, err
	}

	var verifyErr error
	if err := s.limiter.WithHashSlot(r.Context(), func() error {
		verifyErr = auth.VerifyPassword(user.PasswordHash, password)
		return nil
	}); err != nil {
		return store.User{}, err
	}
	if verifyErr != nil {
		if errors.Is(verifyErr, auth.ErrMismatch) {
			return store.User{}, errBadCredentials
		}
		// A malformed stored hash is an operational fault, not a failed
		// login, and is worth seeing in the logs as itself.
		return store.User{}, verifyErr
	}

	// Checked after the password so a disabled account is not distinguishable
	// from a wrong one by response time or message.
	if user.Disabled() {
		return store.User{}, errBadCredentials
	}
	return user, nil
}

// wasteTime performs a hash against a throwaway value so an unknown address
// costs the same as a known one.
func (s *Server) wasteTime(r *http.Request) {
	_ = s.limiter.WithHashSlot(r.Context(), func() error {
		_, _ = auth.HashPassword("timing equalisation")
		return nil
	})
}

// renderLoginError re-renders the form with a message and a fresh CSRF token.
func (s *Server) renderLoginError(w http.ResponseWriter, r *http.Request, status int, email, message string) {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.render(w, r, status, "login.html", func() loginPage {
		ch := s.signedOutChrome(w, r, token)
		return loginPage{chrome: ch, Error: message, Email: email, Stats: s.signInSummary(r)}
	}())
}

// handleLogout ends the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderError(w, r, http.StatusForbidden)
		return
	}

	if session, ok := sessionFrom(r); ok {
		if err := store.DeleteSession(r.Context(), s.db, session.ID); err != nil {
			s.logger.Error("delete session", "error", err)
		}
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleBoardPage serves the authenticated leaderboard.
func (s *Server) handleBoardPage(w http.ResponseWriter, r *http.Request) {
	s.handleBoard(w, r, "", viewPath("", viewBoard), false)
}

// handleTodayPage serves the authenticated front page.
func (s *Server) handleTodayPage(w http.ResponseWriter, r *http.Request) {
	s.handleToday(w, r, "", viewPath("", viewToday), false)
}

// handleMonthsPage serves the authenticated month-by-month view.
func (s *Server) handleMonthsPage(w http.ResponseWriter, r *http.Request) {
	s.handleMonths(w, r, "", viewPath("", viewMonths), false)
}

// handleGridPage serves the authenticated grid.
func (s *Server) handleGridPage(w http.ResponseWriter, r *http.Request) {
	s.handleGrid(w, r, "", viewPath("", viewGrid), false)
}

// handlePlayersPage serves the authenticated players view.
func (s *Server) handlePlayersPage(w http.ResponseWriter, r *http.Request) {
	s.handlePlayers(w, r, "", viewPath("", viewPlayers), false)
}

// handlePlayerPage serves the authenticated player detail page.
func (s *Server) handlePlayerPage(w http.ResponseWriter, r *http.Request) {
	s.handlePlayer(w, r, r.PathValue("slug"), "", viewPath("", viewPlayers), false)
}
