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

// The navigation drawer, the language picker and the account menu all offer
// a choice or an action, so they share name="menu-group": the browser closes
// whichever one was open when another opens. Without a shared name they open
// independently, which is how the language picker used to leave the theme
// picker open.
//
// The theme picker is no longer among them — it is three links rather than a
// disclosure — but the reason the group exists outlives it, and the drawer
// joining it is what keeps a full-height panel from staying open behind a
// menu.
func TestTopbarMenusAreMutuallyExclusive(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	for _, open := range []string{
		`<details class="drawer" name="menu-group">`,
		`<details class="menu" name="menu-group">`,
	} {
		if !strings.Contains(body, open) {
			t.Errorf("%s is not in the menu-group group", open)
		}
	}
	if strings.Contains(body, `<details class="theme`) {
		t.Error("the theme picker is a disclosure again; it is meant to be three links")
	}
}

// The account menu is a menu too, so it must join the same exclusive group
// as the pickers — otherwise opening it while a picker is open leaves both
// showing.
func TestAccountMenuJoinsTheTopbarMenuGroup(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")

	body := fetchAs(t, srv, "/leaderboard", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(body, `<details class="account" name="menu-group">`) {
		t.Error("the account menu does not share name=\"menu-group\" with the pickers")
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
	if !strings.Contains(adminBody, `href="/admin/settings"`) {
		t.Error("an admin has no link to the admin area")
	}

	if body := as(ordinary); strings.Contains(body, `href="/admin/settings"`) {
		t.Error("a non-admin is offered the admin area")
	}
}

// The menu opens with no JavaScript at all, which is the reason it is a
// <details> rather than a button. Its markup carries no inline handler
// either — app.js uses delegated listeners, never
// through anything written on an element itself.
func TestAccountMenuNeedsNoScript(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")

	body := fetchAs(t, srv, "/leaderboard", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(body, "<details class=\"account\" name=\"menu-group\">") {
		t.Error("the account menu is not a details element")
	}
	if strings.Contains(body, "onclick") {
		t.Error("the account menu carries an inline event handler")
	}
}

// Collapsing the rail is a link first and a script second. The link is the
// whole feature — following it re-renders at the other width and the cookie
// remembers — and app.js only does the visible half without the round trip.
// What it needs from the server is the other destination and both labels,
// since it renders neither itself.
func TestTheCollapseControlWorksWithoutScript(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	control, ok := sectionOf(body, `<a class="nav-row nav-collapse"`, "</a>")
	if !ok {
		t.Fatal("the rail has no collapse control")
	}

	// A link with somewhere to go, not a button waiting for a handler.
	if !strings.Contains(control, `href="`) {
		t.Error("the collapse control is not a link")
	}
	if !strings.Contains(control, "sidebar=narrow") {
		t.Error("a wide rail's control does not point at the narrow width")
	}

	// Both destinations, so the script can point the link at the other one
	// after flipping the rail in place.
	for _, attr := range []string{`data-href-wide="`, `data-href-narrow="`} {
		if !strings.Contains(control, attr) {
			t.Errorf("the collapse control is missing %s", attr)
		}
	}

	// Both labels, so the wording and the accessible name follow the width
	// through CSS rather than being written by the script.
	for _, label := range []string{"Collapse sidebar", "Expand sidebar"} {
		if !strings.Contains(control, ">"+label+"<") {
			t.Errorf("the collapse control does not carry %q", label)
		}
	}

	// And the script is wired to that control and nothing else.
	js := fetchAs(t, srv, "/static/app.js", nil).Body.String()
	if !strings.Contains(js, `querySelector(".nav-collapse")`) {
		t.Error("app.js does not bind the collapse control")
	}
	if !strings.Contains(js, "preventDefault") {
		t.Error("app.js does not take over the click it is enhancing")
	}
}

// The rail's width is remembered the way the theme is: a link sets it, a
// cookie keeps it, and <html> says which one is in force. Nothing about it
// depends on a script having run.
func TestSidebarWidthIsRememberedAndApplied(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/board?sidebar=narrow", nil)
	if !strings.Contains(rec.Body.String(), `data-sidebar="narrow"`) {
		t.Error("?sidebar=narrow did not apply")
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sidebarCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("collapsing the rail set no cookie")
	}
	if got := fetchAs(t, srv, "/share/"+slug+"/", cookie).Body.String(); !strings.Contains(got, `data-sidebar="narrow"`) {
		t.Error("the remembered width was not applied on a later request")
	}

	// Wide is the default, and a value that is neither is ignored rather than
	// written through to the attribute.
	bad := fetchAs(t, srv, "/share/"+slug+"/board?sidebar=hidden", nil).Body.String()
	if !strings.Contains(bad, `data-sidebar="wide"`) {
		t.Error("an unknown width was not rejected")
	}
}

// A collapsed rail keeps its labels in the markup. Hiding them with
// display:none would save the same width and leave every row an icon with no
// accessible name, which is the kind of saving that costs somebody the page.
func TestACollapsedRailKeepsItsLabels(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board?sidebar=narrow", nil).Body.String()
	rail, ok := sectionOf(body, `<nav class="sidebar"`, "</nav>")
	if !ok {
		t.Fatal("the rail is missing")
	}
	for _, view := range []string{"Today", "Leaderboard", "Months", "Grid", "Players"} {
		if !strings.Contains(rail, ">"+view+"<") {
			t.Errorf("the collapsed rail dropped %q from the markup", view)
		}
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	rule, ok := ruleFor(css, `:root[data-sidebar="narrow"] .sidebar .nav-label`)
	if !ok {
		t.Fatal("nothing hides the labels when the rail is collapsed")
	}
	if strings.Contains(rule, "display: none") {
		t.Error("the collapsed rail's labels are hidden from assistive technology too")
	}
}

// The shell is drawn once per page, by the layout rather than by each page
// calling for it. The two ways that breaks are a page template that kept its
// own call to the bar, and an error page that was handed the shell it is
// meant to go without.
func TestTheShellIsDrawnOncePerPage(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	for _, path := range []string{
		"/today", "/leaderboard", "/months", "/grid", "/players", "/search",
		"/settings", "/privacy", "/admin/players", "/admin/pending",
		"/admin/activity", "/admin/diagnostics",
	} {
		body := fetchAs(t, srv, path, session).Body.String()
		if got := strings.Count(body, `<nav class="sidebar"`); got != 1 {
			t.Errorf("%s draws the rail %d times, want 1", path, got)
		}
		if got := strings.Count(body, `<header class="topbar">`); got != 1 {
			t.Errorf("%s draws the bar %d times, want 1", path, got)
		}
	}

	// An error page is chrome for a stranger: no rail, no bar, no way into
	// the rest of the application from a page that says there is nothing here.
	notFound := fetchAs(t, srv, "/no-such-page", session).Body.String()
	if strings.Contains(notFound, `<nav class="sidebar"`) || strings.Contains(notFound, `<header class="topbar">`) {
		t.Error("a 404 renders the application shell")
	}
}

// The drawer opens, closes and reports its state without a line of script:
// it is a <details>, the browser owns the open state, and nothing here binds
// a handler to it.
func TestTheDrawerNeedsNoScript(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	if !strings.Contains(body, `<details class="drawer" name="menu-group">`) {
		t.Error("the drawer is not a details element in the menu-group group")
	}
	if !strings.Contains(body, `<summary class="menu-btn drawer-btn"`) {
		t.Error("the drawer has no summary to open it")
	}
	if strings.Contains(body, "onclick") {
		t.Error("the drawer carries an inline event handler")
	}
	// Saying it is a modal dialog would be a claim this cannot keep: focus is
	// free to leave an open drawer, and nothing server-rendered can hold it.
	drawer, ok := sectionOf(body, `<details class="drawer"`, "</details>")
	if !ok {
		t.Fatal("the drawer is missing")
	}
	if strings.Contains(drawer, "aria-modal") || strings.Contains(drawer, `role="dialog"`) {
		t.Error("the drawer claims to be a modal dialog it cannot behave as")
	}
}

// app.js repositions popups and dismisses topbar menus on outside clicks.
// This checks it is wired up once per page and its positioning listener
// is scoped to name="popup" rather
// than the topbar menus, which anchor themselves in CSS instead.
func TestPopupPositioningScriptIsWiredUpAndScoped(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	if got := strings.Count(body, `<script src="/static/app.js" defer></script>`); got != 1 {
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

// The two links live once each — in the About panel at the foot of the rail
// for a page inside the shell, and in the footer for an error page, which has
// no rail. So this is really a test that every page renders through one of
// those — signed out, signed in, admin, and the read-only share view alike.
func TestEveryPageReachesPrivacyAndTheSource(t *testing.T) {
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
		if !strings.Contains(body, `href="/privacy"`) {
			t.Errorf("%s: no link to the privacy notice", p.path)
		}
	}
}

// What this is, for someone who followed a link into it. It replaces the page
// footer, so it has to be on every page inside the shell and to open without a
// script — the same <details> the drawer and the menus are.
func TestTheAboutPanelIsOnEveryShellPageAndNeedsNoScript(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	for _, p := range []struct {
		path   string
		cookie *http.Cookie
	}{
		// Not "/": the sign-in family has no rail to hang this off, and
		// reaches the same two links through the footer instead.
		{path: "/share/" + slug + "/"},
		{path: "/leaderboard", cookie: session},
		{path: "/admin/settings", cookie: session},
	} {
		body := fetchAs(t, srv, p.path, p.cookie).Body.String()
		if !strings.Contains(body, `<details class="about" name="about">`) {
			t.Errorf("%s: no About panel", p.path)
			continue
		}
		if !strings.Contains(body, "About Wordleland") {
			t.Errorf("%s: the About panel says nothing about what this is", p.path)
		}
		// The rail renders one and the drawer renders the other, sharing a
		// name so only one can be open — and so that opening the drawer's
		// does not close the drawer it lives in, which a shared group with
		// the drawer would.
		if got := strings.Count(body, `<details class="about" name="about">`); got != 2 {
			t.Errorf("%s: %d About panels, want one in the rail and one in the drawer", p.path, got)
		}
	}

	// And the footer it replaces is gone from inside the shell, so the two
	// links are in one place rather than two.
	shell := fetchAs(t, srv, "/leaderboard", session).Body.String()
	if strings.Contains(shell, `class="site-footer"`) {
		t.Error("the page footer is still rendered inside the shell")
	}
	// It stays on an error page, which has no rail to carry them.
	if !strings.Contains(fetchAs(t, srv, "/no/such/page", nil).Body.String(), `class="site-footer"`) {
		t.Error("an error page has neither a rail nor a footer, so it reaches nothing")
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

// The theme control offers all three settings, marks the one in force, and
// each of them applies it. They are icon links, so the label a reader gets is
// the accessible name rather than text in the page.
func TestThemeControlOffersAllThree(t *testing.T) {
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

	// System is in force to begin with, and the track says so.
	if !strings.Contains(body, `class="theme-opt on"`) {
		t.Error("the track does not mark the setting in force")
	}

	// And it sits between the two it chooses from: the middle of the track is
	// "whichever of these two the device says", which is only legible if the
	// two are either side of it.
	at := map[string]int{}
	for _, code := range []string{"light", "system", "dark"} {
		// The first occurrence of each is its place in the track, which comes
		// before the single cycling link the narrow bar uses.
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

	narrow := strings.ReplaceAll(hrefFor(t, body, "Fäll ihop sidofältet"), "&amp;", "&")
	for _, want := range []string{"mode=hard", "lang=sv", "sidebar=narrow"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("the collapse link %q dropped %q", narrow, want)
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

	for _, want := range []string{`class="btn"`, "Sign in", "<svg"} {
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
	if strings.Contains(in[:strings.Index(in, "</header>")], `class="btn"`) {
		t.Error("a signed-in reader is offered a sign-in button")
	}
}

// The bar stands on the surface and the page on the canvas, and the search
// control takes the page's ground rather than the bar's — which is what makes
// it read as a field cut into the bar instead of a button sitting on it.
func TestTheSearchControlSitsOnThePagesGround(t *testing.T) {
	srv := testServer(t)

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	bar := cssRule(t, css, ".topbar {")
	if !strings.Contains(bar, "background: var(--color-surface)") {
		t.Error("the bar does not stand on the surface")
	}

	searchBtn := cssRule(t, css, ".search-btn {")
	if !strings.Contains(searchBtn, "background: var(--color-canvas)") {
		t.Error(".search-btn does not take the page's ground")
	}

	// The chip is a key cap: it steps back off that ground, which is what
	// makes it read as raised out of the control.
	shortcut := cssRule(t, css, ".search-shortcut {")
	if !strings.Contains(shortcut, "font-size: var(--text-xs)") {
		t.Error("the shortcut chip is not the smallest step")
	}
	if !strings.Contains(shortcut, "background: var(--color-surface-raised)") {
		t.Error("the shortcut chip does not step off the control's ground")
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

// On a narrow screen the sign-in button drops its text and keeps just the
// icon, matching the search button's own label-hiding rule at the same
// breakpoint — aria-label is what carries the accessible name once the
// visible text is display:none.
func TestSignInButtonDropsItsLabelOnMobile(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	if !strings.Contains(body, `aria-label="Sign in"`) {
		t.Fatal("the sign-in button has no accessible name to fall back on")
	}
	if !strings.Contains(body, `<span class="btn-label">Sign in</span>`) {
		t.Fatal("the sign-in button's label is not its own element to hide")
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	if !strings.Contains(css, ".btn-label { display: none; }") {
		t.Error("no rule hides the sign-in label on a narrow screen")
	}
}

// The search button's word is there, but quiet: the icon already says
// "search", so the label is a muted hint rather than a second copy of the
// same information at full strength. aria-label backs it up regardless,
// since the label hides outright on a narrow screen.
func TestSearchButtonLabelIsPresentButFaded(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	start := strings.Index(body, `class="menu-btn search-btn"`)
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
	if !strings.Contains(css, ".search-label { color: var(--color-text-45); flex: 1; text-align: left; }") {
		t.Error("the search label is not styled as faded")
	}
}

// The door is its own arrangement, not the application shell with the
// navigation taken out of it: a rail emptied down to a wordmark is a menu
// with nothing in it, which reads as an app that has lost its own rather
// than as a way in.
func TestTheSignInFamilyHasItsOwnFrame(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	for _, path := range []string{"/", "/forgot-password", "/reset-password?token=x", "/invite?token=x"} {
		body := fetchAs(t, srv, path, nil).Body.String()
		if !strings.Contains(body, `<div class="auth-frame">`) {
			t.Errorf("%s is not drawn in the auth frame", path)
		}
		for _, gone := range []string{`class="sidebar"`, `<header class="topbar">`, `class="shell-main"`} {
			if strings.Contains(body, gone) {
				t.Errorf("%s still draws %s", path, gone)
			}
		}
		// No rail means no About panel, so the footer is how these two
		// links stay reachable.
		if !strings.Contains(body, `class="site-footer"`) {
			t.Errorf("%s reaches neither the privacy notice nor the source", path)
		}
	}

	// The three frames are distinct, and each page gets exactly one.
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	app := fetchAs(t, srv, "/today", signIn(t, srv, admin.ID)).Body.String()
	if !strings.Contains(app, `<div class="shell">`) || strings.Contains(app, "auth-frame") {
		t.Error("an application page is not drawn in the application shell")
	}
	bare := fetchAs(t, srv, "/no/such/page", nil).Body.String()
	if strings.Contains(bare, "auth-frame") || strings.Contains(bare, `<div class="shell">`) {
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
