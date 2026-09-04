package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

// recoveryCodesPage shows a freshly issued set. It is the only time the
// codes exist in plaintext: only their hashes are stored, so this page
// cannot be rebuilt later and there is no route that re-renders it.
type recoveryCodesPage struct {
	chrome

	Codes []string

	// Enrolling distinguishes the interstitial that follows enrolment from
	// the same list reached out of Settings — one continues into the app,
	// the other goes back to where it was opened from.
	Enrolling bool
}

// recoveryPage is the data for recovery.html, the way in when the
// authenticator app is gone.
type recoveryPage struct {
	chrome

	Email string
	Error string
}

// showRecoveryCodes issues a set and renders it once.
func (s *Server) showRecoveryCodes(w http.ResponseWriter, r *http.Request, user store.User, enrolling bool) {
	codes, err := store.ReplaceRecoveryCodes(r.Context(), s.db, store.PlayerActor(user.ID), user.ID)
	if err != nil {
		s.logger.Error("issue recovery codes", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	page := recoveryCodesPage{
		chrome:    s.newChrome(w, r, "", "", false),
		Codes:     codes,
		Enrolling: enrolling,
	}
	page.CSRFToken = token
	s.render(w, r, http.StatusOK, "recovery_codes.html", page)
}

// handleRecoveryForm prompts for a recovery code instead of a TOTP code.
func (s *Server) handleRecoveryForm(w http.ResponseWriter, r *http.Request) {
	user, ok := s.recoveryCandidate(w, r)
	if !ok {
		return
	}

	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, "recovery.html",
		recoveryPage{chrome: s.signedOutChrome(w, r, token), Email: user.Email})
}

// handleRecoverySubmit spends a code and completes the second factor.
func (s *Server) handleRecoverySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderError(w, r, http.StatusForbidden)
		return
	}
	user, ok := s.recoveryCandidate(w, r)
	if !ok {
		return
	}

	// The same bucket as the TOTP step, deliberately: a recovery code is a
	// second factor too, and giving it a separate allowance would double
	// the guesses an attacker gets at the same account.
	if !s.limiter.Allow("totp:user:"+strconv.FormatInt(user.ID, 10), "totp:ip:"+auth.ClientIP(r, s.cfg.TrustedProxies)) {
		s.renderRecoveryError(w, r, http.StatusTooManyRequests, "recovery.error.tooMany")
		return
	}

	err := store.ConsumeRecoveryCode(r.Context(), s.db, user.ID, r.PostFormValue("code"))
	if errors.Is(err, store.ErrNoRecoveryCode) {
		s.renderRecoveryError(w, r, http.StatusUnauthorized, "recovery.error.noSuchCode")
		return
	}
	if err != nil {
		s.logger.Error("consume recovery code", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	// A spent code is as good as a code from the app, so the replay guard
	// moves forward with it: an observed TOTP code from this same window
	// must not still be usable afterwards.
	if err := store.RecordTOTPStep(r.Context(), s.db, user.ID, auth.CurrentStep(time.Now())); err != nil &&
		!errors.Is(err, store.ErrCodeReplayed) {
		s.logger.Error("record totp step", "error", err)
	}

	s.limiter.Reset("totp:user:" + strconv.FormatInt(user.ID, 10))
	if err := s.clearPendingTOTP(w, r); err != nil {
		s.logger.Error("rotate session", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	remaining, err := store.CountRecoveryCodes(r.Context(), s.db, user.ID)
	if err != nil {
		s.logger.Error("count recovery codes", "error", err)
	}
	s.logger.Info("signed in with a recovery code", "remaining", remaining)
	http.Redirect(w, r, landingPath, http.StatusSeeOther)
}

// recoveryCandidate returns the half-authenticated user this page is for,
// having already sent anyone else somewhere sensible.
//
// The gate is the same as the TOTP step's: the password has been accepted
// and the session is pending. Without that, this page would be a way to
// spend somebody's codes knowing only their email.
func (s *Server) recoveryCandidate(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	session, ok := sessionFrom(r)
	if !ok || !session.PendingTOTP {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return store.User{}, false
	}
	user, ok := userFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return store.User{}, false
	}
	// Nothing to recover before there is an enrolment to recover from.
	if !user.HasTOTP {
		http.Redirect(w, r, "/enroll-totp", http.StatusSeeOther)
		return store.User{}, false
	}
	return user, true
}

func (s *Server) renderRecoveryError(w http.ResponseWriter, r *http.Request, status int, key string) {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	page := recoveryPage{chrome: s.signedOutChrome(w, r, token)}
	if user, ok := userFrom(r); ok {
		page.Email = user.Email
	}
	page.Error = page.T.T(key)
	s.render(w, r, status, "recovery.html", page)
}

// handleSettingsRecoveryCodes issues a fresh set from Settings.
//
// It replaces rather than reveals: only hashes are stored, so an existing
// set cannot be shown again. Saying "generate" rather than "view" is the
// honest label for what the button does.
func (s *Server) handleSettingsRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderError(w, r, http.StatusForbidden)
		return
	}
	user, ok := authenticated(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// Codes are a way past the second factor, so there has to be a second
	// factor for them to be a way past.
	if !user.HasTOTP {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	s.showRecoveryCodes(w, r, user, false)
}
