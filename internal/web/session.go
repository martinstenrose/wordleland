package web

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/martinstenrose/wordleland/internal/store"
)

const sessionCookieName = "wordleland_session"

// contextKey is unexported so no other package can collide with these keys.
type contextKey int

const (
	sessionContextKey contextKey = iota
	userContextKey
)

// setSessionCookie writes the session cookie.
func (s *Server) setSessionCookie(w http.ResponseWriter, session store.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookieName,
		Value: base64.RawURLEncoding.EncodeToString(session.ID),
		Path:  "/",
		// The cookie carries an opaque token and nothing else, so
		// nothing here is worth reading from script.
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  session.ExpiresAt,
	})
}

// clearSessionCookie expires the session cookie.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// loadSession attaches the session and user to the request context when the
// cookie names a live one.
//
// Every request re-reads the session rather than trusting the cookie, which is
// what makes disabling an account take effect immediately: store.SessionUser
// refuses a session whose user has been disabled, so access ends on the next
// request instead of at expiry.
func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		id, err := base64.RawURLEncoding.DecodeString(cookie.Value)
		if err != nil {
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}

		session, user, err := store.SessionUser(r.Context(), s.db, id)
		if err != nil {
			if !errors.Is(err, store.ErrSessionNotFound) {
				s.logger.Error("read session", "error", err)
			}
			// Any failure means "log in again", so the stale cookie goes
			// rather than being presented on every later request.
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}

		if wrote, err := store.TouchSession(r.Context(), s.db, session); err != nil {
			// A failed refresh is not worth failing the request over: the
			// session is still valid, it just expires sooner.
			s.logger.Warn("extend session", "error", err)
		} else if wrote {
			session.ExpiresAt = session.ExpiresAt.Add(store.SessionLifetime)
			s.setSessionCookie(w, session)
		}

		ctx := context.WithValue(r.Context(), sessionContextKey, session)
		ctx = context.WithValue(ctx, userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sessionFrom returns the session on the request, if any.
func sessionFrom(r *http.Request) (store.Session, bool) {
	session, ok := r.Context().Value(sessionContextKey).(store.Session)
	return session, ok
}

// userFrom returns the authenticated user, if any.
func userFrom(r *http.Request) (store.User, bool) {
	user, ok := r.Context().Value(userContextKey).(store.User)
	return user, ok
}

// authenticated reports whether the request carries a fully authenticated
// session — one that has cleared TOTP where TOTP applies.
func authenticated(r *http.Request) (store.User, bool) {
	session, ok := sessionFrom(r)
	if !ok || session.PendingTOTP {
		return store.User{}, false
	}
	user, ok := userFrom(r)
	return user, ok
}

// requireAuth wraps a handler so only fully authenticated requests reach it.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authenticated(r); !ok {
			// A pending-TOTP session goes to the prompt; anything else to
			// the login page.
			if session, has := sessionFrom(r); has && session.PendingTOTP {
				http.Redirect(w, r, "/totp", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}
