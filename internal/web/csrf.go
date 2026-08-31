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

// issueCSRFToken returns the browser session's token, creating it when this
// is the first form rendered in that session.
//
// This is the double-submit pattern: the same value travels in a cookie and in
// the form body, and a POST is accepted only when they match. An attacker's
// page can cause the browser to send our cookie, but cannot read it to put the
// matching value in their form.
//
// The token is authenticated with the application's cipher before it is
// reused or accepted. That matters for the double-submit pattern: a sibling
// subdomain may be able to plant a cookie, but it cannot mint a value this
// server will authenticate.
func (s *Server) issueCSRFToken(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && s.validCSRFToken(cookie.Value) {
		return cookie.Value, nil
	}

	raw := make([]byte, csrfTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	sealed, err := s.cipher.Encrypt(raw)
	if err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(sealed)

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
	if err != nil || !s.validCSRFToken(cookie.Value) {
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

func (s *Server) validCSRFToken(token string) bool {
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	raw, err := s.cipher.Decrypt(sealed)
	return err == nil && len(raw) == csrfTokenLen
}
