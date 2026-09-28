package web

import (
	"regexp"
	"strings"
	"testing"
)

// selfLink matches the picker links that carry the current path. csrfValue
// masks tokens where these isolated requests do not carry a browser cookie.
var (
	selfLink  = regexp.MustCompile(`href="[^"?]*\?(theme=|lang=)`)
	csrfValue = regexp.MustCompile(`name="csrf_token" value="[^"]*"`)
)

// The admin screens are one area and have to look like it: the same header
// card, the same pill row in the same place. Their subtitles are the
// exception — Players and Pending count what they hold, and Activity and
// Diagnostics have nothing to count — so the comparison stops at the head.
func TestAdminScreensShareTheirChrome(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	tag := regexp.MustCompile(`<(section|nav|div|h1|p)[^>]*class="([^"]*)"`)
	shapes := map[string][]string{}

	for _, path := range []string{"/admin/players", "/admin/pending", "/admin/activity"} {
		body := fetchAs(t, srv, path, session).Body.String()
		start := strings.Index(body, `<header class="page-head">`)
		if start < 0 {
			t.Fatalf("%s has no page head", path)
		}
		head := body[start:]
		if cut := strings.Index(head, "</nav>"); cut > 0 {
			head = head[:cut]
		}
		var seq []string
		for _, m := range tag.FindAllStringSubmatch(head, -1) {
			seq = append(seq, m[1]+"."+m[2])
		}
		shapes[path] = seq
	}

	want := shapes["/admin/activity"]
	for path, got := range shapes {
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s chrome = %v\nwant                  %v", path, got, want)
		}
	}
}

// The top bar is the app's furniture, not a property of the board views.
// Settings and the admin area kept losing their pills, which made them
// look like a different site.
func TestTopBarKeepsItsViewsEverywhere(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	views := []string{"Leaderboard", "Months", "Grid", "Players"}
	for _, path := range []string{
		"/today", "/leaderboard", "/settings",
		"/admin/players", "/admin/pending", "/admin/activity",
	} {
		body := fetchAs(t, srv, path, session).Body.String()
		bar := body[strings.Index(body, `class="topbar`):]
		bar = bar[:strings.Index(bar, "</header>")]
		for _, v := range views {
			if !strings.Contains(bar, ">"+v+"<") {
				t.Errorf("%s: the top bar is missing %q", path, v)
			}
		}
	}

	// But not before there is a session: every view needs one, so offering
	// them on the sign-in page offers a round trip back to it.
	for _, path := range []string{"/", "/forgot"} {
		body := fetchAs(t, srv, path, nil).Body.String()
		for _, v := range views {
			if strings.Contains(body, `class="pill-nav-item`) && strings.Contains(body, ">"+v+"<") {
				t.Errorf("%s offers %q before sign-in", path, v)
			}
		}
	}
}

// The switch keeps the small-caps caption every other field has, but its
// explanation is a whole sentence — .form label uppercases everything
// inside it, so the hint needs its own reset or it shouts.
func TestRosterSwitchHintIsNotUppercased(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	at := strings.Index(css, ".form label.switch .hint")
	if at < 0 {
		t.Fatal("nothing resets the uppercase inherited by the switch hint")
	}
	rule := css[at:]
	rule = rule[:strings.Index(rule, "}")]
	if !strings.Contains(rule, "text-transform: none") {
		t.Error("the switch hint does not reset text-transform")
	}
	// The switch is a row with a name of its own, not a field's caption:
	// the label's bold goes, and the name carries it.
	if rule := cssRule(t, css, ".form label.switch {"); !strings.Contains(rule, "font-weight: 400") {
		t.Errorf("the switch row keeps the field caption's weight: %s", rule)
	}

	// And the copy itself is written in sentence case, so the reset is the
	// only thing between it and capitals.
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	body := fetchAs(t, srv, "/admin/players/harda", session).Body.String()
	for _, want := range []string{
		"Still in the group",
		"Turning it off retires the player and keeps their history.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
	if strings.Contains(body, "Membership, not recency") {
		t.Error("the hint still carries the dropped first sentence")
	}
}

// The door has no bar. There is nothing yet to navigate and no account to
// open, so the sign-in family is the card on its field of tiles and nothing
// above it; the language — the one preference worth choosing before signing
// in — is in the footer, in one fixed place on every page of the family,
// beside the two links every page ends with. It used to sit at the foot of
// each card, whose links differ per page, and jumped between them.
func TestTheDoorHasNoBarAndItsLanguageInTheFooter(t *testing.T) {
	t.Parallel()

	srv := testServer(t)

	for _, path := range []string{"/", "/forgot-password", "/reset-password?token=x", "/invite?token=x"} {
		body := fetchAs(t, srv, path, nil).Body.String()

		if strings.Contains(body, `<header class="topbar">`) {
			t.Errorf("%s draws the application's bar", path)
		}
		footer, ok := sectionOf(body, `<footer class="auth-footer">`, "</footer>")
		if !ok {
			t.Errorf("%s has no footer", path)
			continue
		}
		if n := strings.Count(body, `<details class="auth-lang menu" name="menu-group">`); n != 1 {
			t.Errorf("%s renders the language picker %d times, want 1", path, n)
		}
		if !strings.Contains(footer, `class="auth-lang menu"`) {
			t.Errorf("%s renders the language picker outside the footer", path)
		}
		for _, link := range []string{`href="/privacy"`, `href="https://github.com/martinstenrose/wordleland"`} {
			if !strings.Contains(footer, link) {
				t.Errorf("%s: the footer has no %s", path, link)
			}
		}
		// No theme control: the door follows the device, or whatever was
		// chosen inside.
		if strings.Contains(body, `class="seg"`) {
			t.Errorf("%s offers a theme control", path)
		}
	}
}

// The chrome is one thing, built in one place. It used to be assembled per
// page, so the "N days" beside the wordmark appeared on the five board views
// and vanished on Settings and in the admin area.
//
// The phone's capsule is cut out before comparing: it names the page you are
// on, which is the one part of the bar meant to differ.
func TestTheChromeIsIdenticalOnEveryPage(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	chromes := map[string]string{}
	for _, path := range []string{
		// /players is not a page: it opens on whoever leads the board, so
		// the player's own page is what stands for that view here.
		"/today", "/leaderboard", "/months", "/grid", "/players/harda",
		"/settings", "/admin/players", "/admin/pending", "/admin/activity",
		"/admin/diagnostics",
	} {
		body := fetchAs(t, srv, path, session).Body.String()

		bar, ok := sectionOf(body, `<header class="topbar">`, "</header>")
		if !ok {
			t.Fatalf("%s has no bar", path)
		}
		if open := strings.Index(bar, `<details class="bar-menu menu"`); open >= 0 {
			bar = bar[:open] + bar[open+strings.Index(bar[open:], "</details>")+len("</details>"):]
		}
		footer, ok := sectionOf(body, `<footer class="site-footer">`, "</footer>")
		if !ok {
			t.Fatalf("%s has no footer", path)
		}

		combined := bar + footer
		// Three things are meant to differ: which view is marked current,
		// the theme and language links, which point back at the page you
		// are on so switching keeps you there, and the sign-out form's
		// CSRF token: these isolated requests do not share a cookie jar.
		combined = strings.ReplaceAll(combined, " on", "")
		combined = strings.ReplaceAll(combined, ` aria-current="page"`, "")
		combined = selfLink.ReplaceAllString(combined, `href="?$1`)
		combined = csrfValue.ReplaceAllString(combined, `name="csrf_token"`)
		chromes[path] = combined
	}

	want := chromes["/today"]
	if !strings.Contains(want, "footer-days") {
		t.Fatal("the footer has no count of days to compare")
	}
	for path, got := range chromes {
		if got != want {
			t.Errorf("%s renders different chrome than /today", path)
		}
	}
}

// How much history exists is said on the sign-in page, in figures, by the
// panel beside the form — and nowhere on an error page, which is chrome for
// a stranger with no reason to be told anything about this installation.
func TestAnErrorPageDoesNotSayHowBigTheGroupIs(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)

	if body := fetchAs(t, srv, "/", nil).Body.String(); !strings.Contains(body, "signin-figure") {
		t.Error("the sign-in page does not say how much history there is")
	}
	body := fetchAs(t, srv, "/no/such/page", nil).Body.String()
	if !strings.Contains(body, `<footer class="site-footer">`) {
		t.Fatal("an error page has no footer")
	}
	if strings.Contains(body, "footer-days") {
		t.Error("an error page reports the size of the board")
	}
}

// The bar's capsule carries the mark, which is the way back to Today, and
// every view after it. The phone's capsule lists the same views from the same
// list, so neither can go stale without the other.
func TestTheBarCarriesEveryViewAfterTheMark(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	pages, ok := sectionOf(body, `<nav class="bar-pages glass"`, "</nav>")
	if !ok {
		t.Fatal("the bar has no capsule of pages")
	}
	menu, ok := sectionOf(body, `<details class="bar-menu menu"`, "</details>")
	if !ok {
		t.Fatal("the bar has no page menu for a phone")
	}
	for _, view := range []string{"Today", "Leaderboard", "Months", "Grid", "Players"} {
		if !strings.Contains(pages, "<span>"+view+"</span>") {
			t.Errorf("the capsule is missing %q", view)
		}
		if !strings.Contains(menu, `<span class="menu-row-label">`+view+"</span>") {
			t.Errorf("the phone's page menu is missing %q", view)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(pages[strings.Index(pages, ">")+1:]), `<a class="bar-home" href="/today"`) {
		t.Error("the capsule does not lead with the mark, linking to Today")
	}
	// The phone's capsule names the page you are on.
	if !strings.Contains(menu, `<span class="bar-capsule-label">Leaderboard</span>`) {
		t.Error("the phone's capsule does not name the page it is on")
	}
}

// The account menu carries one row for the admin area, for an admin and
// nobody else; which screen inside the area you are on is the pill row at the
// top of that screen's job. The four screens listed in both places would be
// two lists of the same destinations to keep in step.
func TestTheAccountMenuCarriesOneAdminRow(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/pending", session).Body.String()
	menu, ok := sectionOf(body, `<div class="menu-panel glass account-menu">`, "</form>")
	if !ok {
		t.Fatal("no account menu")
	}
	if !strings.Contains(menu, `<span class="menu-row-label">Admin area</span>`) {
		t.Error("the account menu does not offer the admin area")
	}
	for _, screen := range []string{"Pending results", "Activity log", "Diagnostics"} {
		if strings.Contains(menu, ">"+screen+"<") {
			t.Errorf("the account menu lists %q, which the page's own row carries", screen)
		}
	}
	if strings.Contains(fetchAs(t, srv, "/today", signIn(t, srv, seedLogin(t, srv, "member@example.tld", false).ID)).Body.String(), ">Admin area<") {
		t.Error("a member who is not an admin is offered the admin area")
	}

	// And that row is on the page, under the heading that names the
	// section.
	if !strings.Contains(body, `<nav class="admin-tabs" aria-label="Admin area">`) {
		t.Error("the admin screen has no tab bar")
	}
	if !strings.Contains(body, `<h1 class="page-title">Pending results</h1>`) {
		t.Error("the row's head does not carry the section's name as the page's heading")
	}
}

// Every admin screen carries the tab bar, including the one that is not a
// section of its own.
//
// The activity detail is the sixth admin screen and the easy one to forget:
// it is a row of the activity log opened up, so it has a subject of its own
// and does not appear in the list the other five are built from. Losing the
// row there would leave one screen with no way back out of it except the
// browser's own, which is the failure this pins.
func TestEveryAdminScreenCarriesTheSectionBar(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	// The five sections, each naming itself.
	screens := map[string]string{
		"/admin/settings":    "Settings",
		"/admin/players":     "Players",
		"/admin/pending":     "Pending results",
		"/admin/activity":    "Activity log",
		"/admin/diagnostics": "Diagnostics",
	}

	// And the detail, which reports the section it was opened from while
	// keeping its own heading below the bar.
	list := fetchAs(t, srv, "/admin/activity", session).Body.String()
	if href := regexp.MustCompile(`/admin/activity/(\d+)`).FindString(list); href != "" {
		screens[href] = "Activity log"
	}

	for path, section := range screens {
		body := fetchAs(t, srv, path, session).Body.String()
		if n := strings.Count(body, `<nav class="admin-tabs"`); n != 1 {
			t.Errorf("%s renders %d tab bars, want exactly one", path, n)
			continue
		}
		if !strings.Contains(body, `<a class="admin-tab on" href="`) {
			t.Errorf("%s: no tab is marked current", path)
		}
		if !strings.Contains(body, `<span class="admin-tab-label">`+section+`</span>`) {
			t.Errorf("%s: the bar does not say you are in %q", path, section)
		}
	}
}
