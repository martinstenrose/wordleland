package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

var recoveryCodePattern = regexp.MustCompile(`<code>([0-9A-Z-]{19})</code>`)

// enrolWithCodes runs enrolment and returns the codes it showed, which is
// the only place they exist in plaintext.
func enrolWithCodes(t *testing.T, srv *Server, cookies []*http.Cookie) (string, []string, []*http.Cookie) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/enroll-totp", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	secret := secretPattern.FindStringSubmatch(body)
	csrf := csrfFieldPattern.FindStringSubmatch(body)
	if secret == nil || csrf == nil {
		t.Fatal("no secret or CSRF field on the enrolment page")
	}
	cookies = mergeCookies(cookies, rec.Result().Cookies())

	confirm := postForm(t, srv, "/enroll-totp", url.Values{
		"csrf_token": {csrf[1]},
		"code":       {codeFor(t, secret[1], time.Now())},
	}, cookies)
	if confirm.Code != http.StatusOK {
		t.Fatalf("enrolment = %d:\n%s", confirm.Code, confirm.Body.String())
	}

	var codes []string
	for _, m := range recoveryCodePattern.FindAllStringSubmatch(confirm.Body.String(), -1) {
		codes = append(codes, m[1])
	}
	return secret[1], codes, mergeCookies(cookies, confirm.Result().Cookies())
}

// Enrolment hands over a full set, once. Doing it later is doing it after
// the phone is already gone.
func TestEnrolmentIssuesRecoveryCodes(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "admin@example.tld", true)
	_, cookies := login(t, srv, "admin@example.tld", testPassword)

	_, codes, _ := enrolWithCodes(t, srv, cookies)
	if len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("enrolment showed %d codes, want %d", len(codes), auth.RecoveryCodeCount)
	}

	n, err := store.CountRecoveryCodes(context.Background(), srv.db, user.ID)
	if err != nil {
		t.Fatalf("CountRecoveryCodes: %v", err)
	}
	if n != auth.RecoveryCodeCount {
		t.Errorf("%d codes stored, want %d", n, auth.RecoveryCodeCount)
	}
}

// The whole point: a code signs in when the authenticator app cannot.
func TestRecoveryCodeCompletesSignIn(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)
	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	_, codes, _ := enrolWithCodes(t, srv, cookies)

	// A fresh sign-in owes a second factor.
	rec, fresh := login(t, srv, "admin@example.tld", testPassword)
	if got := rec.Header().Get("Location"); got != "/totp" {
		t.Fatalf("Location = %q, want /totp", got)
	}

	csrf, fresh := getCSRF(t, srv, "/recovery", fresh)
	done := postForm(t, srv, "/recovery", url.Values{
		"csrf_token": {csrf},
		"code":       {codes[0]},
	}, fresh)
	if done.Code != http.StatusSeeOther {
		t.Fatalf("recovery sign-in = %d:\n%s", done.Code, done.Body.String())
	}
	fresh = mergeCookies(fresh, done.Result().Cookies())

	// And the session is fully authenticated, not still pending.
	req := httptest.NewRequest(http.MethodGet, "/leaderboard", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range fresh {
		req.AddCookie(c)
	}
	page := httptest.NewRecorder()
	srv.Handler().ServeHTTP(page, req)
	if page.Code != http.StatusOK {
		t.Errorf("board status = %d after recovery sign-in, want %d", page.Code, http.StatusOK)
	}
}

// Single use, end to end: the same code must not open a second session.
func TestRecoveryCodeCannotBeReused(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)
	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	_, codes, _ := enrolWithCodes(t, srv, cookies)

	spend := func() int {
		_, fresh := login(t, srv, "admin@example.tld", testPassword)
		csrf, fresh := getCSRF(t, srv, "/recovery", fresh)
		return postForm(t, srv, "/recovery", url.Values{
			"csrf_token": {csrf},
			"code":       {codes[0]},
		}, fresh).Code
	}

	if got := spend(); got != http.StatusSeeOther {
		t.Fatalf("first use = %d, want %d", got, http.StatusSeeOther)
	}
	if got := spend(); got != http.StatusUnauthorized {
		t.Errorf("second use = %d, want %d", got, http.StatusUnauthorized)
	}
}

// Knowing an email is not enough. Without a password-verified session the
// page is a way to spend somebody else's codes.
func TestRecoveryNeedsAPasswordFirst(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)
	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	_, codes, _ := enrolWithCodes(t, srv, cookies)

	rec := fetchAs(t, srv, "/recovery", nil)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("GET /recovery signed out = %d, want a redirect", rec.Code)
	}

	post := postForm(t, srv, "/recovery", url.Values{"code": {codes[0]}}, nil)
	if post.Code == http.StatusSeeOther && post.Header().Get("Location") == landingPath {
		t.Fatal("a recovery code signed in without a password")
	}
	n, _ := store.CountRecoveryCodes(context.Background(), srv.db, 1)
	if n != auth.RecoveryCodeCount {
		t.Errorf("%d codes left; an unauthenticated request spent one", n)
	}
}

// Recovery shares the TOTP allowance rather than getting its own, which
// would double the guesses an attacker gets at one account.
func TestRecoveryIsRateLimited(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)
	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	enrolWithCodes(t, srv, cookies)

	_, fresh := login(t, srv, "admin@example.tld", testPassword)

	var last int
	for range 15 {
		csrf, next := getCSRF(t, srv, "/recovery", fresh)
		fresh = next
		last = postForm(t, srv, "/recovery", url.Values{
			"csrf_token": {csrf},
			"code":       {"0000-0000-0000-0000"},
		}, fresh).Code
		if last == http.StatusTooManyRequests {
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("last status = %d after 15 wrong codes, want %d", last, http.StatusTooManyRequests)
	}
}

// The codes are shown once and never again: there must be no route that
// re-renders the set a person already has.
func TestRecoveryCodesAreNotRetrievable(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)
	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	_, codes, cookies := enrolWithCodes(t, srv, cookies)

	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	for _, path := range []string{"/enroll-totp", "/settings", "/today"} {
		body := fetchAs(t, srv, path, session).Body.String()
		for _, code := range codes {
			if strings.Contains(body, code) {
				t.Fatalf("%s renders a recovery code", path)
			}
		}
	}
}
