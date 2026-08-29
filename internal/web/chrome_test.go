package web

import (
	"context"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// Every page carries the theme and locale, because they live on <html>.
// A page that forgot them would render untranslated and unthemed.
func TestEveryPageCarriesThemeAndLocale(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	pages := []struct {
		path    string
		cookie  *http.Cookie
		wantErr bool
	}{
		{path: "/"},
		{path: "/forgot-password"},
		{path: "/share/" + slug + "/"},
		{path: "/share/" + slug + "/p/harda"},
		{path: "/share/" + slug + "/today"},
		{path: "/share/" + slug + "/months"},
		{path: "/leaderboard", cookie: session},
		{path: "/p/harda", cookie: session},
		{path: "/today", cookie: session},
		{path: "/months", cookie: session},
		{path: "/admin/players", cookie: session},
		{path: "/admin/players/harda", cookie: session},
		{path: "/no/such/page", wantErr: true},
	}

	for _, p := range pages {
		rec := fetchAs(t, srv, p.path, p.cookie)

		// Assert the status first. A template that fails to execute
		// returns 500 with a plain body, and checking only for the
		// attribute reports that as "no data-theme" — which sends you
		// looking in the wrong place.
		wantStatus := http.StatusOK
		if p.wantErr {
			wantStatus = http.StatusNotFound
		}
		if rec.Code != wantStatus {
			t.Errorf("%s: status = %d, want %d", p.path, rec.Code, wantStatus)
			continue
		}

		body := rec.Body.String()
		if !strings.Contains(body, `data-theme="system"`) {
			t.Errorf("%s: no data-theme on <html>", p.path)
		}
		if !strings.Contains(body, `lang="en"`) {
			t.Errorf("%s: no lang on <html>", p.path)
		}
	}
}

// The theme and language pickers are both <details> in the top bar. Without
// a shared name they open independently, so opening one leaves the other
// open too — the browser only enforces "one at a time" for <details> that
// share a name attribute.
func TestTopbarPickersAreMutuallyExclusive(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	if got := strings.Count(body, `<details class="menu" name="topbar-menu">`); got != 2 {
		t.Errorf("expected both the theme and language pickers to share name=\"topbar-menu\", found %d", got)
	}
}

func TestThemeChoiceIsRememberedAndApplied(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/board?theme=light", nil)
	if !strings.Contains(rec.Body.String(), `data-theme="light"`) {
		t.Error("?theme=light did not apply")
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == themeCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("choosing a theme set no cookie")
	}
	if got := fetchAs(t, srv, "/share/"+slug+"/", cookie).Body.String(); !strings.Contains(got, `data-theme="light"`) {
		t.Error("the remembered theme was not applied on a later request")
	}

	// A value that is not one of the three is ignored rather than written
	// through to the attribute.
	bad := fetchAs(t, srv, "/share/"+slug+"/board?theme=neon", nil).Body.String()
	if !strings.Contains(bad, `data-theme="system"`) {
		t.Error("an unknown theme was not rejected")
	}
}

func TestLanguageSwitcherChangesTheCopy(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	en := fetchAs(t, srv, "/share/"+slug+"/board", nil).Body.String()
	if !strings.Contains(en, "Not ranked") {
		t.Fatal("the English board changed")
	}

	sv := fetchAs(t, srv, "/share/"+slug+"/board?lang=sv", nil)
	body := sv.Body.String()
	if !strings.Contains(body, `lang="sv"`) {
		t.Error("the document language did not change")
	}
	if !strings.Contains(body, "Ej rankade") {
		t.Error("the board copy is still English under ?lang=sv")
	}
	if strings.Contains(body, "Not ranked") {
		t.Error("English copy survived the switch")
	}
}

// Switching one setting must not discard the other, or the board's filters.
// The account menu is for people with accounts.
func TestAccountMenuOnlyForSignedInUsers(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	ctx := context.Background()

	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	ordinary, err := store.CreateUser(ctx, srv.db, store.AdminActor(admin.ID), "player@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	shared := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	if strings.Contains(shared, "account-menu") {
		t.Error("the shared board offers an account menu")
	}
	// No badge: the sign-in button is what marks the view as read-only, and
	// two things saying it was one too many.
	if strings.Contains(shared, "account-menu") {
		t.Error("the shared board offers an account menu")
	}

	as := func(u store.User) string {
		return fetchAs(t, srv, "/leaderboard", signIn(t, srv, u.ID)).Body.String()
	}
	adminBody := as(admin)
	if !strings.Contains(adminBody, "account-menu") {
		t.Fatal("no account menu for a signed-in admin")
	}
	if !strings.Contains(adminBody, "admin@example.tld") {
		t.Error("the account menu does not show which account is signed in")
	}
	if !strings.Contains(adminBody, `href="/admin/players"`) {
		t.Error("an admin has no link to the admin area")
	}

	if body := as(ordinary); strings.Contains(body, `href="/admin/players"`) {
		t.Error("a non-admin is offered the admin area")
	}
}

// The menu opens with no JavaScript at all, which is the reason it is a
// <details> rather than a button.
func TestAccountMenuNeedsNoScript(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")

	body := fetchAs(t, srv, "/leaderboard", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(body, "<details class=\"account\">") {
		t.Error("the account menu is not a details element")
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "onclick") {
		t.Error("the page carries script")
	}
}

// The link lives once in "base", so this is really a test that every page
// renders through it — signed out, signed in, admin, and the read-only
// share view alike — rather than a test of the link itself.
func TestEveryPageHasTheGitHubFooterLink(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	const want = `href="https://github.com/martinstenrose/wordleland"`

	for _, p := range []struct {
		path   string
		cookie *http.Cookie
	}{
		{path: "/"},
		{path: "/share/" + slug + "/"},
		{path: "/leaderboard", cookie: session},
		{path: "/admin/players", cookie: session},
		{path: "/no/such/page"},
	} {
		body := fetchAs(t, srv, p.path, p.cookie).Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("%s: no GitHub footer link", p.path)
		}
	}
}

func TestInitialsFor(t *testing.T) {
	for _, tt := range []struct{ email, want string }{
		{"martin@example.tld", "M"},
		{"martin.stenrose@example.tld", "MS"},
		{"a-b-c@example.tld", "AB"},
		{"first+tag@example.tld", "FT"},
		{"7up@example.tld", "7"},
		{"@example.tld", "?"},
		{"åke@example.tld", "Å"},
	} {
		if got := initialsFor(tt.email); got != tt.want {
			t.Errorf("initialsFor(%q) = %q, want %q", tt.email, got, tt.want)
		}
	}
}

// html/template does not check nesting, so a stray closing tag renders
// happily and only shows up as a broken layout in a browser. These are the
// container elements the templates always close explicitly.
func TestRenderedPagesHaveBalancedContainers(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	pages := []struct {
		path   string
		cookie *http.Cookie
	}{
		{path: "/"},
		{path: "/forgot-password"},
		{path: "/share/" + slug + "/"},
		{path: "/share/" + slug + "/today"},
		{path: "/share/" + slug + "/today?benched=1"},
		{path: "/share/" + slug + "/months"},
		{path: "/share/" + slug + "/grid"},
		{path: "/share/" + slug + "/p/harda"},
		{path: "/share/" + slug + "/p/thin"},
		{path: "/leaderboard", cookie: session},
		{path: "/admin/players", cookie: session},
		{path: "/admin/players/harda", cookie: session},
	}

	tags := []string{"div", "section", "table", "thead", "tbody", "tr",
		"ul", "ol", "li", "aside", "nav", "header", "details", "form", "dl", "svg"}

	for _, p := range pages {
		body := fetchAs(t, srv, p.path, p.cookie).Body.String()
		for _, tag := range tags {
			open := strings.Count(body, "<"+tag+" ") + strings.Count(body, "<"+tag+">")
			closed := strings.Count(body, "</"+tag+">")
			if open != closed {
				t.Errorf("%s: <%s> opened %d times, closed %d", p.path, tag, open, closed)
			}
		}
	}
}

// hrefOfClass returns the href of the first element carrying a class.
func hrefOfClass(t *testing.T, body, class string) string {
	t.Helper()
	i := strings.Index(body, `class="`+class+`"`)
	if i < 0 {
		t.Fatalf("no element with class %q on the page", class)
	}
	rest := body[i:]
	j := strings.Index(rest, `href="`)
	if j < 0 {
		t.Fatalf("element with class %q has no href", class)
	}
	rest = rest[j+len(`href="`):]
	return html.UnescapeString(rest[:strings.Index(rest, `"`)])
}

// The theme menu offers all three settings, marks the one in force, and
// each row applies it.
func TestThemeMenuOffersAllThree(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	for _, label := range []string{"Light", "Dark", "System"} {
		href := hrefFor(t, body, label)
		href = strings.ReplaceAll(href, "&amp;", "&")
		rec := fetchAs(t, srv, href, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", label, rec.Code)
		}
		want := `data-theme="` + strings.ToLower(label) + `"`
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("choosing %s did not apply it", label)
		}
	}

	// System is in force to begin with, and the menu says so.
	if !strings.Contains(body, `class="menu-row on"`) {
		t.Error("the menu does not mark the setting in force")
	}
}

// Switching one setting keeps the rest of the query, filters included.
func TestPickersPreserveTheRestOfTheQuery(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board?mode=hard&lang=sv", nil).Body.String()
	dark := strings.ReplaceAll(hrefFor(t, body, "Mörkt"), "&amp;", "&")
	for _, want := range []string{"mode=hard", "lang=sv", "theme=dark"} {
		if !strings.Contains(dark, want) {
			t.Errorf("the dark link %q dropped %q", dark, want)
		}
	}

	next := fetchAs(t, srv, dark, nil).Body.String()
	if !strings.Contains(next, `data-theme="dark"`) {
		t.Error("following the link did not change the theme")
	}
	if !strings.Contains(next, "dolda") {
		t.Error("following the link lost the hard-mode filter")
	}
}

// The language picker is its own menu in the bar, on every surface. What
// it changes for a signed-in reader is their account, not just this
// browser — see TestSettingsLanguagePersistsToTheAccount.
func TestLanguagePickerIsAvailableEverywhere(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	board := fetchAs(t, srv, "/leaderboard", session).Body.String()
	bar := board[strings.Index(board, `class="topbar`):]
	bar = bar[:strings.Index(bar, "</header>")]
	if !strings.Contains(bar, "lang=sv") {
		t.Error("no language picker when signed in")
	}
	if !strings.Contains(bar, "theme=") {
		t.Error("the top bar lost the theme picker")
	}

	// Settings does not carry a second one: two doors to one setting. The
	// bar is on that page too, so look below it.
	settings := fetchAs(t, srv, "/settings", session).Body.String()
	if body := settings[strings.Index(settings, "</header>"):]; strings.Contains(body, "lang=sv") {
		t.Error("Settings offers a second language control")
	}

	shared := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	if strings.Contains(shared, "account-menu") {
		t.Fatal("the shared board grew an account menu")
	}
	if !strings.Contains(shared, "lang=sv") {
		t.Error("the shared board has no way to change language")
	}
	// And it offers the way in.
	if !strings.Contains(shared, "Sign in") {
		t.Error("the shared board does not offer sign-in")
	}
}

// The shared bar carries what the design gives it: the read-only badge, the
// note, and a sign-in button rather than a bare link.
func TestSharedBarOffersSignIn(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	bar := body[strings.Index(body, "topbar-controls"):strings.Index(body, "</header>")]

	for _, want := range []string{`class="btn-primary"`, "Sign in", "<svg"} {
		if !strings.Contains(bar, want) {
			t.Errorf("the shared bar is missing %q", want)
		}
	}
	if !strings.Contains(bar, `href="/"`) {
		t.Error("the sign-in button does not point at the login page")
	}

	// Signed in, there is nothing to sign in to.
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	in := fetchAs(t, srv, "/today", signIn(t, srv, admin.ID)).Body.String()
	if strings.Contains(in[:strings.Index(in, "</header>")], "btn-primary") {
		t.Error("a signed-in reader is offered a sign-in button")
	}
}
