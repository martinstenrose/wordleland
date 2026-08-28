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

// invitingServer returns a server whose mail goes to a buffer.
func invitingServer(t *testing.T) (*Server, *[]byte, *[]string) {
	t.Helper()
	srv := testServer(t)
	seedBoard(t, srv)

	var sent []byte
	var to []string
	srv.mailer = auth.NewMailer("smtp.example.tld", "587", "", "", "wordle@example.tld")
	srv.mailer.SetSender(func(_ string, _ smtp.Auth, _ string, rcpt []string, msg []byte) error {
		to, sent = rcpt, msg
		return nil
	})
	srv.cfg.AppURL = "https://wordle.example.tld"
	return srv, &sent, &to
}

// inviteToken sends an invitation and digs the token out of the message.
func inviteToken(t *testing.T, srv *Server, sent *[]byte, session *http.Cookie, slug, email string) string {
	t.Helper()
	rec := postAdmin(t, srv, "/admin/players/"+slug+"/invite",
		url.Values{"invite_email": {email}}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("invite = %d; body:\n%s", rec.Code, rec.Body.String())
	}

	const prefix = "https://wordle.example.tld/invite?token="
	body := string(*sent)
	i := strings.Index(body, prefix)
	if i < 0 {
		t.Fatal("no invitation link in the message")
	}
	return strings.Fields(body[i+len(prefix):])[0]
}

// Accepting creates the account, links it to the player and signs them in —
// all of it, or none.
func TestInvitationClaimsThePlayer(t *testing.T) {
	srv, sent, to := invitingServer(t)
	_, session := adminSession(t, srv)
	ctx := context.Background()

	token := inviteToken(t, srv, sent, session, "harda", "harda@example.tld")
	if len(*to) != 1 || (*to)[0] != "harda@example.tld" {
		t.Errorf("invitation went to %v", *to)
	}

	// Nothing exists yet: an unaccepted invitation leaves no account behind.
	if _, err := store.UserByEmail(ctx, srv.db, "harda@example.tld"); err == nil {
		t.Fatal("an account was created before the invitation was accepted")
	}

	page := fetchAs(t, srv, "/invite?token="+token, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("claim page = %d", page.Code)
	}
	body := page.Body.String()
	if !strings.Contains(body, "Harda") {
		t.Error("the claim page does not name the player")
	}
	if !strings.Contains(body, "harda@example.tld") {
		t.Error("the claim page does not name the address")
	}

	rec := postInvite(t, srv, token, "a perfectly good passphrase", "a perfectly good passphrase")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("claim = %d; body:\n%s", rec.Code, rec.Body.String())
	}

	user, err := store.UserByEmail(ctx, srv.db, "harda@example.tld")
	if err != nil {
		t.Fatalf("no account after claiming: %v", err)
	}
	if user.EmailVerifiedAt == nil {
		t.Error("the address is not marked verified, though following the link proved it")
	}
	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if player.UserID == nil || *player.UserID != user.ID {
		t.Errorf("the player is not linked to the new account: %v", player.UserID)
	}

	// Signed in, rather than asked to type the password they just chose.
	var session2 *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			session2 = c
		}
	}
	if session2 == nil {
		t.Fatal("claiming did not sign them in")
	}
	if got := fetchAs(t, srv, "/settings", session2).Code; got != http.StatusOK {
		t.Errorf("the new session cannot reach settings: %d", got)
	}
}

func postInvite(t *testing.T, srv *Server, token, password, confirm string) *httptest.ResponseRecorder {
	t.Helper()

	get := fetchAs(t, srv, "/invite?token="+token, nil)
	var csrf *http.Cookie
	for _, c := range get.Result().Cookies() {
		if c.Name == csrfCookieName {
			csrf = c
		}
	}
	if csrf == nil {
		t.Fatal("no CSRF cookie on the claim page")
	}

	form := url.Values{
		"token": {token}, "password": {password}, "password_confirm": {confirm},
		csrfFieldName: {csrf.Value},
	}
	req := httptest.NewRequest(http.MethodPost, "/invite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = clientAddr(t)
	req.AddCookie(csrf)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// A token works once. A second use must not hand the player to somebody
// else, or make a second account.
func TestInvitationIsSingleUse(t *testing.T) {
	srv, sent, _ := invitingServer(t)
	_, session := adminSession(t, srv)

	token := inviteToken(t, srv, sent, session, "harda", "harda@example.tld")
	if rec := postInvite(t, srv, token, "a perfectly good passphrase", "a perfectly good passphrase"); rec.Code != http.StatusSeeOther {
		t.Fatalf("first claim = %d", rec.Code)
	}

	again := fetchAs(t, srv, "/invite?token="+token, nil)
	if again.Code != http.StatusBadRequest {
		t.Errorf("a spent token = %d, want 400", again.Code)
	}
	if !strings.Contains(again.Body.String(), "no longer valid") {
		t.Error("the page does not say the invitation is spent")
	}
}

// Re-inviting spends the earlier token, so two live links never point at
// one player.
func TestReinvitingSpendsTheEarlierToken(t *testing.T) {
	srv, sent, _ := invitingServer(t)
	_, session := adminSession(t, srv)

	first := inviteToken(t, srv, sent, session, "harda", "harda@example.tld")
	second := inviteToken(t, srv, sent, session, "harda", "harda2@example.tld")

	if got := fetchAs(t, srv, "/invite?token="+first, nil).Code; got != http.StatusBadRequest {
		t.Errorf("the earlier token still works: %d", got)
	}
	if got := fetchAs(t, srv, "/invite?token="+second, nil).Code; got != http.StatusOK {
		t.Errorf("the newer token = %d, want 200", got)
	}
}

func TestInvitationRejectsBadPasswords(t *testing.T) {
	srv, sent, _ := invitingServer(t)
	_, session := adminSession(t, srv)
	token := inviteToken(t, srv, sent, session, "harda", "harda@example.tld")

	short := postInvite(t, srv, token, "short", "short")
	if short.Code != http.StatusUnprocessableEntity {
		t.Errorf("short password = %d, want 422", short.Code)
	}
	mismatch := postInvite(t, srv, token, "a perfectly good passphrase", "something else entirely")
	if mismatch.Code != http.StatusUnprocessableEntity {
		t.Errorf("mismatch = %d, want 422", mismatch.Code)
	}
	if !strings.Contains(mismatch.Body.String(), "do not match") {
		t.Error("the mismatch is not explained")
	}

	// And after both rejections the invitation is still good.
	if got := fetchAs(t, srv, "/invite?token="+token, nil).Code; got != http.StatusOK {
		t.Errorf("a rejected attempt spent the invitation: %d", got)
	}
}

// A player who already has a login has nothing to claim.
func TestCannotInviteAPlayerWithALogin(t *testing.T) {
	srv, sent, _ := invitingServer(t)
	admin, session := adminSession(t, srv)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, srv.db, store.AdminActor(admin.ID), "taken@example.tld", "hash", false)
	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if _, err := store.LinkPlayer(ctx, srv.db, store.AdminActor(admin.ID), player.ID, &user.ID); err != nil {
		t.Fatalf("LinkPlayer: %v", err)
	}
	_ = sent

	rec := postAdmin(t, srv, "/admin/players/harda/invite",
		url.Values{"invite_email": {"someone@example.tld"}}, session)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("inviting a linked player = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already has a login") {
		t.Error("the rejection does not say why")
	}
}

// An address that already has an account should be linked, not invited.
func TestCannotInviteAnExistingAccount(t *testing.T) {
	srv, _, _ := invitingServer(t)
	admin, session := adminSession(t, srv)

	if _, err := store.CreateUser(context.Background(), srv.db, store.AdminActor(admin.ID),
		"already@example.tld", "hash", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rec := postAdmin(t, srv, "/admin/players/harda/invite",
		url.Values{"invite_email": {"already@example.tld"}}, session)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("= %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already has an account") {
		t.Error("the rejection does not point at linking instead")
	}
}

// The panel shows an outstanding invitation rather than looking as though
// nothing has happened.
func TestAdminPanelShowsAPendingInvitation(t *testing.T) {
	srv, sent, _ := invitingServer(t)
	_, session := adminSession(t, srv)
	inviteToken(t, srv, sent, session, "harda", "harda@example.tld")

	body := fetchAs(t, srv, "/admin/players/harda", session).Body.String()
	if !strings.Contains(body, "Invited harda@example.tld") {
		t.Error("the panel does not show the outstanding invitation")
	}
	if !strings.Contains(body, "Send another invitation") {
		t.Error("the panel does not offer to re-send")
	}
}

// An invited player gets an ordinary account and nothing more. Checked
// through the real claim flow rather than against a hand-made user,
// because the flow is what actually creates them.
func TestInvitedUserIsNotAnAdmin(t *testing.T) {
	ctx := context.Background()
	srv, sent, _ := invitingServer(t)
	_, adminSess := adminSession(t, srv)

	token := inviteToken(t, srv, sent, adminSess, "harda", "newcomer@example.tld")
	if rec := postInvite(t, srv, token, testPassword, testPassword); rec.Code != http.StatusSeeOther {
		t.Fatalf("claim = %d; body:\n%s", rec.Code, rec.Body.String())
	}

	user, err := store.UserByEmail(ctx, srv.db, "newcomer@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if user.IsAdmin {
		t.Fatal("an invited user was created as an admin")
	}

	session := signIn(t, srv, user.ID)

	// Every admin page is a 404 for them: not a 403, since the area is not
	// something they are being told they cannot have.
	for _, path := range []string{
		"/admin/players", "/admin/players/harda",
		"/admin/pending", "/admin/activity", "/admin/activity/1",
	} {
		if got := fetchAs(t, srv, path, session).Code; got != http.StatusNotFound {
			t.Errorf("GET %s as an invited user = %d, want 404", path, got)
		}
	}

	// And the admin writes are refused too, so the gate is not merely on
	// the pages that render the forms.
	for _, path := range []string{
		"/admin/players/harda",
		"/admin/players/harda/invite",
		"/admin/pending/assign",
		"/admin/pending/discard",
	} {
		form := url.Values{"name": {"Hijacked"}, "slug": {"harda"}, "active": {"1"}}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = clientAddr(t)
		req.AddCookie(session)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusSeeOther {
			t.Errorf("POST %s as an invited user = %d; the write was accepted", path, rec.Code)
		}
	}

	// The roster is untouched by all of that.
	player, err := store.PlayerBySlug(ctx, srv.db, "harda")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}
	if player.Name == "Hijacked" {
		t.Error("a non-admin renamed a player")
	}

	// Nor does the account menu offer them the way in.
	body := fetchAs(t, srv, "/today", session).Body.String()
	if strings.Contains(body, `href="/admin/players"`) {
		t.Error("the account menu offers the admin area to a non-admin")
	}
}

// The invitation is written in the language chosen when it was sent, and
// the account it creates starts in that language. Not the admin's: an
// admin reading in Swedish does not make the invitee a Swedish speaker.
func TestInvitationCarriesItsLanguage(t *testing.T) {
	ctx := context.Background()
	srv, sent, _ := invitingServer(t)
	_, session := adminSession(t, srv)

	// The admin is reading in Swedish.
	swedish := postAdmin(t, srv, "/admin/players/harda/invite",
		url.Values{"invite_email": {"harda@example.tld"}, "locale": {"sv"}}, session)
	if swedish.Code != http.StatusSeeOther {
		t.Fatalf("invite = %d", swedish.Code)
	}
	if body := string(*sent); !strings.Contains(body, `lang="sv"`) {
		t.Error("the invitation was not written in the language chosen")
	}

	token := lastInviteToken(t, sent)
	if rec := postInvite(t, srv, token, testPassword, testPassword); rec.Code != http.StatusSeeOther {
		t.Fatalf("claim = %d", rec.Code)
	}
	user, err := store.UserByEmail(ctx, srv.db, "harda@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if user.Locale != "sv" {
		t.Errorf("the account starts in %q, want the invitation's %q", user.Locale, "sv")
	}
}

// No choice, or one nobody has strings for, means English.
func TestInvitationLanguageDefaultsToEnglish(t *testing.T) {
	ctx := context.Background()
	srv, sent, _ := invitingServer(t)
	_, session := adminSession(t, srv)

	for _, locale := range []string{"", "kl"} {
		form := url.Values{"invite_email": {"harda@example.tld"}}
		if locale != "" {
			form.Set("locale", locale)
		}
		if rec := postAdmin(t, srv, "/admin/players/harda/invite", form, session); rec.Code != http.StatusSeeOther {
			t.Fatalf("invite %q = %d", locale, rec.Code)
		}
		if body := string(*sent); !strings.Contains(body, `lang="en"`) {
			t.Errorf("locale %q did not fall back to English", locale)
		}
	}

	token := lastInviteToken(t, sent)
	if rec := postInvite(t, srv, token, testPassword, testPassword); rec.Code != http.StatusSeeOther {
		t.Fatalf("claim = %d", rec.Code)
	}
	user, _ := store.UserByEmail(ctx, srv.db, "harda@example.tld")
	if user.Locale != "en" {
		t.Errorf("the account starts in %q, want English", user.Locale)
	}
}

// lastInviteToken digs the token out of the most recent message.
func lastInviteToken(t *testing.T, sent *[]byte) string {
	t.Helper()
	const prefix = "https://wordle.example.tld/invite?token="
	body := string(*sent)
	i := strings.Index(body, prefix)
	if i < 0 {
		t.Fatal("no invitation link in the message")
	}
	return strings.Fields(body[i+len(prefix):])[0]
}

// Mail goes to the recipient in their language, not in whatever the
// browser that triggered it was set to.
func TestResetEmailUsesTheRecipientsLanguage(t *testing.T) {
	ctx := context.Background()
	srv, sent, _ := invitingServer(t)

	user := seedLogin(t, srv, "reader@example.tld", false)
	if err := store.SetUserLocale(ctx, srv.db, user.ID, "sv"); err != nil {
		t.Fatalf("SetUserLocale: %v", err)
	}

	// Requested from an English browser, for a Swedish reader.
	csrf, cookies := getCSRF(t, srv, "/forgot-password", nil)
	form := url.Values{"email": {"reader@example.tld"}, csrfFieldName: {csrf}}
	if rec := postForm(t, srv, "/forgot-password", form, cookies); rec.Code != http.StatusOK &&
		rec.Code != http.StatusSeeOther {
		t.Fatalf("reset request = %d", rec.Code)
	}
	if body := string(*sent); !strings.Contains(body, `lang="sv"`) {
		t.Error("the reset went out in the requester's language, not the reader's")
	}
}
