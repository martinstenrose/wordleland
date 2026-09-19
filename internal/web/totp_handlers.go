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

	// Replacing marks the page as a second enrolment over a live one: the
	// account already has a secret and is rotating it. It asks for the
	// password as well as the code, and says so.
	Replacing bool
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

// enrolCandidate returns the user this enrolment page is for and whether
// they are rotating a secret rather than setting a first one, having already
// sent anyone who shouldn't be here somewhere sensible.
//
// Enrolment mints a secret and, on submit, overwrites whatever was there
// before. A pending session has cleared only the password, so letting it
// re-enrol would let a password alone replace the real secret and discard
// the recovery codes with it, defeating the second factor entirely. Route it
// to the TOTP prompt instead, which asks for the secret that already exists.
//
// A session that has cleared both factors is a different matter, and it is
// the one this page used to turn away: rotating the secret — a new phone, a
// lost one — is something an account with two-factor on is supposed to be
// able to do, and the settings screen has offered it all along. What makes it
// safe is not the session but the password the submit asks for on top of the
// new code, which is what a borrowed screen does not have.
//
// There is one secret per account, not one per app: an authenticator app is
// a holder of it, and any number of them can hold the same one. So this is
// the only shape a change can take — a fresh secret, scanned into as many
// apps as the reader wants, and every app holding the old one goes quiet.
//
// "Secret" rather than "key" throughout, because TOTP_KEY is already the key
// this server encrypts every one of these with, and the admin settings screen
// shows it. The standards agree: RFC 4226 calls it the shared secret, and the
// otpauth:// URI carries it as secret=.
func (s *Server) enrolCandidate(w http.ResponseWriter, r *http.Request) (user store.User, replacing bool, ok bool) {
	session, ok := sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return store.User{}, false, false
	}
	user, ok = userFrom(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return store.User{}, false, false
	}
	if user.HasTOTP && session.PendingTOTP {
		http.Redirect(w, r, "/totp", http.StatusSeeOther)
		return store.User{}, false, false
	}
	return user, user.HasTOTP, true
}

// handleEnrolTOTPForm shows a QR code for a new secret.
//
// The secret is stored pending, never live, so abandoning this page leaves the
// account exactly as it was. Revisiting generates a fresh one, which is
// what makes a mis-scanned code recoverable by simply reloading.
func (s *Server) handleEnrolTOTPForm(w http.ResponseWriter, r *http.Request) {
	user, replacing, ok := s.enrolCandidate(w, r)
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

	page := enrolPage{
		chrome: s.signedOutChrome(w, r, token),
		// Inlined as a data: URI rather than served from a second endpoint,
		// which would mean holding the secret across two requests.
		QRCode: qrDataURI(png),
		// Shown alongside the QR code for anyone entering it by hand.
		Secret:    secret,
		Mandatory: user.IsAdmin,
		Replacing: replacing,
	}
	// The card alone, for the dialog the settings screen opens it in. The
	// page around it is the sign-in family's frame, which is right for an
	// admin sent here at sign-in and wrong over a page somebody is already
	// reading.
	if wantsPartial(r) {
		s.renderBlock(w, r, http.StatusOK, "enroll_totp.html", "content", page)
		return
	}
	s.render(w, r, http.StatusOK, "enroll_totp.html", page)
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
	user, replacing, ok := s.enrolCandidate(w, r)
	if !ok {
		return
	}

	// Rotating the secret asks for the password as well, for the reason a
	// password change does: the session is already
	// authenticated, and a borrowed screen should not be enough to take the
	// second factor off an account — which, since promotion discards the
	// recovery codes with the old secret, is the whole of it.
	//
	// It is checked before the code so that a wrong password never spends an
	// attempt at the new secret, and it shares the settings screen's limiter
	// key because it is the same password being guessed at from the same
	// account.
	if replacing && !s.verifyEnrolPassword(w, r, user) {
		return
	}

	// Keyed by user id rather than address. Sign-in has to key on the
	// address because it is all it has before the account is found, but
	// here the account is already known — and an address can change, which
	// would hand the holder of a password a fresh budget of code guesses
	// just by editing a settings field.
	if !s.limiter.Allow("totp:user:"+strconv.FormatInt(user.ID, 10), "totp:ip:"+auth.ClientIP(r, s.cfg.TrustedProxies)) {
		s.renderEnrolError(w, r, http.StatusTooManyRequests,
			s.translatorFor(w, r).T("enrol.error.tooMany"))
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
			s.translatorFor(w, r).T("enrol.error.wrong"))
		return
	}

	if err := store.PromotePendingTOTPSecret(r.Context(), s.db, store.PlayerActor(user.ID), user.ID, step); err != nil {
		s.logger.Error("promote totp secret", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	s.limiter.Reset("totp:user:" + strconv.FormatInt(user.ID, 10))
	if replacing {
		s.limiter.Reset("settings-password:user:" + strconv.FormatInt(user.ID, 10))
	}

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
			s.translatorFor(w, r).T("totp.error.tooMany"))
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
		s.renderTOTPError(w, r, http.StatusUnauthorized, s.translatorFor(w, r).T("totp.error.wrong"))
		return
	}

	// Replay: the code is valid for its whole window, so one observed over a
	// shoulder would otherwise work again within thirty seconds.
	if err := store.RecordTOTPStep(r.Context(), s.db, user.ID, step); err != nil {
		if errors.Is(err, store.ErrCodeReplayed) {
			s.renderTOTPError(w, r, http.StatusUnauthorized,
				s.translatorFor(w, r).T("totp.error.reused"))
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
	page := enrolPage{
		chrome:    s.signedOutChrome(w, r, token),
		Error:     message,
		QRCode:    qrDataURI(png.Bytes()),
		Secret:    string(secret),
		Mandatory: user.IsAdmin,
		Replacing: user.HasTOTP,
	}
	if wantsPartial(r) {
		s.renderBlock(w, r, status, "enroll_totp.html", "content", page)
		return
	}
	s.render(w, r, status, "enroll_totp.html", page)
}

// verifyEnrolPassword checks the password a replacement asks for, reporting
// whether the caller should carry on. It renders the failure itself, back
// onto the enrolment page with the pending secret intact, so a mistyped
// password does not cost the reader the QR code they have just scanned.
func (s *Server) verifyEnrolPassword(w http.ResponseWriter, r *http.Request, user store.User) bool {
	// Before the hash rather than after: verifying costs 64 MiB and the CPU
	// time to go with it, so an unthrottled endpoint is a way to spend the
	// box's memory as well as a way to find the password.
	clientIP := auth.ClientIP(r, s.cfg.TrustedProxies)
	if !s.limiter.Allow("settings-password:user:"+strconv.FormatInt(user.ID, 10),
		"settings-password:ip:"+clientIP) {
		s.logger.Warn("two-factor replacement rate limited", "ip", clientIP)
		s.renderEnrolError(w, r, http.StatusTooManyRequests,
			s.translatorFor(w, r).T("enrol.error.tooMany"))
		return false
	}

	var verifyErr error
	if err := s.limiter.WithHashSlot(r.Context(), func() error {
		verifyErr = auth.VerifyPassword(user.PasswordHash, r.PostFormValue("password"))
		return nil
	}); err != nil {
		s.logger.Error("verify password", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return false
	}
	if verifyErr != nil {
		s.renderEnrolError(w, r, http.StatusUnauthorized,
			s.translatorFor(w, r).T("settings.error.wrongPassword"))
		return false
	}
	return true
}
