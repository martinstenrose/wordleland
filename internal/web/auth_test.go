package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

const testPassword = "correct horse battery staple"

// seedLogin creates a user who can sign in.
func seedLogin(t *testing.T, srv *Server, email string, admin bool) store.User {
	t.Helper()

	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}
	user, err := store.CreateUser(context.Background(), srv.db, store.SystemActor(), email, hash, admin)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	return user
}

var csrfFieldPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// mergeCookies applies a response's Set-Cookie headers to a jar the way a
// browser would: replacing by name rather than accumulating, so a refreshed
// token supersedes the old one instead of hiding behind it.
func mergeCookies(jar []*http.Cookie, fresh []*http.Cookie) []*http.Cookie {
	for _, c := range fresh {
		replaced := false
		for i, existing := range jar {
			if existing.Name == c.Name {
				jar[i] = c
				replaced = true
				break
			}
		}
		if !replaced {
			jar = append(jar, c)
		}
	}
	return jar
}

// clientAddr varies the source address per test, so the per-address half of
// the rate limiter does not make unrelated tests interfere.
func clientAddr(t *testing.T) string {
	t.Helper()
	sum := 0
	for _, r := range t.Name() {
		sum = (sum*31 + int(r)) % 60000
	}
	return fmt.Sprintf("198.51.100.%d:%d", sum%254+1, sum%40000+1024)
}

// getCSRF fetches a page and returns its CSRF token and cookies.
func getCSRF(t *testing.T, srv *Server, path string, cookies []*http.Cookie) (string, []*http.Cookie) {
	t.Helper()
	return getCSRFFrom(t, srv, path, cookies, clientAddr(t))
}

func getCSRFFrom(t *testing.T, srv *Server, path string, cookies []*http.Cookie, addr string) (string, []*http.Cookie) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = addr
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	match := csrfFieldPattern.FindStringSubmatch(rec.Body.String())
	if match == nil {
		t.Fatalf("no CSRF field on %s (status %d)", path, rec.Code)
	}
	return match[1], mergeCookies(cookies, rec.Result().Cookies())
}

// postForm submits a form with the given cookies.
func postForm(t *testing.T, srv *Server, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return postFormFrom(t, srv, path, form, cookies, clientAddr(t))
}

func postFormFrom(t *testing.T, srv *Server, path string, form url.Values, cookies []*http.Cookie, addr string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = addr
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// login performs a full sign-in and returns the resulting cookies.
func login(t *testing.T, srv *Server, email, password string) (*httptest.ResponseRecorder, []*http.Cookie) {
	t.Helper()
	return loginFrom(t, srv, email, password, clientAddr(t))
}

// loginFrom signs in from a named source address, so the per-account and
// per-address halves of the limiter can be exercised independently.
func loginFrom(t *testing.T, srv *Server, email, password, addr string) (*httptest.ResponseRecorder, []*http.Cookie) {
	t.Helper()

	token, cookies := getCSRFFrom(t, srv, "/", nil, addr)
	rec := postFormFrom(t, srv, "/login", url.Values{
		"csrf_token": {token},
		"email":      {email},
		"password":   {password},
	}, cookies, addr)
	return rec, mergeCookies(cookies, rec.Result().Cookies())
}

func TestLoginSucceeds(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	rec, cookies := login(t, srv, "martin@example.tld", testPassword)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	// A player without TOTP goes straight through; only admins and enrolled
	// users owe a second step.
	if got := rec.Header().Get("Location"); got != landingPath {
		t.Errorf("Location = %q, want the landing page", got)
	}

	req := httptest.NewRequest(http.MethodGet, landingPath, nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	dash := httptest.NewRecorder()
	srv.Handler().ServeHTTP(dash, req)

	if dash.Code != http.StatusOK {
		t.Fatalf("board status = %d, want %d", dash.Code, http.StatusOK)
	}
	if !strings.Contains(dash.Body.String(), "martin@example.tld") {
		t.Error("the board does not show who is signed in")
	}
}

func TestLoginCreatesSessionRow(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	login(t, srv, "martin@example.tld", testPassword)

	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("sessions = %d, want 1", count)
	}
}

// A wrong address, a wrong password and a disabled account must be
// indistinguishable, or the form becomes a way to enumerate accounts.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	disabled := seedLogin(t, srv, "gone@example.tld", false)
	if err := store.SetUserDisabled(context.Background(), srv.db, store.SystemActor(), disabled.ID, true); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}

	cases := []struct {
		name     string
		email    string
		password string
	}{
		{"unknown address", "nobody@example.tld", testPassword},
		{"wrong password", "martin@example.tld", "not the password"},
		{"disabled account", "gone@example.tld", testPassword},
	}

	var bodies []string
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := login(t, srv, tt.email, tt.password)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			bodies = append(bodies, rec.Body.String())
		})
	}

	// Compare the rendered messages, not just the codes.
	for i := 1; i < len(bodies); i++ {
		if extractError(bodies[0]) != extractError(bodies[i]) {
			t.Errorf("failure messages differ:\n%q\nvs\n%q",
				extractError(bodies[0]), extractError(bodies[i]))
		}
	}
}

var errorPattern = regexp.MustCompile(`role="alert">([^<]*)<`)

func extractError(body string) string {
	if m := errorPattern.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// A disabled account must not be able to sign in at all, checked at the login
// handler rather than only when a session is later read.
func TestLoginRefusesDisabledAccount(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)
	if err := store.SetUserDisabled(context.Background(), srv.db, store.SystemActor(), user.ID, true); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}

	rec, _ := login(t, srv, "martin@example.tld", testPassword)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("sessions = %d for a disabled account, want 0", count)
	}
}

// Disabling mid-session must take effect on the next request, not at expiry:
// a session can last a month, which is not what an admin means by "disable".
func TestDisablingEndsAccessImmediately(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	_, cookies := login(t, srv, "martin@example.tld", testPassword)

	// Disabled directly, leaving the session row in place, so this exercises
	// the middleware's check rather than SetUserDisabled's session deletion.
	if _, err := srv.db.Exec(`UPDATE users SET disabled_at = CURRENT_TIMESTAMP WHERE id = ?`, user.ID); err != nil {
		t.Fatalf("disable user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, landingPath, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect to the login page", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

// 2FA is mandatory for admins. An admin without it cannot reach anything
// until they enrol.
func TestAdminWithoutTOTPIsSentToEnrolment(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	rec, cookies := login(t, srv, "admin@example.tld", testPassword)

	if got := rec.Header().Get("Location"); got != "/enroll-totp" {
		t.Errorf("Location = %q, want /enroll-totp", got)
	}

	// The session exists but grants nothing yet.
	req := httptest.NewRequest(http.MethodGet, landingPath, nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	dash := httptest.NewRecorder()
	srv.Handler().ServeHTTP(dash, req)

	if dash.Code != http.StatusSeeOther {
		t.Fatalf("board status = %d, want a redirect", dash.Code)
	}
	if got := dash.Header().Get("Location"); got != "/totp" {
		t.Errorf("Location = %q, want the pending session sent to /totp", got)
	}
}

func TestBoardRequiresAuthentication(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, landingPath, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

func TestLoginRejectsMissingCSRFToken(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	_, cookies := getCSRF(t, srv, "/", nil)

	// The cookie is present but the form field is not, which is what a
	// cross-site form would look like.
	rec := postForm(t, srv, "/login", url.Values{
		"email":    {"martin@example.tld"},
		"password": {testPassword},
	}, cookies)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestLoginRejectsMismatchedCSRFToken(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	_, cookies := getCSRF(t, srv, "/", nil)

	rec := postForm(t, srv, "/login", url.Values{
		"csrf_token": {"a token from somewhere else"},
		"email":      {"martin@example.tld"},
		"password":   {testPassword},
	}, cookies)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestLoginRejectsMissingCSRFCookie(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	token, _ := getCSRF(t, srv, "/", nil)

	// The field without the cookie: an attacker who somehow learned the token
	// value still cannot present the matching cookie.
	rec := postForm(t, srv, "/login", url.Values{
		"csrf_token": {token},
		"email":      {"martin@example.tld"},
		"password":   {testPassword},
	}, nil)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// The double-submit cookie is deliberately HttpOnly, unlike the textbook
// pattern, because the server embeds the value in the form itself.
func TestCSRFCookieFlags(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name != csrfCookieName {
			continue
		}
		found = true
		if !c.HttpOnly {
			t.Error("the CSRF cookie is not HttpOnly; nothing needs to read it from script")
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("SameSite = %v, want Lax", c.SameSite)
		}
	}
	if !found {
		t.Fatal("no CSRF cookie was set")
	}
}

func TestSessionCookieFlags(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	rec, _ := login(t, srv, "martin@example.tld", testPassword)

	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name != sessionCookieName {
			continue
		}
		found = true
		if !c.HttpOnly {
			t.Error("the session cookie is not HttpOnly")
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("SameSite = %v, want Lax", c.SameSite)
		}
		if c.Value == "" {
			t.Error("the session cookie is empty")
		}
	}
	if !found {
		t.Fatal("no session cookie was set")
	}
}

// The limiter is checked before hashing, so a blocked attempt costs nothing.
func TestLoginIsRateLimited(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	var blocked bool
	for i := 0; i < auth.DefaultMaxAttempts+2; i++ {
		rec, _ := login(t, srv, "martin@example.tld", "wrong password")
		if rec.Code == http.StatusTooManyRequests {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatalf("no attempt was rate limited within %d tries", auth.DefaultMaxAttempts+2)
	}

	// Blocked by the account key, so even the right password is refused for
	// the rest of the window.
	rec, _ := login(t, srv, "martin@example.tld", testPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d after the limit, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

// A user who mistypes and then succeeds should not stay locked out of their
// own account. The account counter clears; the address counter deliberately
// does not.
func TestSuccessfulLoginResetsTheAccountLimiter(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	const firstAddr = "198.51.100.10:1024"
	for i := 0; i < auth.DefaultMaxAttempts-1; i++ {
		loginFrom(t, srv, "martin@example.tld", "wrong password", firstAddr)
	}
	if rec, _ := loginFrom(t, srv, "martin@example.tld", testPassword, firstAddr); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want the login to succeed", rec.Code)
	}

	// From a different address, so this exercises the account counter alone.
	const secondAddr = "198.51.100.11:1024"
	for i := 0; i < auth.DefaultMaxAttempts-1; i++ {
		rec, _ := loginFrom(t, srv, "martin@example.tld", "wrong password", secondAddr)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was throttled; the account counter did not reset on success", i+1)
		}
	}
}

// The address counter is not reset by a successful login. Otherwise someone
// holding one valid account could refresh their budget at will and spray the
// rest of the roster indefinitely.
func TestSuccessfulLoginDoesNotResetTheAddressLimiter(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)
	seedLogin(t, srv, "alex@example.tld", false)

	const addr = "198.51.100.20:1024"

	// Spend most of the address budget guessing at one account, then log in
	// successfully to the account this attacker legitimately holds.
	for i := 0; i < auth.DefaultMaxAttempts-1; i++ {
		loginFrom(t, srv, "alex@example.tld", "wrong password", addr)
	}
	if rec, _ := loginFrom(t, srv, "martin@example.tld", testPassword, addr); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want the login to succeed", rec.Code)
	}

	// The address is now at its limit regardless of that success.
	rec, _ := loginFrom(t, srv, "alex@example.tld", "wrong password", addr)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d: the address budget was refreshed by an unrelated success",
			rec.Code, http.StatusTooManyRequests)
	}
}

func TestLogout(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	_, cookies := login(t, srv, "martin@example.tld", testPassword)
	token, cookies := getCSRF(t, srv, landingPath, cookies)

	rec := postForm(t, srv, "/logout", url.Values{"csrf_token": {token}}, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("sessions = %d after logout, want 0", count)
	}
}

func TestLogoutRequiresCSRF(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	_, cookies := login(t, srv, "martin@example.tld", testPassword)

	rec := postForm(t, srv, "/logout", url.Values{}, cookies)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Error("the session was destroyed by a request with no CSRF token")
	}
}

// A signed-in visitor should not be shown a form they do not need.
func TestRootRedirectsWhenSignedIn(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	_, cookies := login(t, srv, "martin@example.tld", testPassword)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != landingPath {
		t.Errorf("Location = %q, want the landing page", got)
	}
}

// A tampered or stale cookie must not be presented on every later request.
func TestInvalidSessionCookieIsCleared(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-a-real-session"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the invalid session cookie was not cleared")
	}
}
