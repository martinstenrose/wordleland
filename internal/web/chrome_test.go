package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// Every page carries the theme and locale, because they live on <html>.
// A page that forgot them would render untranslated and unthemed.
func TestEveryPageCarriesThemeAndLocale(t *testing.T) {
	t.Parallel()

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
		{path: "/share/" + slug + "/players/harda"},
		{path: "/share/" + slug + "/today"},
		{path: "/share/" + slug + "/months"},
		{path: "/leaderboard", cookie: session},
		{path: "/players/harda", cookie: session},
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

// The phone's page menu and the account menu — or the guest's, on a share
// link — both offer a choice or an action, so they share name="menu-group":
// the browser closes whichever one was open when another opens. Without a
// shared name they open independently, and a page menu stayed open behind
// the account menu.
//
// The theme is not among them — it is three links rather than a
// disclosure — and neither is the language row, which opens in place inside
// the menu it sits in and would close that menu if it joined the group.
func TestTopbarMenusAreMutuallyExclusive(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")

	shared := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	signedIn := fetchAs(t, srv, "/leaderboard", signIn(t, srv, admin.ID)).Body.String()
	for body, menus := range map[string][]string{
		shared:   {`<details class="bar-menu menu" name="menu-group">`, `<details class="account guest menu" name="menu-group">`},
		signedIn: {`<details class="bar-menu menu" name="menu-group">`, `<details class="account menu" name="menu-group">`},
	} {
		for _, open := range menus {
			if !strings.Contains(body, open) {
				t.Errorf("%s is not in the menu-group group", open)
			}
		}
		if !strings.Contains(body, `<details class="lang-row">`) {
			t.Error("the language row is missing, or has joined a group it would close its menu with")
		}
	}
	if strings.Contains(shared, `<details class="theme`) {
		t.Error("the theme picker is a disclosure again; it is meant to be three links")
	}
}

func TestThemeChoiceIsRememberedAndApplied(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

// The account menu is for people with accounts. A reader without one — the
// share link — gets the empty seat in its place: a menu that says why it is
// empty, the way in, and the two preferences, and nothing that implies an
// account.
func TestAccountMenuOnlyForSignedInUsers(t *testing.T) {
	t.Parallel()

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
	if !strings.Contains(shared, `<details class="account guest menu"`) {
		t.Error("the shared board has no empty seat where the account would be")
	}
	for _, account := range []string{`href="/settings"`, `action="/logout"`, `href="/admin/settings"`} {
		if strings.Contains(shared, account) {
			t.Errorf("the shared board offers %s", account)
		}
	}

	as := func(u store.User) string {
		return fetchAs(t, srv, "/leaderboard", signIn(t, srv, u.ID)).Body.String()
	}
	adminBody := as(admin)
	if !strings.Contains(adminBody, `<details class="account menu"`) {
		t.Fatal("no account menu for a signed-in admin")
	}
	// Headed by who is signed in: the linked player's name where there is
	// one, and the address where, as here, there is not.
	if !strings.Contains(adminBody, `<span class="account-name">admin@example.tld</span>`) {
		t.Error("the account menu does not show which account is signed in")
	}
	if !strings.Contains(adminBody, `href="/admin/settings"`) {
		t.Error("an admin has no link to the admin area")
	}
	if strings.Contains(adminBody, "guest") {
		t.Error("a signed-in reader is shown the empty seat")
	}

	if body := as(ordinary); strings.Contains(body, `href="/admin/settings"`) {
		t.Error("a non-admin is offered the admin area")
	}
}

// The account menu is headed by the player the account plays as.
func TestTheAccountMenuNamesTheLinkedPlayer(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	harda, err := store.PlayerBySlug(ctx, srv.db, "harda")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkPlayer(ctx, srv.db, store.AdminActor(admin.ID), harda.ID, &admin.ID); err != nil {
		t.Fatal(err)
	}
	body := fetchAs(t, srv, "/leaderboard", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(body, `<span class="account-name">`+harda.Name+`</span>`) {
		t.Errorf("the account menu is not headed by the linked player, %s", harda.Name)
	}
}

// The menu opens with no JavaScript at all, which is the reason it is a
// <details> rather than a button. Its markup carries no inline handler
// either — app.js uses delegated listeners, never
// through anything written on an element itself.
func TestAccountMenuNeedsNoScript(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")

	body := fetchAs(t, srv, "/leaderboard", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(body, `<details class="account menu" name="menu-group">`) {
		t.Error("the account menu is not a details element")
	}
	if strings.Contains(body, "onclick") {
		t.Error("the account menu carries an inline event handler")
	}
}

// The shell is drawn once per page, by the layout rather than by each page
// calling for it. The two ways that breaks are a page template that kept its
// own call to the bar, and an error page that was handed the shell it is
// meant to go without.
func TestTheShellIsDrawnOncePerPage(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	for _, path := range []string{
		"/today", "/leaderboard", "/months", "/grid", "/players/harda", "/search",
		"/settings", "/privacy", "/admin/players", "/admin/pending",
		"/admin/activity", "/admin/diagnostics",
	} {
		body := fetchAs(t, srv, path, session).Body.String()
		if got := strings.Count(body, `<header class="topbar">`); got != 1 {
			t.Errorf("%s draws the bar %d times, want 1", path, got)
		}
		if got := strings.Count(body, `<footer class="site-footer">`); got != 1 {
			t.Errorf("%s draws the footer %d times, want 1", path, got)
		}
	}

	// An error page is chrome for a stranger: no bar, no way into the rest
	// of the application from a page that says there is nothing here.
	notFound := fetchAs(t, srv, "/no-such-page", session).Body.String()
	if strings.Contains(notFound, `<header class="topbar">`) {
		t.Error("a 404 renders the application's bar")
	}
}

// app.js repositions popups and dismisses topbar menus on outside clicks.
// This checks it is wired up once per page and its positioning listener
// is scoped to name="popup" rather
// than the topbar menus, which anchor themselves in CSS instead.
func TestPopupPositioningScriptIsWiredUpAndScoped(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	// Matched up to the path: the tag carries ?v= after it, see serveStatic.
	if got := strings.Count(body, `<script src="/static/app.js`); got != 1 {
		t.Errorf("expected one popup-positioning script tag, found %d", got)
	}
	if strings.Contains(body, "onclick") {
		t.Error("the page carries an inline event handler")
	}

	rec := fetchAs(t, srv, "/static/app.js", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/app.js = %d", rec.Code)
	}
	script := rec.Body.String()
	if !strings.Contains(script, `getAttribute("name") === "popup"`) {
		t.Error("the script does not scope itself to name=\"popup\"")
	}
	if strings.Contains(script, `getAttribute("name") === "menu-group"`) {
		t.Error("the positioning listener also reaches into the topbar menus, which anchor themselves in CSS instead")
	}
}

// The two links end every page, in the footer — the shell's, the sign-in
// family's and an error page's. So this is really a test that every page
// renders through one of those — signed out, signed in, admin, and the
// read-only share view alike.
func TestEveryPageReachesPrivacyAndTheSource(t *testing.T) {
	t.Parallel()

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
			t.Errorf("%s: no link to the source", p.path)
		}
		// The share view links its own copy, so a reader who came in by the
		// link is not sent out of it.
		privacy := `href="/privacy"`
		if strings.HasPrefix(p.path, "/share/") {
			privacy = `href="/share/` + slug + `/privacy"`
		}
		if !strings.Contains(body, privacy) {
			t.Errorf("%s: no link to the privacy notice", p.path)
		}
	}
}

// What this is, for someone who followed a link into it: About, in the
// account menu or the guest's, and again in the footer. It opens without a
// script — the same <details> the menus are — and in its own group, since it
// lives inside a menu and a shared group would close the menu it opened from.
func TestTheAboutPanelIsInEveryAccountMenuAndNeedsNoScript(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	for _, p := range []struct {
		path   string
		cookie *http.Cookie
	}{
		// Not "/": the sign-in family has no menu to hang this off, and
		// reaches the same two links through its footer instead.
		{path: "/share/" + slug + "/"},
		{path: "/leaderboard", cookie: session},
		{path: "/admin/settings", cookie: session},
	} {
		body := fetchAs(t, srv, p.path, p.cookie).Body.String()
		if got := strings.Count(body, `<details class="about" name="about">`); got != 1 {
			t.Errorf("%s: %d About panels in the menus, want one", p.path, got)
			continue
		}
		if got := strings.Count(body, `<details class="about about-footer" name="about">`); got != 1 {
			t.Errorf("%s: %d About links in the footer, want one", p.path, got)
		}
		menu, _ := sectionOf(body, `<div class="menu-panel glass account-menu">`, `<div class="about-panel"`)
		if !strings.Contains(menu, `<details class="about" name="about">`) {
			t.Errorf("%s: the About panel is not in the account menu", p.path)
		}
		if !strings.Contains(body, "About Wordleland") {
			t.Errorf("%s: the About panel says nothing about what this is", p.path)
		}
	}
}

func TestInitialsFor(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
		{path: "/share/" + slug + "/players/harda"},
		{path: "/share/" + slug + "/players/thin"},
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

// The theme control offers all three settings, marks the one in force, and
// each of them applies it. They are icon links, so the label a reader gets is
// the accessible name rather than text in the page.
func TestThemeControlOffersAllThree(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	for _, label := range []string{"Light", "Dark", "System"} {
		href := hrefForName(t, body, label)
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

	// System is in force to begin with, and the segmented control says so.
	if !strings.Contains(body, `class="seg-opt on"`) {
		t.Error("the segmented control does not mark the setting in force")
	}

	// And it sits between the two it chooses from: the middle of the track is
	// "whichever of these two the device says", which is only legible if the
	// two are either side of it.
	at := map[string]int{}
	for _, code := range []string{"light", "system", "dark"} {
		if at[code] = strings.Index(body, "theme="+code+`"`); at[code] < 0 {
			t.Fatalf("no link to the %s theme", code)
		}
	}
	if !(at["light"] < at["system"] && at["system"] < at["dark"]) {
		t.Errorf("the track reads %v, want system between light and dark", at)
	}
}

// Switching one setting keeps the rest of the query, filters included.
func TestPickersPreserveTheRestOfTheQuery(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board?mode=hard&lang=sv", nil).Body.String()
	dark := strings.ReplaceAll(hrefForName(t, body, "Mörkt"), "&amp;", "&")
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

// The language is a row in the account menu, or in the guest's on a share
// link — in the bar, on every surface. What
// it changes for a signed-in reader is their account, not just this
// browser — see TestSettingsLanguagePersistsToTheAccount.
func TestLanguagePickerIsAvailableEverywhere(t *testing.T) {
	t.Parallel()

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
	if strings.Contains(shared, `action="/logout"`) {
		t.Fatal("the shared board grew an account")
	}
	if !strings.Contains(shared, "lang=sv") {
		t.Error("the shared board has no way to change language")
	}
	// And it offers the way in.
	if !strings.Contains(shared, "Sign in") {
		t.Error("the shared board does not offer sign-in")
	}
}

// The share link's empty seat offers the way in: a button, not a bare link,
// at the foot of its menu, under the line that says why the seat is empty and
// the preferences a guest can still set — and nothing in the bar itself,
// where a sign-in button beside the seat would be two doors to one room.
func TestSharedBarOffersSignIn(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	menu, ok := sectionOf(body, `<details class="account guest menu"`, `</header>`)
	if !ok {
		t.Fatal("the shared bar has no empty seat")
	}
	for _, want := range []string{"Viewing as guest", "You opened a shared link", `<a class="btn account-signin" href="/">`, "Sign in"} {
		if !strings.Contains(menu, want) {
			t.Errorf("the guest menu is missing %q", want)
		}
	}
	if strings.Index(menu, "account-signin") < strings.Index(menu, `<details class="about"`) {
		t.Error("the way in is not the last thing the guest menu offers")
	}
	bar, _ := sectionOf(body, `<header class="topbar">`, `<details class="account guest menu"`)
	if strings.Contains(bar, `class="btn`) {
		t.Error("the bar carries a sign-in button of its own beside the seat")
	}

	// Signed in, there is nothing to sign in to.
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	in := fetchAs(t, srv, "/today", signIn(t, srv, admin.ID)).Body.String()
	if strings.Contains(in[:strings.Index(in, "</header>")], "account-signin") {
		t.Error("a signed-in reader is offered a sign-in button")
	}
}

// Search is glass like the rest of the bar, and its shortcut is a key cap
// on the surface, which is what makes it read as raised out of the field.
func TestTheSearchControlIsGlassWithAKeyCap(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	if !strings.Contains(body, `<a class="bar-search glass" href="/search"`) {
		t.Error("the search control is not a glass link to the search page")
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	key := cssRule(t, css, ".bar-search-key {")
	if !strings.Contains(key, "background: var(--color-surface)") {
		t.Error("the shortcut is not a key cap on the surface")
	}
}

// cssRule returns the body of the first rule in css whose selector opens
// with prefix (e.g. ".menu-btn {"), for a test that wants to inspect one
// rule's declarations without matching a substring anywhere else in the
// file.
// The prefix is anchored to the start of an unindented line, so it finds the
// rule it names and not a longer selector ending in the same text. Without
// that anchor ".search-btn {" also matches ".topbar-search .search-btn {" and
// any grouped selector whose last member is .search-btn — both of which live
// inside media queries, come earlier in the file, and would hand back the
// wrong declarations.
func cssRule(t *testing.T, css, prefix string) string {
	t.Helper()
	start := strings.Index(css, "\n"+prefix)
	if start < 0 {
		t.Fatalf("no rule opening with %q found", prefix)
	}
	start++
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatalf("rule opening with %q is never closed", prefix)
	}
	return css[start : start+end]
}

// The search button's word is there, but quiet: the icon already says
// "search", so the label is a muted hint rather than a second copy of the
// same information at full strength. aria-label backs it up regardless,
// since the label hides outright on a narrow screen.
func TestSearchButtonLabelIsPresentButFaded(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	start := strings.Index(body, `class="bar-search glass"`)
	if start < 0 {
		t.Fatal("no search button on the page")
	}
	end := strings.Index(body[start:], "</a>")
	if end < 0 {
		t.Fatal("the search button is never closed")
	}
	tag := body[start : start+end]

	if !strings.Contains(tag, `>Search players and pages<`) {
		t.Error("the search button lost its visible label")
	}
	if !strings.Contains(tag, `aria-label="Search"`) {
		t.Error("the search button has no accessible name for when the label hides on a narrow screen")
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	if !strings.Contains(cssRule(t, css, ".bar-search {"), "color: var(--color-muted)") {
		t.Error("the search label is not styled as faded")
	}
}

// The door is its own arrangement, not the application shell with the
// navigation taken out of it: a bar emptied down to a wordmark is a menu
// with nothing in it, which reads as an app that has lost its own rather
// than as a way in.
func TestTheSignInFamilyHasItsOwnFrame(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)

	for _, path := range []string{"/", "/forgot-password", "/reset-password?token=x", "/invite?token=x"} {
		body := fetchAs(t, srv, path, nil).Body.String()
		if !strings.Contains(body, `<div class="auth-frame">`) {
			t.Errorf("%s is not drawn in the auth frame", path)
		}
		for _, gone := range []string{`<header class="topbar">`, `<div class="page">`} {
			if strings.Contains(body, gone) {
				t.Errorf("%s still draws %s", path, gone)
			}
		}
		// No bar means no menu to carry Help, so the footer is how these two
		// links stay reachable.
		if !strings.Contains(body, `<footer class="auth-footer">`) {
			t.Errorf("%s reaches neither the privacy notice nor the source", path)
		}
	}

	// The three frames are distinct, and each page gets exactly one.
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	app := fetchAs(t, srv, "/today", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(app, `<div class="page">`) || strings.Contains(app, "auth-frame") {
		t.Error("an application page is not drawn in the application shell")
	}
	bare := fetchAs(t, srv, "/no/such/page", nil).Body.String()
	if strings.Contains(bare, "auth-frame") || strings.Contains(bare, `<div class="page">`) {
		t.Error("an error page is drawn in a frame")
	}
}

// A failure is drawn in the danger tone and a warning in the second accent,
// and inside a card both sit on the card's own gutter.
//
// Both were drawn in the brand hue, because the palette had no red or amber
// when they were written — a green ring around "the form expired" told a
// reader the opposite of what the words did. And as a direct child of a card
// the box inherits the card's gutter as padding, which its own shorthand
// throws away: it spanned the card edge to edge while the heading beneath it
// sat 22px in.
func TestAFailureIsDrawnAsOneAndSitsOnTheGutter(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	for _, want := range []string{
		".note.error { background: var(--color-danger-08); box-shadow: inset 0 0 0 1px var(--color-danger); }",
		".note.warn { background: var(--color-accent-2-14); box-shadow: inset 0 0 0 1px var(--color-accent-2); }",
		".card > .note.warn { margin: 14px var(--space-card); }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("missing rule: %s", want)
		}
	}
	// The accent is the brand hue. Neither of these may reach for it again.
	for _, rule := range []string{".note.error", ".note.warn", ".hint.warn"} {
		at := strings.Index(css, "\n"+rule+" {")
		if at < 0 {
			t.Errorf("no rule for %s", rule)
			continue
		}
		body := css[at : at+strings.Index(css[at:], "}")]
		if strings.Contains(body, "--color-accent-strong") || strings.Contains(body, "--color-accent-50") {
			t.Errorf("%s is drawn in the brand hue: %s", rule, body)
		}
	}
}

// The one link on the site that leaves it.
//
// It opens a tab of its own, which is a thing a reader has to be told rather
// than discover: the arrow leaving its box says it to anyone looking, and the
// words beside it, visually hidden, say it to anyone who is not. rel carries
// noreferrer as well as noopener, for the reason nothing on these pages is
// fetched from a third party — where somebody was reading is not GitHub's to
// know.
func TestTheOneExternalLinkSaysThatItLeaves(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{
		"/share/" + slug + "/", // the About panel in the rail
		"/",                    // the sign-in page's footer
	} {
		body := fetchAs(t, srv, path, nil).Body.String()
		at := strings.Index(body, `href="https://github.com/`)
		if at < 0 {
			t.Errorf("%s: no source link", path)
			continue
		}
		link := body[strings.LastIndex(body[:at], "<a "):]
		link = link[:strings.Index(link, "</a>")]

		for _, want := range []string{`target="_blank"`, `rel="noopener noreferrer"`} {
			if !strings.Contains(link, want) {
				t.Errorf("%s: the source link is missing %s", path, want)
			}
		}
		if !strings.Contains(link, "opens in a new tab") {
			t.Errorf("%s: nothing tells a reader it opens a tab of its own", path)
		}
		if !strings.Contains(link, `class="external-icon"`) {
			t.Errorf("%s: nothing shows a reader it leaves the site", path)
		}
	}

	// And it is the only link that does: every other one on an application
	// page stays here, which is what makes the mark mean something. The count
	// is two rather than one because the About panel holding it is rendered
	// twice — once in the rail, once in the drawer — and defined once.
	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	tabs := strings.Count(body, `target="_blank"`)
	if source := strings.Count(body, `href="https://github.com/`); tabs != source {
		t.Errorf("%d links open a new tab against %d source links; something else leaves the site", tabs, source)
	}
}

// Every sentence a reader can see is in their language, including the ones
// on the way in and the ones that say something went wrong.
//
// The error page, the sign-in form, the two-factor prompt and the password
// reset all had their messages written into the Go rather than into the
// catalogue, so a Swedish reader got a translated page with an English
// sentence under it — invisible in English, which is the language everybody
// who wrote them was reading in.
func TestTheErrorsSpeakTheReadersLanguage(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedLogin(t, srv, "admin@example.tld", true)

	// The error page, for a stranger who has picked Swedish.
	for _, tt := range []struct{ path, want, never string }{
		{"/no-such-page?lang=sv", "Det finns ingenting på den här adressen.", "There is nothing at this address"},
	} {
		body := fetchAs(t, srv, tt.path, nil).Body.String()
		if !strings.Contains(body, tt.want) {
			t.Errorf("%s: the error page does not say %q", tt.path, tt.want)
		}
		if strings.Contains(body, tt.never) || strings.Contains(body, ">Not Found<") {
			t.Errorf("%s: the error page still speaks English", tt.path)
		}
	}

	// A rejected sign-in, in the language the form was shown in.
	csrf, cookies := getCSRF(t, srv, "/?lang=sv", nil)
	rec := postForm(t, srv, "/login", url.Values{
		"csrf_token": {csrf}, "email": {"admin@example.tld"}, "password": {"wrong"},
	}, cookies)
	if body := rec.Body.String(); !strings.Contains(body, "E-postadressen och lösenordet stämmer inte.") ||
		strings.Contains(body, "do not match") {
		t.Error("a rejected sign-in answers in English to a Swedish reader")
	}

	// And a rejected two-factor code.
	_, cookies = login(t, srv, "admin@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)
	_, pending := login(t, srv, "admin@example.tld", testPassword)
	csrf, pending = getCSRF(t, srv, "/totp?lang=sv", pending)
	rec = postForm(t, srv, "/totp", url.Values{"csrf_token": {csrf}, "code": {"000000"}}, pending)
	if body := rec.Body.String(); !strings.Contains(body, "Koden stämmer inte.") ||
		strings.Contains(body, "That code is not right") {
		t.Error("a rejected code answers in English to a Swedish reader")
	}
	_ = cookies
}
