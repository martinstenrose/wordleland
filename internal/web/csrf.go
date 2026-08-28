package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

const (
	csrfCookieName = "wordleland_csrf"
	csrfFieldName  = "csrf_token"
	csrfTokenLen   = 32
)

// issueCSRFToken sets a fresh token cookie and returns the value to embed in
// the form.
//
// This is the double-submit pattern: the same value travels in a cookie and in
// the form body, and a POST is accepted only when they match. An attacker's
// page can cause the browser to send our cookie, but cannot read it to put the
// matching value in their form.
//
// A session-bound token would be the stronger choice, but GET / is served
// before any session exists — there would be nothing to bind to at the moment
// the login form is rendered.
func (s *Server) issueCSRFToken(w http.ResponseWriter, r *http.Request) (string, error) {
	raw := make([]byte, csrfTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	http.SetCookie(w, &http.Cookie{
		Name:  csrfCookieName,
		Value: token,
		Path:  "/",
		// HttpOnly, which the textbook double-submit pattern says not to do:
		// that version has JavaScript read the cookie and copy it into the
		// request. Here the server embeds the value in the form itself, so
		// nothing needs to read it from script — and denying script access
		// removes a way to steal it. Do not "fix" this.
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	return token, nil
}

// checkCSRF verifies the submitted token against the cookie.
func (s *Server) checkCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	submitted := r.PostFormValue(csrfFieldName)
	if submitted == "" {
		return false
	}
	// Constant time out of habit rather than necessity: the comparison is
	// against a value the requester already holds.
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(submitted)) == 1
}
