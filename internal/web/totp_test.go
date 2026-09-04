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
	"time"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

var secretPattern = regexp.MustCompile(`<code>([A-Z2-7]+)</code>`)

// enrol walks an account through TOTP enrolment and returns its secret.
func enrol(t *testing.T, srv *Server, cookies []*http.Cookie) (string, []*http.Cookie) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/enroll-totp", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("enrolment page status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()

	match := secretPattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no secret on the enrolment page:\n%s", body)
	}
	secret := match[1]
	cookies = mergeCookies(cookies, rec.Result().Cookies())

	csrf := csrfFieldPattern.FindStringSubmatch(body)
	if csrf == nil {
		t.Fatal("no CSRF field on the enrolment page")
	}

	confirm := postForm(t, srv, "/enroll-totp", url.Values{
		"csrf_token": {csrf[1]},
		"code":       {codeFor(t, secret, time.Now())},
	}, cookies)
	// Enrolment lands on the recovery codes rather than redirecting: it is
	// the only moment they can be shown, so it is not skipped past.
	if confirm.Code != http.StatusOK {
		t.Fatalf("enrolment confirmation status = %d, want %d\n%s", confirm.Code, http.StatusOK, confirm.Body.String())
	}
	if !strings.Contains(confirm.Body.String(), "recovery-codes") {
		t.Fatal("enrolment did not show the recovery codes")
	}
	return secret, mergeCookies(cookies, confirm.Result().Cookies())
}

// nextWindow is used for codes submitted after enrolment. Confirming
// enrolment records the step it used, so the very same code is correctly
// refused as a replay — a real user simply waits for their app to roll over.
const nextWindow = 30 * time.Second

func codeFor(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

// The enrolment page must offer a scannable code, not only a string to type.
func TestEnrolmentPageOffersQRCode(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)

	req := httptest.NewRequest(http.MethodGet, "/enroll-totp", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `src="data:image/png;base64,`) {
		t.Error("the enrolment page has no inline QR code")
	}
	if !secretPattern.MatchString(body) {
		t.Error("the enrolment page does not show the secret for manual entry")
	}
}

// The secret stays pending until a code proves the phone holds it, so a
// mis-scanned QR cannot lock anyone out.
func TestEnrolmentSecretStaysPendingUntilConfirmed(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)

	req := httptest.NewRequest(http.MethodGet, "/enroll-totp", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	// Visited but not confirmed: the account is exactly as it was.
	if _, err := store.TOTPSecret(context.Background(), srv.db, user.ID); err == nil {
		t.Error("the secret went live without a confirming code")
	}
	if _, err := store.PendingTOTPSecret(context.Background(), srv.db, user.ID); err != nil {
		t.Errorf("no pending secret was stored: %v", err)
	}
}

func TestEnrolmentCompletes(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)

	reloaded, err := store.UserByID(context.Background(), srv.db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if !reloaded.HasTOTP {
		t.Error("HasTOTP = false after enrolment")
	}

	// Enrolment satisfies the second factor, so the session is usable now
	// rather than demanding another code immediately.
	req := httptest.NewRequest(http.MethodGet, "/leaderboard", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("board status = %d after enrolling, want %d", rec.Code, http.StatusOK)
	}
}

func TestEnrolmentRejectsWrongCode(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	csrf, cookies := getCSRF(t, srv, "/enroll-totp", cookies)

	rec := postForm(t, srv, "/enroll-totp", url.Values{
		"csrf_token": {csrf},
		"code":       {"000000"},
	}, cookies)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if _, err := store.TOTPSecret(context.Background(), srv.db, user.ID); err == nil {
		t.Error("a wrong code promoted the secret")
	}
}

// Full two-step login for an enrolled account.
func TestLoginRequiresTOTPWhenEnrolled(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	secret, _ := enrol(t, srv, cookies)

	// A fresh sign-in now owes a code.
	rec, fresh := login(t, srv, "admin@example.tld", testPassword)
	if got := rec.Header().Get("Location"); got != "/totp" {
		t.Fatalf("Location = %q, want /totp", got)
	}

	// The pending session reaches nothing but the prompt.
	req := httptest.NewRequest(http.MethodGet, "/leaderboard", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range fresh {
		req.AddCookie(c)
	}
	blocked := httptest.NewRecorder()
	srv.Handler().ServeHTTP(blocked, req)
	if blocked.Code != http.StatusSeeOther || blocked.Header().Get("Location") != "/totp" {
		t.Errorf("a pending session reached the board: status %d", blocked.Code)
	}

	csrf, fresh := getCSRF(t, srv, "/totp", fresh)
	done := postForm(t, srv, "/totp", url.Values{
		"csrf_token": {csrf},
		"code":       {codeFor(t, secret, time.Now().Add(nextWindow))},
	}, fresh)
	if done.Code != http.StatusSeeOther || done.Header().Get("Location") != landingPath {
		t.Fatalf("second factor failed: status %d\n%s", done.Code, done.Body.String())
	}
}

// A pending session on an already-enrolled account is what a password
// alone gets you: the second factor has not been cleared. It must not be
// enough to reach a fresh enrolment, which would let that password replace
// the real secret and delete the recovery codes with it.
func TestEnrolmentFormBlockedWhenAlreadyEnrolled(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	secret, _ := enrol(t, srv, cookies)

	// A fresh sign-in on the now-enrolled account is pending, not past TOTP.
	_, fresh := login(t, srv, "admin@example.tld", testPassword)

	req := httptest.NewRequest(http.MethodGet, "/enroll-totp", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range fresh {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/totp" {
		t.Fatalf("GET /enroll-totp on a pending enrolled session: status %d, Location %q, want 303 to /totp",
			rec.Code, rec.Header().Get("Location"))
	}

	// The account's real secret must still be the one that validates.
	csrf, fresh := getCSRF(t, srv, "/totp", fresh)
	done := postForm(t, srv, "/totp", url.Values{
		"csrf_token": {csrf},
		"code":       {codeFor(t, secret, time.Now().Add(nextWindow))},
	}, fresh)
	if done.Code != http.StatusSeeOther || done.Header().Get("Location") != landingPath {
		t.Fatalf("the original secret no longer validates after GET /enroll-totp: status %d\n%s",
			done.Code, done.Body.String())
	}
}

// The submit handler is reachable directly and must not rely on the form
// handler's guard: a POST with only a pending session must not be able to
// promote a fresh secret over the account's real one.
func TestEnrolmentSubmitCannotOverwriteExistingSecret(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	secret, _ := enrol(t, srv, cookies)

	// A fresh sign-in on the now-enrolled account is pending, not past TOTP
	// — the state reachable holding only the password.
	_, fresh := login(t, srv, "admin@example.tld", testPassword)

	// /enroll-totp itself now redirects away before rendering a form, so the
	// CSRF token comes from the one page a pending session can still reach.
	csrf, fresh := getCSRF(t, srv, "/totp", fresh)

	rec := postForm(t, srv, "/enroll-totp", url.Values{
		"csrf_token": {csrf},
		"code":       {"000000"},
	}, fresh)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/totp" {
		t.Fatalf("POST /enroll-totp on a pending enrolled session: status %d, Location %q, want 303 to /totp",
			rec.Code, rec.Header().Get("Location"))
	}

	// The account's real secret must be unchanged by the blocked attempt.
	csrf, fresh = getCSRF(t, srv, "/totp", fresh)
	done := postForm(t, srv, "/totp", url.Values{
		"csrf_token": {csrf},
		"code":       {codeFor(t, secret, time.Now().Add(nextWindow))},
	}, fresh)
	if done.Code != http.StatusSeeOther || done.Header().Get("Location") != landingPath {
		t.Fatalf("the account's secret was replaced by the blocked enrolment attempt: status %d\n%s",
			done.Code, done.Body.String())
	}
}

// A code from a step already accepted is refused, so one observed over a
// shoulder cannot be reused inside its window.
func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	secret, _ := enrol(t, srv, cookies)

	code := codeFor(t, secret, time.Now().Add(nextWindow))

	// First sign-in with this code.
	_, first := login(t, srv, "admin@example.tld", testPassword)
	csrf, first := getCSRF(t, srv, "/totp", first)
	rec := postForm(t, srv, "/totp", url.Values{"csrf_token": {csrf}, "code": {code}}, first)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("first use of the code failed: status %d", rec.Code)
	}

	// Second sign-in reusing the same code, still inside its window.
	_, second := login(t, srv, "admin@example.tld", testPassword)
	csrf, second = getCSRF(t, srv, "/totp", second)
	replay := postForm(t, srv, "/totp", url.Values{"csrf_token": {csrf}, "code": {code}}, second)

	if replay.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want the replayed code refused", replay.Code)
	}
	if !strings.Contains(replay.Body.String(), "already been used") {
		t.Errorf("the message does not explain the code was already used:\n%s", replay.Body.String())
	}
}

func TestTOTPIsRateLimited(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	enrol(t, srv, cookies)

	_, fresh := login(t, srv, "admin@example.tld", testPassword)

	var blocked bool
	for i := 0; i < auth.DefaultMaxAttempts+2; i++ {
		csrf, updated := getCSRF(t, srv, "/totp", fresh)
		fresh = updated
		rec := postForm(t, srv, "/totp", url.Values{"csrf_token": {csrf}, "code": {"000000"}}, fresh)
		if rec.Code == http.StatusTooManyRequests {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Error("six digits is a million possibilities and none of the attempts were throttled")
	}
}

// Changing the address on an account must not hand back a fresh budget of
// code guesses.
//
// Every attempt here comes from an address of its own, which is the case the
// per-account key exists for: many addresses against one account. It also
// means the IP key never blocks anything, so a 429 can only have come from
// the account key — and it only survives the rename if that key is not the
// address.
func TestTOTPRateLimitSurvivesAnEmailChange(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	enrol(t, srv, cookies)

	_, fresh := login(t, srv, "admin@example.tld", testPassword)

	// 203.0.113.0/24 rather than clientAddr's range, so no address here can
	// collide with one another test has already spent.
	guess := func(t *testing.T, n int) int {
		t.Helper()
		addr := fmt.Sprintf("203.0.113.%d:1025", n)
		csrf, updated := getCSRFFrom(t, srv, "/totp", fresh, addr)
		fresh = updated
		return postFormFrom(t, srv, "/totp",
			url.Values{"csrf_token": {csrf}, "code": {"000000"}}, fresh, addr).Code
	}

	for i := 0; i < auth.DefaultMaxAttempts; i++ {
		if got := guess(t, i+1); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d got %d, want %d", i+1, got, http.StatusUnauthorized)
		}
	}
	if got := guess(t, 200); got != http.StatusTooManyRequests {
		t.Fatalf("the account was never throttled: got %d, want %d", got, http.StatusTooManyRequests)
	}

	// Straight to the column: going through settings would need a mail
	// server and a verification round trip, and neither is under test here.
	if _, err := srv.db.ExecContext(context.Background(),
		`UPDATE users SET email = ? WHERE email = ?`,
		"renamed@example.tld", "admin@example.tld"); err != nil {
		t.Fatalf("rename the account: %v", err)
	}

	if got := guess(t, 201); got != http.StatusTooManyRequests {
		t.Errorf("after the rename an attempt got %d, want %d: the budget reset with the address",
			got, http.StatusTooManyRequests)
	}
}

// An admin who has not enrolled belongs in enrolment, not at a prompt for a
// secret that does not exist.
func TestUnenrolledAdminAtTOTPPromptGoesToEnrolment(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)

	req := httptest.NewRequest(http.MethodGet, "/totp", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Location"); got != "/enroll-totp" {
		t.Errorf("Location = %q, want /enroll-totp", got)
	}
}

// The second factor is a privilege change, so the token must change with it.
func TestSessionRotatesAfterTOTP(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	secret, _ := enrol(t, srv, cookies)

	_, fresh := login(t, srv, "admin@example.tld", testPassword)
	before := sessionCookieValue(t, fresh)

	csrf, fresh := getCSRF(t, srv, "/totp", fresh)
	rec := postForm(t, srv, "/totp", url.Values{
		"csrf_token": {csrf},
		"code":       {codeFor(t, secret, time.Now().Add(nextWindow))},
	}, fresh)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("second factor failed: status %d", rec.Code)
	}

	after := sessionCookieValue(t, mergeCookies(fresh, rec.Result().Cookies()))
	if before == after {
		t.Error("the session token survived the second factor unchanged")
	}
}

func sessionCookieValue(t *testing.T, cookies []*http.Cookie) string {
	t.Helper()
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

// Confirming enrolment records the step it used, so the enrolling code cannot
// be turned straight around into a login. This is the same rule as replay
// rejection, and it is worth pinning because it looks like a bug the first
// time it is hit.
func TestEnrollingCodeCannotBeReusedToLogIn(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	secret, _ := enrol(t, srv, cookies)

	// The very code that completed enrolment, still inside its window.
	code := codeFor(t, secret, time.Now())

	_, fresh := login(t, srv, "admin@example.tld", testPassword)
	csrf, fresh := getCSRF(t, srv, "/totp", fresh)
	rec := postForm(t, srv, "/totp", url.Values{"csrf_token": {csrf}, "code": {code}}, fresh)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want the enrolling code refused", rec.Code)
	}
}

// The code page names the account it is verifying and explains the timing,
// as the design's copy does.
func TestTOTPPageFollowsTheDesign(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	enrol(t, srv, cookies)

	_, fresh := login(t, srv, "admin@example.tld", testPassword)
	req := httptest.NewRequest(http.MethodGet, "/totp", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range fresh {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /totp = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Enter your code") {
		t.Error("the heading is not the design's")
	}
	if !strings.Contains(body, "admin@example.tld") {
		t.Error("the page does not name the account being verified")
	}
	if !strings.Contains(body, "every 30 seconds") {
		t.Error("the page does not explain the timing")
	}
	// The design's way out when the phone is gone, and it goes somewhere.
	if !strings.Contains(body, `href="/recovery"`) {
		t.Error("the page does not offer a recovery code")
	}
}
