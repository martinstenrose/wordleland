package web

import (
	"bytes"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/skip2/go-qrcode"
)

// enrolPage is the data for enroll_totp.html.
type enrolPage struct {
	chrome

	Error string

	// template.URL, not string: html/template refuses to emit a data: URI
	// from an untyped value in a src attribute, replacing it with
	// #ZgotmplZ, because it cannot tell a safe payload from a script vector.
	// The value here is base64 of PNG bytes this server just produced, so
	// vouching for it is accurate rather than a way around the check.
	QRCode template.URL

	Secret    string
	Mandatory bool
}

// qrDataURI wraps encoded PNG bytes as a URL the template will emit.
//
// template.URL is an assertion that switches off contextual escaping for this
// value, so it is only safe where the content is known rather than merely
// expected. It holds here because the input is PNG bytes this process just
// produced from a secret it just generated: no part of it comes from a
// request, a form, or the database.
//
// Do not reuse this for a URL that carries anything a user supplied. If the
// source is ever not "bytes we made ourselves a few lines ago", the correct
// move is to serve the image from its own handler rather than to widen this.
func qrDataURI(png []byte) template.URL {
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
}

// totpPage is the data for totp.html.
type totpPage struct {
	chrome

	// Email names the account being verified, as the design's copy does.
	Email string
	Error string
}

// enrolCandidate returns the user this enrolment page is for, having
// already sent anyone who shouldn't be here somewhere sensible.
//
// Enrolment mints a secret and, on submit, overwrites whatever was there
// before — so an account that already has one does not belong here. A
// pending session needs only the password to reach this page; letting it
// re-enrol would let a password alone replace the real secret and delete
// the recovery codes with it, defeating the second factor entirely. Route
// it to the TOTP prompt instead, which asks for the secret that already
// exists. An enrolled account whose session has already cleared TOTP has
// nothing to do here either.
func (s *Server) enrolCandidate(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	session, ok := sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return store.User{}, false
	}
	user, ok := userFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return store.User{}, false
	}
	if user.HasTOTP {
		if session.PendingTOTP {
			http.Redirect(w, r, "/totp", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, landingPath, http.StatusSeeOther)
		}
		return store.User{}, false
	}
	return user, true
}

// handleEnrolTOTPForm shows a QR code for a new secret.
//
// The secret is stored pending, never live, so abandoning this page leaves the
// account exactly as it was. Revisiting generates a fresh one, which is
// what makes a mis-scanned code recoverable by simply reloading.
func (s *Server) handleEnrolTOTPForm(w http.ResponseWriter, r *http.Request) {
	user, ok := s.enrolCandidate(w, r)
	if !ok {
		return
	}

	secret, uri, err := auth.GenerateTOTPSecret(user.Email)
	if err != nil {
		s.logger.Error("generate totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	sealed, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		s.logger.Error("encrypt totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	if err := store.SetPendingTOTPSecret(r.Context(), s.db, user.ID, sealed); err != nil {
		s.logger.Error("store pending totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		s.logger.Error("render qr code", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	s.render(w, r, http.StatusOK, "enroll_totp.html", enrolPage{
		chrome: s.signedOutChrome(w, r, token),
		// Inlined as a data: URI rather than served from a second endpoint,
		// which would mean holding the secret across two requests.
		QRCode: qrDataURI(png),
		// Shown alongside the QR code for anyone entering it by hand.
		Secret:    secret,
		Mandatory: user.IsAdmin,
	})
}

// handleEnrolTOTPSubmit promotes the pending secret once a code proves it was
// scanned correctly.
func (s *Server) handleEnrolTOTPSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderError(w, r, http.StatusForbidden)
		return
	}
	user, ok := s.enrolCandidate(w, r)
	if !ok {
		return
	}

	// Keyed by user id rather than address. Sign-in has to key on the
	// address because it is all it has before the account is found, but
	// here the account is already known — and an address can change, which
	// would hand the holder of a password a fresh budget of code guesses
	// just by editing a settings field.
	if !s.limiter.Allow("totp:user:"+strconv.FormatInt(user.ID, 10), "totp:ip:"+auth.ClientIP(r, s.cfg.TrustedProxies)) {
		s.renderEnrolError(w, r, http.StatusTooManyRequests,
			"Too many attempts. Please wait a few minutes and try again.")
		return
	}

	sealed, err := store.PendingTOTPSecret(r.Context(), s.db, user.ID)
	if err != nil {
		if errors.Is(err, store.ErrNoPendingSecret) {
			// The page was reloaded or the enrolment abandoned; sending them
			// back regenerates a secret rather than failing.
			http.Redirect(w, r, "/enroll-totp", http.StatusSeeOther)
			return
		}
		s.logger.Error("read pending totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	secret, err := s.cipher.Decrypt(sealed)
	if err != nil {
		s.logger.Error("decrypt pending totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	step, err := auth.ValidateTOTP(string(secret), strings.TrimSpace(r.PostFormValue("code")), time.Now())
	if err != nil {
		s.renderEnrolError(w, r, http.StatusUnauthorized,
			"That code is not right. Check your authenticator app and try again.")
		return
	}

	if err := store.PromotePendingTOTPSecret(r.Context(), s.db, store.PlayerActor(user.ID), user.ID, step); err != nil {
		s.logger.Error("promote totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.limiter.Reset("totp:user:" + strconv.FormatInt(user.ID, 10))

	// Enrolment completes the second factor, so the session is rotated out of
	// its pending state rather than requiring an immediate second code.
	if err := s.clearPendingTOTP(w, r); err != nil {
		s.logger.Error("rotate session", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	// The codes are issued here rather than offered later, because later is
	// after the phone has been lost. This is the one moment the person is
	// both enrolled and looking at the screen.
	s.showRecoveryCodes(w, r, user, true)
}

// handleTOTPForm prompts for a code from an already-enrolled account.
func (s *Server) handleTOTPForm(w http.ResponseWriter, r *http.Request) {
	session, ok := sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !session.PendingTOTP {
		http.Redirect(w, r, landingPath, http.StatusSeeOther)
		return
	}
	user, ok := userFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// An admin who has not enrolled belongs in enrolment, not at a prompt for
	// a secret that does not exist.
	if !user.HasTOTP {
		http.Redirect(w, r, "/enroll-totp", http.StatusSeeOther)
		return
	}

	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, "totp.html", totpPage{chrome: s.signedOutChrome(w, r, token), Email: user.Email})
}

// handleTOTPSubmit checks a code and clears the pending flag.
func (s *Server) handleTOTPSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderError(w, r, http.StatusForbidden)
		return
	}
	session, ok := sessionFrom(r)
	if !ok || !session.PendingTOTP {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	user, ok := userFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Six digits is a million possibilities; without a limit it is
	// brute-forceable in an afternoon.
	if !s.limiter.Allow("totp:user:"+strconv.FormatInt(user.ID, 10), "totp:ip:"+auth.ClientIP(r, s.cfg.TrustedProxies)) {
		s.renderTOTPError(w, r, http.StatusTooManyRequests,
			"Too many attempts. Please wait a few minutes and try again.")
		return
	}

	sealed, err := store.TOTPSecret(r.Context(), s.db, user.ID)
	if err != nil {
		s.logger.Error("read totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	secret, err := s.cipher.Decrypt(sealed)
	if err != nil {
		// Almost certainly the wrong TOTP_KEY: worth saying plainly, because
		// the operator's fix is to restore the key or reset the enrolment.
		s.logger.Error("decrypt totp secret; is TOTP_KEY the one this secret was enrolled with?", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	step, err := auth.ValidateTOTP(string(secret), strings.TrimSpace(r.PostFormValue("code")), time.Now())
	if err != nil {
		s.renderTOTPError(w, r, http.StatusUnauthorized, "That code is not right.")
		return
	}

	// Replay: the code is valid for its whole window, so one observed over a
	// shoulder would otherwise work again within thirty seconds.
	if err := store.RecordTOTPStep(r.Context(), s.db, user.ID, step); err != nil {
		if errors.Is(err, store.ErrCodeReplayed) {
			s.renderTOTPError(w, r, http.StatusUnauthorized,
				"That code has already been used. Wait for your app to show the next one.")
			return
		}
		s.logger.Error("record totp step", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	s.limiter.Reset("totp:user:" + strconv.FormatInt(user.ID, 10))
	if err := s.clearPendingTOTP(w, r); err != nil {
		s.logger.Error("rotate session", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, landingPath, http.StatusSeeOther)
}

// clearPendingTOTP rotates the session into its fully authenticated form.
//
// A new id rather than an update in place: the second factor is a privilege
// change, so a token captured before it must not carry the rights granted
// after it.
func (s *Server) clearPendingTOTP(w http.ResponseWriter, r *http.Request) error {
	session, ok := sessionFrom(r)
	if !ok {
		return errors.New("no session to rotate")
	}
	fresh, err := store.RotateSession(r.Context(), s.db, session, false)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, fresh)
	return nil
}

func (s *Server) renderTOTPError(w http.ResponseWriter, r *http.Request, status int, message string) {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	page := totpPage{chrome: s.signedOutChrome(w, r, token), Error: message}
	if user, ok := userFrom(r); ok {
		page.Email = user.Email
	}
	s.render(w, r, status, "totp.html", page)
}

// renderEnrolError re-renders enrolment with the pending secret intact, so a
// mistyped code does not force a fresh scan.
func (s *Server) renderEnrolError(w http.ResponseWriter, r *http.Request, status int, message string) {
	user, _ := userFrom(r)

	sealed, err := store.PendingTOTPSecret(r.Context(), s.db, user.ID)
	if err != nil {
		http.Redirect(w, r, "/enroll-totp", http.StatusSeeOther)
		return
	}
	secret, err := s.cipher.Decrypt(sealed)
	if err != nil {
		http.Redirect(w, r, "/enroll-totp", http.StatusSeeOther)
		return
	}

	_, uri, err := auth.RebuildTOTPURI(string(secret), user.Email)
	if err != nil {
		http.Redirect(w, r, "/enroll-totp", http.StatusSeeOther)
		return
	}
	var png bytes.Buffer
	if data, err := qrcode.Encode(uri, qrcode.Medium, 256); err == nil {
		png.Write(data)
	}

	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.render(w, r, status, "enroll_totp.html", enrolPage{
		chrome:    s.signedOutChrome(w, r, token),
		Error:     message,
		QRCode:    qrDataURI(png.Bytes()),
		Secret:    string(secret),
		Mandatory: user.IsAdmin,
	})
}
