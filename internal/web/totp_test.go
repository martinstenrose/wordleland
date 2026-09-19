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

// Rotating the secret is a thing an account with two-factor on is
// supposed to be able to do — a new phone, a lost one — and the settings
// screen has offered it all along. The page turned every one of those people
// away: it refused anybody who already had a secret, which is exactly who the
// link is for, and the redirect landed them back on Today with nothing said.
func TestAnEnrolledAccountCanRotateItsSecret(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	first, cookies := enrol(t, srv, cookies)

	// The link the settings screen offers, followed by the person it offers
	// it to.
	page, cookies := getWith(t, srv, "/enroll-totp", cookies)
	if page.Code != http.StatusOK {
		t.Fatalf("the replacement page = %d, want %d", page.Code, http.StatusOK)
	}
	body := page.Body.String()
	if !strings.Contains(body, `name="password"`) {
		t.Error("a replacement does not ask for the password")
	}
	if !strings.Contains(body, "Rotate your two-factor secret") {
		t.Error("the replacement page is headed as a first enrolment")
	}

	second := secretPattern.FindStringSubmatch(body)
	if second == nil {
		t.Fatal("no secret on the replacement page")
	}
	if second[1] == first {
		t.Error("the replacement offers the secret already in use")
	}
	csrf := csrfFieldPattern.FindStringSubmatch(body)

	// Until it is confirmed, the secret in hand is still the one that works
	// — which is what makes abandoning this page safe.
	if liveSecret(t, srv, user.ID) != first {
		t.Error("the live secret changed before the replacement was confirmed")
	}

	done := postForm(t, srv, "/enroll-totp", url.Values{
		"csrf_token": {csrf[1]},
		"code":       {codeFor(t, second[1], time.Now())},
		"password":   {testPassword},
	}, cookies)
	if done.Code != http.StatusOK {
		t.Fatalf("the replacement = %d, want %d\n%s", done.Code, http.StatusOK, done.Body.String())
	}
	// It ends where a first enrolment ends, and for a stronger reason:
	// promotion discards the old codes with the old secret, so this is the
	// one moment a new set can be handed over.
	if !strings.Contains(done.Body.String(), "recovery-codes") {
		t.Error("rotating the secret does not hand over a new set of recovery codes")
	}

	if liveSecret(t, srv, user.ID) != second[1] {
		t.Error("the new secret is not the live one")
	}
}

// The session is already signed in, so the password is the whole of what
// stops a borrowed screen taking the second factor off an account — and with
// it the recovery codes, which promotion discards along with the old secret.
func TestReplacingAnAuthenticatorNeedsThePassword(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	first, cookies := enrol(t, srv, cookies)

	page, cookies := getWith(t, srv, "/enroll-totp", cookies)
	body := page.Body.String()
	csrf := csrfFieldPattern.FindStringSubmatch(body)[1]
	second := secretPattern.FindStringSubmatch(body)[1]

	for _, password := range []string{"", "not-the-password"} {
		rec := postForm(t, srv, "/enroll-totp", url.Values{
			"csrf_token": {csrf},
			"code":       {codeFor(t, second, time.Now())},
			"password":   {password},
		}, cookies)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("password %q: status = %d, want %d", password, rec.Code, http.StatusUnauthorized)
		}
		if liveSecret(t, srv, user.ID) != first {
			t.Errorf("password %q: the secret was replaced without it", password)
		}
	}
}

// getWith is a GET carrying a whole cookie jar, which the enrolment flow
// needs: fetchAs takes the session alone, and the CSRF cookie matters here.
func getWith(t *testing.T, srv *Server, path string, cookies []*http.Cookie) (*httptest.ResponseRecorder, []*http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec, mergeCookies(cookies, rec.Result().Cookies())
}

// liveSecret is the account's working two-factor secret, which is stored
// encrypted — comparing the column against a base32 string compares a cipher
// text to a plaintext and is true of nothing.
func liveSecret(t *testing.T, srv *Server, userID int64) string {
	t.Helper()
	sealed, err := store.TOTPSecret(context.Background(), srv.db, userID)
	if err != nil {
		t.Fatalf("TOTPSecret: %v", err)
	}
	secret, err := srv.cipher.Decrypt(sealed)
	if err != nil {
		t.Fatalf("decrypt TOTP secret: %v", err)
	}
	return string(secret)
}

// Two-factor is optional for a player, which has to mean they can turn it
// off again: an account that can only ever add one has a setting it cannot
// undo without an admin and a shell.
func TestAPlayerCanTurnTwoFactorOff(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "player@example.tld", false)

	_, cookies := login(t, srv, "player@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)

	// The control is an outlined red link that only opens the question —
	// nothing is turned off by pressing it.
	page, cookies := getWith(t, srv, "/settings/security", cookies)
	if !strings.Contains(page.Body.String(), `href="/settings/security?confirm=totp"`) {
		t.Fatal("the security screen does not offer to turn two-factor off")
	}

	asked, cookies := getWith(t, srv, "/settings/security?confirm=totp", cookies)
	body := asked.Body.String()
	if !strings.Contains(body, `class="confirm"`) {
		t.Error("the question is not asked before the field that answers it")
	}
	if !strings.Contains(body, `action="/settings/totp/disable"`) {
		t.Fatal("the question has no form to answer it with")
	}
	if !strings.Contains(body, `class="danger"`) {
		t.Error("the control that commits it is not in the danger tone")
	}
	csrf := csrfFieldPattern.FindAllStringSubmatch(body, -1)

	done := postForm(t, srv, "/settings/totp/disable", url.Values{
		"csrf_token": {csrf[len(csrf)-1][1]},
		"password":   {testPassword},
	}, cookies)
	if done.Code != http.StatusSeeOther {
		t.Fatalf("turning it off = %d, want %d\n%s", done.Code, http.StatusSeeOther, done.Body.String())
	}

	reloaded, err := store.UserByID(context.Background(), srv.db, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if reloaded.HasTOTP {
		t.Error("two-factor is still on")
	}
	// The codes are minted against the secret, so they go with it rather
	// than outliving it as a way past a factor the account no longer has.
	if left, err := store.CountRecoveryCodes(context.Background(), srv.db, user.ID); err != nil || left != 0 {
		t.Errorf("%d recovery codes survived the secret they were issued against", left)
	}
	// And the session that made the decision still works: every one of them
	// proved both factors when it was granted.
	if rec, _ := getWith(t, srv, "/settings/security", cookies); rec.Code != http.StatusOK {
		t.Errorf("the session that turned it off = %d, want %d", rec.Code, http.StatusOK)
	}
}

// The password is the whole of what stands between a borrowed screen and an
// account with no second factor and no recovery codes.
func TestTurningTwoFactorOffNeedsThePassword(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "player@example.tld", false)

	_, cookies := login(t, srv, "player@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)
	csrf, cookies := getCSRF(t, srv, "/settings/security?confirm=totp", cookies)

	for _, password := range []string{"", "not-the-password"} {
		rec := postForm(t, srv, "/settings/totp/disable", url.Values{
			"csrf_token": {csrf},
			"password":   {password},
		}, cookies)
		if rec.Code == http.StatusSeeOther {
			t.Errorf("password %q: two-factor was turned off", password)
		}
		reloaded, _ := store.UserByID(context.Background(), srv.db, user.ID)
		if !reloaded.HasTOTP {
			t.Fatalf("password %q: two-factor is off", password)
		}
	}
}

// The admin area is gated on two-factor, so an admin who could switch it off
// would make "required for admins" a suggestion. The screen does not offer
// it, and the route does not allow it either — the template decides what is
// offered and the handler decides what is allowed, and only one of those is
// reachable by typing a URL.
func TestAnAdminCannotTurnTwoFactorOff(t *testing.T) {
	srv := testServer(t)
	user := seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)

	page, cookies := getWith(t, srv, "/settings/security?confirm=totp", cookies)
	body := page.Body.String()
	if strings.Contains(body, "/settings/totp/disable") {
		t.Error("the security screen offers an admin a way to turn two-factor off")
	}
	// The replacement is still offered: an admin changing phones is the
	// ordinary case, and it is the same enrolment everybody else gets.
	if !strings.Contains(body, `href="/enroll-totp"`) {
		t.Error("an admin is not offered a way to rotate their secret")
	}

	csrf := csrfFieldPattern.FindStringSubmatch(body)[1]
	rec := postForm(t, srv, "/settings/totp/disable", url.Values{
		"csrf_token": {csrf},
		"password":   {testPassword},
	}, cookies)
	if rec.Code != http.StatusForbidden {
		t.Errorf("posting it anyway = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if reloaded, _ := store.UserByID(context.Background(), srv.db, user.ID); !reloaded.HasTOTP {
		t.Error("an admin turned their two-factor off")
	}
}
