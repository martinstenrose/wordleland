package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

// settingsUser makes a signed-in reader with a known password.
func settingsUser(t *testing.T, srv *Server, email, password string, admin bool) (store.User, *http.Cookie) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user, err := store.CreateUser(context.Background(), srv.db, store.SystemActor(), email, hash, admin)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user, signIn(t, srv, user.ID)
}

func postSettings(t *testing.T, srv *Server, path string, form url.Values, session *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	get := fetchAs(t, srv, "/settings", session)
	if get.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", get.Code)
	}
	var csrf *http.Cookie
	for _, c := range get.Result().Cookies() {
		if c.Name == csrfCookieName {
			csrf = c
		}
	}
	if csrf == nil {
		t.Fatal("no CSRF cookie")
	}
	form.Set(csrfFieldName, csrf.Value)

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = clientAddr(t)
	req.AddCookie(session)
	req.AddCookie(csrf)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSettingsShowsTheAccount(t *testing.T) {
	srv := testServer(t)
	user, session := settingsUser(t, srv, "reader@example.tld", "correct horse battery staple", false)

	body := fetchAs(t, srv, "/settings", session).Body.String()
	if !strings.Contains(body, user.Email) {
		t.Error("settings does not name the account")
	}
	if !strings.Contains(body, "Player") {
		t.Error("settings does not show the role")
	}
	// No player linked yet, so there is no name to change and the page says
	// so rather than offering a field that would go nowhere.
	if !strings.Contains(body, "No player is linked") {
		t.Error("settings does not explain the missing name field")
	}
}

// The display name belongs to the player, so a reader with one can rename
// themselves and it shows on the board.
func TestSettingsRenamesTheLinkedPlayer(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()

	user, session := settingsUser(t, srv, "harda@example.tld", "correct horse battery staple", false)
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if _, err := store.LinkPlayer(ctx, srv.db, store.AdminActor(admin.ID), player.ID, &user.ID); err != nil {
		t.Fatalf("LinkPlayer: %v", err)
	}

	rec := postSettings(t, srv, "/settings/name", url.Values{"name": {"Renamed"}}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d; body:\n%s", rec.Code, rec.Body.String())
	}

	after, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if after.Name != "Renamed" {
		t.Errorf("name = %q, want %q", after.Name, "Renamed")
	}
}

// The current password is required even though the session is already
// authenticated: a borrowed screen should not be enough to take the account.
func TestSettingsPasswordRequiresTheCurrentOne(t *testing.T) {
	srv := testServer(t)
	const old = "correct horse battery staple"
	user, session := settingsUser(t, srv, "reader@example.tld", old, false)

	wrong := postSettings(t, srv, "/settings/password", url.Values{
		"current": {"not the password"}, "password": {"a brand new passphrase"},
	}, session)
	if wrong.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong current password = %d, want 422", wrong.Code)
	}
	if !strings.Contains(wrong.Body.String(), "not your current password") {
		t.Error("the rejection does not say why")
	}

	fresh, _ := store.UserByID(context.Background(), srv.db, user.ID)
	if err := auth.VerifyPassword(fresh.PasswordHash, old); err != nil {
		t.Error("the password changed despite the wrong current one")
	}
}

func TestSettingsChangesThePassword(t *testing.T) {
	srv := testServer(t)
	const old, next = "correct horse battery staple", "an entirely different one"
	user, session := settingsUser(t, srv, "reader@example.tld", old, false)

	rec := postSettings(t, srv, "/settings/password", url.Values{
		"current": {old}, "password": {next},
	}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("change = %d; body:\n%s", rec.Code, rec.Body.String())
	}

	fresh, _ := store.UserByID(context.Background(), srv.db, user.ID)
	if err := auth.VerifyPassword(fresh.PasswordHash, next); err != nil {
		t.Error("the new password does not verify")
	}
	// Every session ends, this one included, so the reader is not left on a
	// page they can no longer act on.
	if got := fetchAs(t, srv, "/settings", session).Code; got != http.StatusSeeOther {
		t.Errorf("the old session still works: %d", got)
	}
}

func TestSettingsPasswordHasAFloor(t *testing.T) {
	srv := testServer(t)
	const old = "correct horse battery staple"
	_, session := settingsUser(t, srv, "reader@example.tld", old, false)

	rec := postSettings(t, srv, "/settings/password", url.Values{
		"current": {old}, "password": {"short"},
	}, session)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("short password = %d, want 422", rec.Code)
	}
}

// A new address waits to be confirmed. Sign-in keeps using the old one, so
// a typo cannot lock somebody out of their own account.
func TestSettingsEmailChangeWaitsForConfirmation(t *testing.T) {
	srv := testServer(t)
	user, session := settingsUser(t, srv, "old@example.tld", "correct horse battery staple", false)

	var sent []byte
	var to []string
	srv.mailer = auth.NewMailer("smtp.example.tld", "587", "", "", "wordle@example.tld")
	srv.mailer.SetSender(func(_ string, _ smtp.Auth, _ string, rcpt []string, msg []byte) error {
		to, sent = rcpt, msg
		return nil
	})
	srv.cfg.AppURL = "https://wordle.example.tld"

	rec := postSettings(t, srv, "/settings/email", url.Values{"email": {"new@example.tld"}}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("change = %d; body:\n%s", rec.Code, rec.Body.String())
	}

	// The confirmation goes to the new address: a message to the old one
	// would prove nothing about the new one.
	if len(to) != 1 || to[0] != "new@example.tld" {
		t.Errorf("confirmation went to %v, want the new address", to)
	}

	fresh, _ := store.UserByID(context.Background(), srv.db, user.ID)
	if fresh.Email != "old@example.tld" {
		t.Errorf("sign-in address changed to %q before confirmation", fresh.Email)
	}
	if fresh.PendingEmail == nil || *fresh.PendingEmail != "new@example.tld" {
		t.Errorf("PendingEmail = %v", fresh.PendingEmail)
	}

	// Following the link promotes it.
	link := string(sent)
	i := strings.Index(link, "https://wordle.example.tld/verify-email?token=")
	if i < 0 {
		t.Fatal("no verification link in the message")
	}
	token := link[i+len("https://wordle.example.tld/verify-email?token="):]
	token = strings.Fields(token)[0]

	if got := fetchAs(t, srv, "/verify-email?token="+token, nil).Code; got != http.StatusOK {
		t.Fatalf("verify = %d", got)
	}
	fresh, _ = store.UserByID(context.Background(), srv.db, user.ID)
	if fresh.Email != "new@example.tld" {
		t.Errorf("address = %q after confirmation, want the new one", fresh.Email)
	}
	if fresh.PendingEmail != nil {
		t.Errorf("PendingEmail = %v after confirmation, want nil", fresh.PendingEmail)
	}
}

// Saying "that address is taken" would tell whoever asks who else is on the
// board, so a taken address reads the same as an unusable one.
func TestSettingsEmailDoesNotRevealOtherAccounts(t *testing.T) {
	srv := testServer(t)
	_, session := settingsUser(t, srv, "one@example.tld", "correct horse battery staple", false)
	if _, err := store.CreateUser(context.Background(), srv.db, store.SystemActor(),
		"two@example.tld", "hash", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	srv.mailer = auth.NewMailer("smtp.example.tld", "587", "", "", "wordle@example.tld")
	srv.mailer.SetSender(func(string, smtp.Auth, string, []string, []byte) error { return nil })

	taken := postSettings(t, srv, "/settings/email", url.Values{"email": {"two@example.tld"}}, session)
	unusable := postSettings(t, srv, "/settings/email", url.Values{"email": {"not-an-address"}}, session)

	if taken.Code != unusable.Code {
		t.Errorf("statuses differ: taken %d, unusable %d", taken.Code, unusable.Code)
	}
	if !strings.Contains(taken.Body.String(), "cannot be used") {
		t.Error("a taken address is reported distinctly")
	}
}

// Choosing a language writes it to the account, not just to a cookie: it
// is what the reader's mail is written in, and it should follow them to a
// second device. The control is the top bar's picker; this is about where
// the choice lands, not where the control is.
func TestLanguageChoicePersistsToTheAccount(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	if rec := fetchAs(t, srv, "/settings?lang=sv", session); rec.Code != http.StatusOK {
		t.Fatalf("settings = %d", rec.Code)
	}

	after, err := store.UserByID(ctx, srv.db, admin.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if after.Locale != "sv" {
		t.Fatalf("Locale = %q, want it stored on the account", after.Locale)
	}

	// A fresh session with no language cookie still reads in Swedish,
	// which is the point of storing it rather than remembering the browser.
	clean := signIn(t, srv, admin.ID)
	body := fetchAs(t, srv, "/today", clean).Body.String()
	if !strings.Contains(body, `lang="sv"`) {
		t.Error("a new session did not pick up the stored language")
	}
}

// An unknown language is ignored rather than stored, or it would mean the
// fallback forever with nothing to show it had gone wrong.
func TestUnknownLanguageIsIgnored(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	fetchAs(t, srv, "/settings?lang=kl", session)

	after, _ := store.UserByID(ctx, srv.db, admin.ID)
	if after.Locale != "en" {
		t.Errorf("Locale = %q, want it left alone", after.Locale)
	}
}
