package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bytes"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/store"
)

// mailServer returns a server with a working mailer and the messages it sent.
func mailServer(t *testing.T) (*Server, *[]string) {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db, store.Migrations()); err != nil {
		t.Fatalf("store.Migrate() failed: %v", err)
	}

	cfg := &config.Config{
		DBPath:  "test",
		TOTPKey: bytes.Repeat([]byte{0x2a}, 32),
		AppURL:  "https://wordle.example.tld",
		SMTP: config.SMTP{
			Host: "smtp.example.tld", Port: "587", From: "wordle@example.tld",
		},
	}
	srv, err := New(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	var sent []string
	srv.mailer.SetSender(func(_ string, _ smtp.Auth, _ string, _ []string, msg []byte) error {
		sent = append(sent, string(msg))
		return nil
	})
	return srv, &sent
}

var resetLinkPattern = regexp.MustCompile(`https://wordle\.example\.tld/reset-password\?token=([\w-]+)`)

// : with SMTP unconfigured the flow is unavailable and the app runs
// normally. It must not be a 500 or a startup failure.
func TestForgotPasswordWithoutSMTP(t *testing.T) {
	srv := testServer(t) // no SMTP configured

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/forgot-password", nil)
	req.RemoteAddr = clientAddr(t)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "unavailable") {
		t.Errorf("the page does not say the flow is unavailable:\n%s", body)
	}
	// The CLI is the path that always works, so the page points at an admin.
	if !strings.Contains(body, "admin") {
		t.Error("the page does not point at asking an admin")
	}
}

func TestForgotPasswordSubmitWithoutSMTP(t *testing.T) {
	srv := testServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	// The page renders no form when mail is unavailable, so the token comes
	// from elsewhere — which is also what a form left open from before the
	// configuration changed would present.
	csrf, cookies := getCSRF(t, srv, "/", nil)
	rec := postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf},
		"email":      {"martin@example.tld"},
	}, cookies)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM password_reset_tokens`).Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 0 {
		t.Error("a reset token was issued with no way to deliver it")
	}
}

// The response is identical whether or not the address exists, so the
// endpoint does not confirm who has an account.
func TestForgotPasswordDoesNotRevealAccounts(t *testing.T) {
	srv, sent := mailServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	var bodies []string
	for _, email := range []string{"martin@example.tld", "nobody@example.tld"} {
		csrf, cookies := getCSRF(t, srv, "/forgot-password", nil)
		rec := postForm(t, srv, "/forgot-password", url.Values{
			"csrf_token": {csrf},
			"email":      {email},
		}, cookies)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d for %s", rec.Code, email)
		}
		bodies = append(bodies, rec.Body.String())
	}

	if stripCSRF(bodies[0]) != stripCSRF(bodies[1]) {
		t.Error("the response differs between a known and an unknown address")
	}
	// Only the real address produced mail.
	if len(*sent) != 1 {
		t.Errorf("messages sent = %d, want 1", len(*sent))
	}
}

var csrfValuePattern = regexp.MustCompile(`value="[^"]*"`)

func stripCSRF(body string) string { return csrfValuePattern.ReplaceAllString(body, "") }

func TestPasswordResetRoundTrip(t *testing.T) {
	srv, sent := mailServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	csrf, cookies := getCSRF(t, srv, "/forgot-password", nil)
	postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf},
		"email":      {"martin@example.tld"},
	}, cookies)

	if len(*sent) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(*sent))
	}
	match := resetLinkPattern.FindStringSubmatch((*sent)[0])
	if match == nil {
		t.Fatalf("no reset link in the message:\n%s", (*sent)[0])
	}
	token := match[1]

	// The link is built from APP_URL, never the request Host: otherwise a
	// forged header could point the emailed link at another server.
	if !strings.Contains((*sent)[0], "https://wordle.example.tld/reset-password") {
		t.Error("the link was not built from APP_URL")
	}

	const newPassword = "a whole new password"
	csrf, cookies = getCSRF(t, srv, "/reset-password?token="+token, nil)
	rec := postForm(t, srv, "/reset-password", url.Values{
		"csrf_token":       {csrf},
		"token":            {token},
		"password":         {newPassword},
		"password_confirm": {newPassword},
	}, cookies)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d\n%s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}

	reloaded, err := store.UserByID(context.Background(), srv.db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if err := auth.VerifyPassword(reloaded.PasswordHash, newPassword); err != nil {
		t.Errorf("the new password does not verify: %v", err)
	}
}

func TestResetLinkIsSingleUse(t *testing.T) {
	srv, sent := mailServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	csrf, cookies := getCSRF(t, srv, "/forgot-password", nil)
	postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf}, "email": {"martin@example.tld"},
	}, cookies)
	token := resetLinkPattern.FindStringSubmatch((*sent)[0])[1]

	submit := func(password string) *httptest.ResponseRecorder {
		csrf, cookies := getCSRF(t, srv, "/reset-password?token="+token, nil)
		return postForm(t, srv, "/reset-password", url.Values{
			"csrf_token": {csrf}, "token": {token},
			"password": {password}, "password_confirm": {password},
		}, cookies)
	}

	if rec := submit("a whole new password"); rec.Code != http.StatusSeeOther {
		t.Fatalf("first use status = %d", rec.Code)
	}
	rec := submit("another new password")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("second use status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "no longer valid") {
		t.Error("the page does not explain the link is spent")
	}
}

// A reset does not bypass 2FA. An enrolled user still owes a code.
func TestResetDoesNotBypassTOTP(t *testing.T) {
	srv, sent := mailServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	_, cookies := login(t, srv, "admin@example.tld", testPassword)
	enrol(t, srv, cookies)

	csrf, jar := getCSRF(t, srv, "/forgot-password", nil)
	postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf}, "email": {"admin@example.tld"},
	}, jar)
	token := resetLinkPattern.FindStringSubmatch((*sent)[0])[1]

	const newPassword = "a whole new password"
	csrf, jar = getCSRF(t, srv, "/reset-password?token="+token, nil)
	rec := postForm(t, srv, "/reset-password", url.Values{
		"csrf_token": {csrf}, "token": {token},
		"password": {newPassword}, "password_confirm": {newPassword},
	}, jar)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reset status = %d", rec.Code)
	}
	// Sent back to sign in rather than straight through: a reset proves the
	// address, not the second factor.
	// The path, not the whole header: the destination is what matters, and
	// the query carries a confirmation flag that is free to change.
	dest, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || dest.Path != "/" {
		t.Errorf("Location = %q, want the login page", rec.Header().Get("Location"))
	}

	// Signing in with the new password still stops at the second factor.
	after, _ := login(t, srv, "admin@example.tld", newPassword)
	if got := after.Header().Get("Location"); got != "/totp" {
		t.Errorf("Location = %q after reset, want /totp", got)
	}
}

func TestResetInvalidatesSessions(t *testing.T) {
	srv, sent := mailServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	_, live := login(t, srv, "martin@example.tld", testPassword)

	csrf, jar := getCSRF(t, srv, "/forgot-password", nil)
	postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf}, "email": {"martin@example.tld"},
	}, jar)
	token := resetLinkPattern.FindStringSubmatch((*sent)[0])[1]

	const newPassword = "a whole new password"
	csrf, jar = getCSRF(t, srv, "/reset-password?token="+token, nil)
	postForm(t, srv, "/reset-password", url.Values{
		"csrf_token": {csrf}, "token": {token},
		"password": {newPassword}, "password_confirm": {newPassword},
	}, jar)

	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("sessions remaining = %d, want 0", count)
	}

	// The session held before the reset is dead.
	req := httptest.NewRequest(http.MethodGet, "/leaderboard", nil)
	req.RemoteAddr = clientAddr(t)
	for _, c := range live {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("the pre-reset session still works: status %d", rec.Code)
	}
}

func TestResetRejectsMismatchedPasswords(t *testing.T) {
	srv, sent := mailServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	csrf, jar := getCSRF(t, srv, "/forgot-password", nil)
	postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf}, "email": {"martin@example.tld"},
	}, jar)
	token := resetLinkPattern.FindStringSubmatch((*sent)[0])[1]

	csrf, jar = getCSRF(t, srv, "/reset-password?token="+token, nil)
	rec := postForm(t, srv, "/reset-password", url.Values{
		"csrf_token": {csrf}, "token": {token},
		"password": {"a whole new password"}, "password_confirm": {"something else"},
	}, jar)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "do not match") {
		t.Error("the page does not say the passwords differ")
	}
}

func TestResetRejectsShortPassword(t *testing.T) {
	srv, sent := mailServer(t)
	seedLogin(t, srv, "martin@example.tld", false)

	csrf, jar := getCSRF(t, srv, "/forgot-password", nil)
	postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf}, "email": {"martin@example.tld"},
	}, jar)
	token := resetLinkPattern.FindStringSubmatch((*sent)[0])[1]

	csrf, jar = getCSRF(t, srv, "/reset-password?token="+token, nil)
	rec := postForm(t, srv, "/reset-password", url.Values{
		"csrf_token": {csrf}, "token": {token},
		"password": {"short"}, "password_confirm": {"short"},
	}, jar)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// An unknown token is attacker-controlled input. It must be rejected before
// Argon2, or concurrent guesses turn the public endpoint into a memory and CPU
// exhaustion lever.
func TestResetRejectsUnknownTokenBeforeHashing(t *testing.T) {
	srv := testServer(t)
	hashes := 0
	srv.hashPassword = func(string) (string, error) {
		hashes++
		return "hash", nil
	}

	csrf, jar := getCSRF(t, srv, "/reset-password?token=not-a-token", nil)
	rec := postForm(t, srv, "/reset-password", url.Values{
		"csrf_token": {csrf}, "token": {"not-a-token"},
		"password": {"a whole new password"}, "password_confirm": {"a whole new password"},
	}, jar)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if hashes != 0 {
		t.Errorf("password hashes = %d, want 0", hashes)
	}
}

// A link issued before an account was retired is not a way back in.
func TestResetRefusesDisabledAccount(t *testing.T) {
	srv, sent := mailServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	csrf, jar := getCSRF(t, srv, "/forgot-password", nil)
	postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf}, "email": {"martin@example.tld"},
	}, jar)
	token := resetLinkPattern.FindStringSubmatch((*sent)[0])[1]

	if err := store.SetUserDisabled(context.Background(), srv.db, store.SystemActor(), user.ID, true); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}

	const newPassword = "a whole new password"
	csrf, jar = getCSRF(t, srv, "/reset-password?token="+token, nil)
	rec := postForm(t, srv, "/reset-password", url.Values{
		"csrf_token": {csrf}, "token": {token},
		"password": {newPassword}, "password_confirm": {newPassword},
	}, jar)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want the link refused", rec.Code)
	}
}

// A disabled account must not even generate a link.
func TestForgotPasswordSkipsDisabledAccount(t *testing.T) {
	srv, sent := mailServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)
	if err := store.SetUserDisabled(context.Background(), srv.db, store.SystemActor(), user.ID, true); err != nil {
		t.Fatalf("SetUserDisabled() failed: %v", err)
	}

	csrf, jar := getCSRF(t, srv, "/forgot-password", nil)
	rec := postForm(t, srv, "/forgot-password", url.Values{
		"csrf_token": {csrf}, "email": {"martin@example.tld"},
	}, jar)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the same page as any other address", rec.Code)
	}
	if len(*sent) != 0 {
		t.Errorf("messages sent = %d for a disabled account, want 0", len(*sent))
	}
}

func TestEmailVerification(t *testing.T) {
	srv, _ := mailServer(t)
	user := seedLogin(t, srv, "martin@example.tld", false)

	token, err := store.CreateEmailVerificationToken(context.Background(), srv.db, user.ID)
	if err != nil {
		t.Fatalf("CreateEmailVerificationToken() failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/verify-email?token="+token, nil)
	req.RemoteAddr = clientAddr(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	reloaded, err := store.UserByID(context.Background(), srv.db, user.ID)
	if err != nil {
		t.Fatalf("UserByID() failed: %v", err)
	}
	if reloaded.EmailVerifiedAt == nil {
		t.Error("EmailVerifiedAt is still nil")
	}
}

func TestEmailVerificationRejectsBadToken(t *testing.T) {
	srv, _ := mailServer(t)

	req := httptest.NewRequest(http.MethodGet, "/verify-email?token=nonsense", nil)
	req.RemoteAddr = clientAddr(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// The reset email carries both parts. A client that will not render HTML
// still has to receive the link, which is the whole point of the message.
func TestResetEmailIsMultipartAndCarriesTheLink(t *testing.T) {
	srv := testServer(t)
	var sent []byte
	srv.mailer = auth.NewMailer("smtp.example.tld", "587", "", "", "wordle@example.tld")
	srv.mailer.SetSender(func(_ string, _ smtp.Auth, _ string, _ []string, msg []byte) error {
		sent = msg
		return nil
	})
	srv.cfg.AppURL = "https://wordle.example.tld"

	ctx := context.Background()
	if _, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "reader@example.tld", "hash", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := srv.issueResetLink(httptest.NewRequest(http.MethodGet, "/", nil), "reader@example.tld"); err != nil {
		t.Fatalf("issueResetLink: %v", err)
	}

	body := string(sent)
	for _, want := range []string{
		"multipart/alternative",
		"Content-Type: text/plain",
		"Content-Type: text/html",
		"https://wordle.example.tld/reset-password?token=",
		"<!DOCTYPE html>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the message is missing %q", want)
		}
	}
	// The link has to be in the text part too, not only inside an anchor.
	text := body[strings.Index(body, "text/plain"):strings.Index(body, "text/html")]
	if !strings.Contains(text, "https://wordle.example.tld/reset-password?token=") {
		t.Error("the plain-text part has no link")
	}
}
