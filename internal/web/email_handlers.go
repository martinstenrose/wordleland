package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

// forgotPage is the data for forgot_password.html.
type forgotPage struct {
	chrome

	Error       string
	Sent        bool
	Unavailable bool
}

// resetPage is the data for reset_password.html.
type resetPage struct {
	chrome

	Token   string
	Error   string
	Invalid bool
}

// handleForgotPasswordForm asks for an address.
func (s *Server) handleForgotPasswordForm(w http.ResponseWriter, r *http.Request) {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	// Running without a mail server is supported, so this says so plainly
	// and points at the CLI rather than failing.
	s.render(w, r, http.StatusOK, "forgot_password.html", forgotPage{
		chrome:      s.signedOutChrome(w, r, token),
		Unavailable: !s.mailer.Configured(),
	})
}

// handleForgotPasswordSubmit issues a reset link.
func (s *Server) handleForgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderError(w, r, http.StatusForbidden)
		return
	}
	if !s.mailer.Configured() {
		s.renderForgot(w, r, http.StatusOK, forgotPage{Unavailable: true})
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	if !s.limiter.Allow("reset:user:"+store.NormalizeEmail(email),
		"reset:ip:"+auth.ClientIP(r, s.cfg.TrustedProxies)) {
		// Still reported as sent: saying "too many requests" for one address
		// and "sent" for another would answer the question this endpoint
		// exists to refuse.
		s.renderForgot(w, r, http.StatusOK, forgotPage{Sent: true})
		return
	}

	// The response is identical whether or not the address exists, so
	// everything below happens quietly and the same page is rendered at the
	// end regardless.
	if err := s.issueResetLink(r, email); err != nil {
		s.logger.Error("issue reset link", "error", err)
	}
	s.renderForgot(w, r, http.StatusOK, forgotPage{Sent: true})
}

// issueResetLink creates and emails a reset token, if the address is one we
// know. A miss is not an error: it is the normal case for a typo.
func (s *Server) issueResetLink(r *http.Request, email string) error {
	user, err := store.UserByEmail(r.Context(), s.db, email)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			return nil
		}
		return err
	}
	// A retired account is not a way back in.
	if user.Disabled() {
		return nil
	}

	token, err := store.CreatePasswordResetToken(r.Context(), s.db, user.ID)
	if err != nil {
		return err
	}

	// Built from APP_URL, never from the request Host: otherwise someone
	// could request a reset for another account with a forged header and have
	// the emailed link point at a server they control.
	link := fmt.Sprintf("%s/reset-password?token=%s", s.cfg.AppURL, token)

	// The recipient's language, not the requester's. A reset can be asked
	// for from any browser, including one that is not theirs.
	t := s.translatorIn(user.Locale)
	return s.sendEmail(user.Email, emailPage{
		Lang:    t.locale,
		AppName: t.T("app.name"),
		Subject: t.T("email.reset.subject"),
		Preview: t.T("email.reset.preview"),
		Heading: t.T("email.reset.heading"),
		Intro:   t.T("email.reset.intro", user.Email),

		ActionURL:   link,
		ActionLabel: t.T("email.reset.action"),
		ActionNote:  t.T("email.reset.note"),

		Aside: &emailAside{
			Title: t.T("email.reset.aside.title"),
			Body:  t.T("email.reset.aside.body"),
		},
		// The time the request was made, so a reader can tell one message
		// from another. Not the requester's address: locating it would mean
		// asking a third party where an IP is, and the answer is not worth
		// the request.
		Meta:   t.T("email.reset.meta", time.Now().UTC().Format("2006-01-02 15:04 MST")),
		Footer: t.T("email.footer.security", t.T("app.name")),
	})
}

// handleResetPasswordForm shows the new-password form for a token.
func (s *Server) handleResetPasswordForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		s.renderReset(w, r, http.StatusBadRequest, resetPage{Invalid: true})
		return
	}
	s.renderReset(w, r, http.StatusOK, resetPage{Token: token})
}

// handleResetPasswordSubmit consumes a token and sets the password.
func (s *Server) handleResetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderError(w, r, http.StatusForbidden)
		return
	}

	token := r.PostFormValue("token")
	password := r.PostFormValue("password")
	confirm := r.PostFormValue("password_confirm")

	switch {
	case token == "":
		s.renderReset(w, r, http.StatusBadRequest, resetPage{Invalid: true})
		return
	case password != confirm:
		s.renderReset(w, r, http.StatusBadRequest, resetPage{
			Token: token, Error: "The two passwords do not match."})
		return
	case len([]rune(password)) < auth.MinPasswordLength:
		s.renderReset(w, r, http.StatusBadRequest, resetPage{
			Token: token,
			Error: fmt.Sprintf("Please choose a password of at least %d characters.", auth.MinPasswordLength)})
		return
	}

	// Reject a guessed, expired or spent token before paying for Argon2. The
	// token is checked again when it is consumed, so a concurrent request
	// cannot turn this early check into a second use.
	if err := store.ValidatePasswordResetToken(r.Context(), s.db, token); err != nil {
		if errors.Is(err, store.ErrResetTokenInvalid) {
			s.renderReset(w, r, http.StatusBadRequest, resetPage{Invalid: true})
			return
		}
		s.logger.Error("validate reset token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	if !s.limiter.Allow("reset-submit:token:"+store.HashToken(token),
		"reset-submit:ip:"+auth.ClientIP(r, s.cfg.TrustedProxies)) {
		s.renderReset(w, r, http.StatusTooManyRequests, resetPage{Invalid: true})
		return
	}

	var hash string
	err := s.limiter.WithHashSlot(r.Context(), func() error {
		var err error
		hash, err = s.hashPassword(password)
		return err
	})
	if err != nil {
		s.logger.Error("hash password", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	user, err := store.ConsumePasswordResetToken(r.Context(), s.db, token, hash)
	if err != nil {
		if errors.Is(err, store.ErrResetTokenInvalid) {
			s.renderReset(w, r, http.StatusBadRequest, resetPage{Invalid: true})
			return
		}
		s.logger.Error("consume reset token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	// A reset is not a bypass: an enrolled user still owes their second
	// factor, so they are sent to the login page rather than straight in.
	s.logger.Info("password reset via email", "user", user.ID)
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/?reset=1", http.StatusSeeOther)
}

// verifiedPage is the data for verified.html. It was a map, which renders
// without complaint but silently leaves the language and theme empty:
// html/template treats a missing map key as a zero value rather than an
// error.
type verifiedPage struct {
	chrome

	Email   string
	Invalid bool
}

// handleVerifyEmail records that an address was reachable.
//
// Verification gates nothing in v1: it exists so reset mail is known to
// reach a real inbox rather than a typo.
func (s *Server) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}

	user, err := store.ConsumeEmailVerificationToken(r.Context(), s.db, token)
	if err != nil {
		if errors.Is(err, store.ErrResetTokenInvalid) {
			s.render(w, r, http.StatusBadRequest, "verified.html", verifiedPage{
				chrome:  s.newChrome(w, r, "", "", true),
				Invalid: true,
			})
			return
		}
		s.logger.Error("verify email", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	s.render(w, r, http.StatusOK, "verified.html", verifiedPage{
		chrome: s.newChrome(w, r, "", "", true),
		Email:  user.Email,
	})
}

func (s *Server) renderForgot(w http.ResponseWriter, r *http.Request, status int, page forgotPage) {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	page.chrome = s.signedOutChrome(w, r, token)
	if !s.mailer.Configured() {
		page.Unavailable = true
	}
	s.render(w, r, status, "forgot_password.html", page)
}

func (s *Server) renderReset(w http.ResponseWriter, r *http.Request, status int, page resetPage) {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	page.chrome = s.signedOutChrome(w, r, token)
	s.render(w, r, status, "reset_password.html", page)
}

// sendVerifyEmail asks a new address to confirm itself.
//
// It goes to the new address, never the old one: the point is to prove the
// new one is reachable, and a message to the old address proves nothing
// about it.
func (s *Server) sendVerifyEmail(r *http.Request, user store.User, to string) error {
	token, err := store.CreateEmailVerificationToken(r.Context(), s.db, user.ID)
	if err != nil {
		return err
	}
	link := fmt.Sprintf("%s/verify-email?token=%s", s.cfg.AppURL, token)

	t := s.translatorIn(user.Locale)
	return s.sendEmail(to, emailPage{
		Lang:    t.locale,
		AppName: t.T("app.name"),
		Subject: t.T("email.verify.subject"),
		Preview: t.T("email.verify.preview"),
		Heading: t.T("email.verify.heading"),
		Intro:   t.T("email.verify.intro", to),

		ActionURL:   link,
		ActionLabel: t.T("email.verify.action"),
		ActionNote:  t.T("email.verify.note"),

		Aside: &emailAside{
			Title: t.T("email.verify.aside.title"),
			Body:  t.T("email.verify.aside.body", user.Email),
		},
		Footer: t.T("email.footer.security", t.T("app.name")),
	})
}
