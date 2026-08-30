package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// adminSession returns a signed-in admin and their cookie.
func adminSession(t *testing.T, srv *Server) (store.User, *http.Cookie) {
	t.Helper()
	admin, err := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	return admin, signIn(t, srv, admin.ID)
}

// postAdmin submits the form, carrying the CSRF cookie and token the page
// itself issued.
func postAdmin(t *testing.T, srv *Server, path string, form url.Values, session *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	// The token comes from a page that renders a form, which is not always
	// the path being posted to: an action like /invite is POST-only.
	source := path
	for _, action := range []string{"/invite", "/assign", "/discard"} {
		if i := strings.Index(path, action); i > 0 {
			source = path[:i]
		}
	}
	get := fetchAs(t, srv, source, session)
	if get.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", source, get.Code)
	}
	var csrf *http.Cookie
	for _, c := range get.Result().Cookies() {
		if c.Name == csrfCookieName {
			csrf = c
		}
	}
	if csrf == nil {
		t.Fatalf("GET %s issued no CSRF cookie", path)
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

// The admin area is for admins. A signed-in ordinary user gets 404 rather
// than 403: there is nothing to tell them about.
func TestAdminAreaIsAdminOnly(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()

	admin, adminCookie := adminSession(t, srv)
	ordinary, err := store.CreateUser(ctx, srv.db, store.AdminActor(admin.ID),
		"player@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	for _, path := range []string{"/admin/players", "/admin/players/harda"} {
		if got := fetchAs(t, srv, path, nil).Code; got != http.StatusSeeOther {
			t.Errorf("anonymous GET %s = %d, want a redirect to login", path, got)
		}
		if got := fetchAs(t, srv, path, signIn(t, srv, ordinary.ID)).Code; got != http.StatusNotFound {
			t.Errorf("non-admin GET %s = %d, want 404", path, got)
		}
		if got := fetchAs(t, srv, path, adminCookie).Code; got != http.StatusOK {
			t.Errorf("admin GET %s = %d, want 200", path, got)
		}
	}
}

func TestAdminEditsPlayerNameAndSlug(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	rec := postAdmin(t, srv, "/admin/players/harda", url.Values{
		"name": {"Renamed"}, "slug": {"renamed"}, "active": {"1"},
	}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d, want a redirect; body:\n%s", rec.Code, rec.Body.String())
	}

	player, err := store.PlayerBySlug(context.Background(), srv.db, "renamed")
	if err != nil {
		t.Fatalf("PlayerBySlug after rename: %v", err)
	}
	if player.Name != "Renamed" {
		t.Errorf("name = %q, want %q", player.Name, "Renamed")
	}
	if !player.Active {
		t.Error("the player was retired by an edit that did not ask for it")
	}
}

// Clearing the box retires the player and keeps their history — the schema's
// membership flag, not a delete.
func TestAdminRetiresPlayerWithoutLosingHistory(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	ctx := context.Background()

	before, err := store.ResultsForBoard(ctx, srv.db)
	if err != nil {
		t.Fatalf("ResultsForBoard: %v", err)
	}

	rec := postAdmin(t, srv, "/admin/players/harda", url.Values{
		"name": {"Harda"}, "slug": {"harda"},
	}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d; body:\n%s", rec.Code, rec.Body.String())
	}

	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if player.Active {
		t.Error("clearing the box did not retire the player")
	}
	after, _ := store.ResultsForBoard(ctx, srv.db)
	if len(after) != len(before) {
		t.Errorf("results went from %d to %d; retiring must not delete history",
			len(before), len(after))
	}
}

func TestAdminRejectsDuplicateSlug(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	rec := postAdmin(t, srv, "/admin/players/harda", url.Values{
		"name": {"Harda"}, "slug": {"hardb"}, "active": {"1"},
	}, session)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("save with a taken slug = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already uses that slug") {
		t.Error("the rejection does not say why")
	}
	// The submitted values come back, so the edit is not silently lost.
	if !strings.Contains(rec.Body.String(), `value="hardb"`) {
		t.Error("the rejected form did not keep what was typed")
	}
	if player, _ := store.PlayerBySlug(context.Background(), srv.db, "harda"); player.Slug != "harda" {
		t.Error("a rejected edit changed the stored slug anyway")
	}
}

func TestAdminRejectsInvalidSlug(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	rec := postAdmin(t, srv, "/admin/players/harda", url.Values{
		"name": {"Harda"}, "slug": {"Not A Slug"}, "active": {"1"},
	}, session)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("save with a bad slug = %d, want 422", rec.Code)
	}
}

func TestAdminLinksAndUnlinksALogin(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, srv.db, store.AdminActor(admin.ID),
		"harda@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	base := url.Values{"name": {"Harda"}, "slug": {"harda"}, "active": {"1"}}
	linked := url.Values{}
	for k, v := range base {
		linked[k] = v
	}
	linked.Set("user_id", strconv.FormatInt(user.ID, 10))

	if rec := postAdmin(t, srv, "/admin/players/harda", linked, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("link = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if player.UserID == nil || *player.UserID != user.ID {
		t.Fatalf("player.UserID = %v, want %d", player.UserID, user.ID)
	}

	// Submitting the field empty — "No login" — detaches it again.
	cleared := url.Values{}
	for k, v := range base {
		cleared[k] = v
	}
	cleared.Set("user_id", "")
	if rec := postAdmin(t, srv, "/admin/players/harda", cleared, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("unlink = %d", rec.Code)
	}
	player, _ = store.PlayerBySlug(ctx, srv.db, "harda")
	if player.UserID != nil {
		t.Errorf("player.UserID = %v, want nil after unlinking", player.UserID)
	}
}

// The link control is hidden until there is a screen for managing users,
// so the edit form no longer carries user_id at all. An absent field must
// mean "leave the link alone" — read as an empty selection it would unlink
// every player somebody merely renamed.
func TestSavingWithoutTheLinkFieldKeepsTheLogin(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	user := seedLogin(t, srv, "player@example.tld", false)

	base := url.Values{"name": {"Harda"}, "slug": {"harda"}, "active": {"1"}}
	linked := url.Values{"user_id": {strconv.FormatInt(user.ID, 10)}}
	for k, v := range base {
		linked[k] = v
	}
	if rec := postAdmin(t, srv, "/admin/players/harda", linked, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("link = %d", rec.Code)
	}

	// A rename, submitted by the form as it now stands: no user_id.
	renamed := url.Values{"name": {"Renamed"}, "slug": {"harda"}, "active": {"1"}}
	if rec := postAdmin(t, srv, "/admin/players/harda", renamed, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d", rec.Code)
	}

	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if player.Name != "Renamed" {
		t.Errorf("player.Name = %q, want the rename to have applied", player.Name)
	}
	if player.UserID == nil || *player.UserID != user.ID {
		t.Fatalf("player.UserID = %v; the rename unlinked the login", player.UserID)
	}
}

// And the control really is gone from the page.
func TestPlayerEditorHidesTheLinkPicker(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/players/harda", session).Body.String()
	if strings.Contains(body, `name="user_id"`) {
		t.Error("the editor still offers a login picker")
	}
	// The rest of the editor is untouched.
	for _, want := range []string{`name="name"`, `name="slug"`, `name="active"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the editor lost %s", want)
		}
	}
}

// players.user_id is UNIQUE, so one login belongs to one player. The second
// attempt has to say so rather than fail opaquely.
func TestAdminRefusesALoginLinkedElsewhere(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)
	ctx := context.Background()

	user, _ := store.CreateUser(ctx, srv.db, store.AdminActor(admin.ID), "one@example.tld", "hash", false)
	form := url.Values{"name": {"Harda"}, "slug": {"harda"}, "active": {"1"},
		"user_id": {strconv.FormatInt(user.ID, 10)}}
	if rec := postAdmin(t, srv, "/admin/players/harda", form, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("first link = %d", rec.Code)
	}

	second := url.Values{"name": {"Hardb"}, "slug": {"hardb"}, "active": {"1"},
		"user_id": {strconv.FormatInt(user.ID, 10)}}
	rec := postAdmin(t, srv, "/admin/players/hardb", second, session)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second link = %d, want 422; body:\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already linked to another player") {
		t.Error("the rejection does not explain the conflict")
	}
}

// Every write goes through the store's actor, so the audit log records who
// made the change.
func TestAdminEditIsAudited(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)
	ctx := context.Background()

	var before int
	if err := srv.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`).Scan(&before); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}

	if rec := postAdmin(t, srv, "/admin/players/harda", url.Values{
		"name": {"Audited"}, "slug": {"harda"}, "active": {"1"},
	}, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}

	var after int
	var actor *int64
	if err := srv.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`).Scan(&after); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	if after <= before {
		t.Fatal("the edit wrote no audit entry")
	}
	if err := srv.db.QueryRowContext(ctx,
		`SELECT actor_user_id FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&actor); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	if actor == nil || *actor != admin.ID {
		t.Errorf("audit actor = %v, want the signed-in admin %d", actor, admin.ID)
	}
}

func TestAdminSubmitWithoutCSRFIsRejected(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	form := url.Values{"name": {"NoToken"}, "slug": {"harda"}, "active": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/players/harda", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = clientAddr(t)
	req.AddCookie(session)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusSeeOther {
		t.Fatal("a submission with no CSRF token was accepted")
	}
	if player, _ := store.PlayerBySlug(context.Background(), srv.db, "harda"); player.Name == "NoToken" {
		t.Error("the change was applied despite the missing token")
	}
}

func TestAdminUnknownPlayerIs404(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	if got := fetchAs(t, srv, "/admin/players/nobody", session).Code; got != http.StatusNotFound {
		t.Errorf("GET an unknown player = %d, want 404", got)
	}
}

// The roster list is the way in, so it has to show what an admin is picking
// between.
func TestAdminListShowsRosterState(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/players", session).Body.String()
	for _, want := range []string{"harda", "normala", "lapsed", "Puzzles"} {
		if !strings.Contains(body, want) {
			t.Errorf("the roster list is missing %q", want)
		}
	}
	if !strings.Contains(body, `href="/admin/players/harda"`) {
		t.Error("the list does not link to the edit form")
	}
}

// The roster and the editor share one page: choosing a player is a link, so
// nothing here needs script.
func TestAdminListAndEditorAreOnePage(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	list := fetchAs(t, srv, "/admin/players", session).Body.String()
	if !strings.Contains(list, "admin-split") {
		t.Fatal("the roster page has no two-pane layout")
	}
	// Nothing chosen yet, so the panel says what to do rather than editing
	// somebody arbitrary.
	if !strings.Contains(list, "Choose a player") {
		t.Error("the empty panel does not prompt")
	}
	if strings.Contains(list, `id="adm-name"`) {
		t.Error("the editor is open with no player chosen")
	}

	// Following a row opens the panel on that player, list still present.
	sel := fetchAs(t, srv, "/admin/players/harda", session).Body.String()
	if !strings.Contains(sel, `id="adm-name"`) {
		t.Error("following a row did not open the editor")
	}
	if !strings.Contains(sel, "admin-list") {
		t.Error("the list disappeared when a player was chosen")
	}
	if !strings.Contains(sel, `class="admin-row on"`) {
		t.Error("the chosen row is not marked")
	}
}

// The counts under the heading are what the design puts there.
func TestAdminPageCountsLogins(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, srv.db, store.AdminActor(admin.ID), "one@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if _, err := store.LinkPlayer(ctx, srv.db, store.AdminActor(admin.ID), player.ID, &user.ID); err != nil {
		t.Fatalf("LinkPlayer: %v", err)
	}

	body := fetchAs(t, srv, "/admin/players", session).Body.String()
	if !strings.Contains(body, "1 with a login") {
		t.Errorf("the counts do not report linked players")
	}
}

// The slug is editable, and the field shows the address it forms.
func TestAdminCanChangeASlug(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	page := fetchAs(t, srv, "/admin/players/harda", session).Body.String()
	if !strings.Contains(page, "slug-base") {
		t.Error("the slug field does not show the address it forms")
	}

	rec := postAdmin(t, srv, "/admin/players/harda", url.Values{
		"name": {"Harda"}, "slug": {"harda-two"}, "active": {"1"},
	}, session)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("slug change = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	if _, err := store.PlayerBySlug(context.Background(), srv.db, "harda-two"); err != nil {
		t.Errorf("the slug did not change: %v", err)
	}
}
