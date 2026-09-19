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

// The admin screens are one area and have to look like it: the same card, the
// same tab row in the same place, the same header. Their subtitles are the
// exception — Players and Pending count what they hold, and Activity and
// Diagnostics have nothing to count — so the comparison stops at the head.
func TestAdminScreensShareTheirChrome(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	tag := regexp.MustCompile(`<(section|nav|div|h1|p)[^>]*class="([^"]*)"`)
	shapes := map[string][]string{}

	for _, path := range []string{"/admin/players", "/admin/pending", "/admin/activity"} {
		body := fetchAs(t, srv, path, session).Body.String()
		start := strings.Index(body, `<section class="card`)
		if start < 0 {
			t.Fatalf("%s has no card section", path)
		}
		head := body[start:]
		if cut := strings.Index(head, "<h1"); cut > 0 {
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
	// The caption itself is left alone: it is a field label like the rest.
	if strings.Contains(css, ".form label.switch {") {
		t.Error("the switch caption overrides .form label; it should not")
	}

	// And the copy itself is written in sentence case, so the reset is the
	// only thing between it and capitals.
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	body := fetchAs(t, srv, "/admin/players/harda", session).Body.String()
	for _, want := range []string{
		"Still in the group",
		"Clearing it retires the player and keeps their history.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
	if strings.Contains(body, "Membership, not recency") {
		t.Error("the hint still carries the dropped first sentence")
	}
}

// The pickers sit in the chrome, in one fixed place on every page. They used
// to flow after the links at the foot of the auth card, and those links differ
// per page — sign-in has two, the two-factor step two others, the reset page
// one — so the pickers sat at a different height on each and jumped when
// moving between pages. Then they moved to a row of their own at the top of
// each card, which fixed the jumping and left every auth page carrying its own
// copy of the chrome. The chrome carries it now, and the cards carry none —
// the top bar inside the application, its own much shorter row at the door.
func TestAuthPickersAreInTheChromeOnly(t *testing.T) {
	srv := testServer(t)

	for _, path := range []string{"/", "/forgot-password", "/reset-password?token=x", "/invite?token=x"} {
		body := fetchAs(t, srv, path, nil).Body.String()

		bar := strings.Index(body, `<header class="auth-chrome">`)
		if bar < 0 {
			t.Errorf("%s has no chrome row", path)
			continue
		}
		// And no application shell around it: a rail emptied to the wordmark
		// is a navigation with nothing in it.
		if strings.Contains(body, `class="sidebar"`) {
			t.Errorf("%s draws the application rail", path)
		}
		for _, control := range []string{`class="theme-track"`, `<details class="menu" name="menu-group">`} {
			if n := strings.Count(body, control); n != 1 {
				t.Errorf("%s renders %s %d times, want 1", path, control, n)
			}
			if strings.Index(body, control) < bar {
				t.Errorf("%s renders %s outside the top bar", path, control)
			}
		}
	}
}

// The chrome is one thing, built in one place. It used to be assembled per
// page, so the subtitle beside the wordmark appeared on the five board
// views and vanished on Settings and in the admin area.
//
// The drawer is cut out before comparing: it carries the rail's rows, and
// those are meant to differ — inside the admin area the four admin screens
// hang under the admin row. That the drawer and the rail agree is
// TestTheDrawerCarriesTheSameRowsAsTheRail's job.
func TestTheChromeIsIdenticalOnEveryPage(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	chromes := map[string]string{}
	for _, path := range []string{
		"/today", "/leaderboard", "/months", "/grid", "/players",
		"/settings", "/admin/players", "/admin/pending", "/admin/activity",
		"/admin/diagnostics",
	} {
		body := fetchAs(t, srv, path, session).Body.String()

		bar := body[strings.Index(body, `class="topbar`):]
		bar = bar[:strings.Index(bar, "</header>")]
		if open := strings.Index(bar, `<details class="drawer"`); open >= 0 {
			bar = bar[:open] + bar[strings.Index(bar, "</details>")+len("</details>"):]
		}
		// The wordmark and its subtitle live in the rail now, and they are
		// the part of it that must not vary.
		brand := body[strings.Index(body, `<nav class="sidebar"`):]
		brand = brand[:strings.Index(brand, "</a>")]

		combined := bar + brand
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
	if !strings.Contains(want, "brand-sub") {
		t.Fatal("the wordmark has no subtitle to compare")
	}
	for path, got := range chromes {
		if got != want {
			t.Errorf("%s renders different chrome than /today", path)
		}
	}
}

// How much history exists is said once, beside the wordmark, and repeated in
// figures by the card's own panel.
//
// This test used to assert the opposite: that a visitor who has not signed in
// is not told the size of the board. The panel beside the sign-in form already
// says "398 puzzles logged over 45 days", so that was never true in practice,
// and on a phone — where the panel does not fit — the wordmark is the only
// place it is said at all.
func TestTheSignInPageSaysHowBigTheGroupIs(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	body := fetchAs(t, srv, "/", nil).Body.String()
	if !strings.Contains(body, "brand-sub") {
		t.Error("the sign-in page does not say how much history there is")
	}
	// And a 404 still says nothing: that page is chrome for a stranger with
	// no reason to be told anything about this installation.
	if strings.Contains(fetchAs(t, srv, "/no/such/page", nil).Body.String(), "brand-sub") {
		t.Error("an error page reports the size of the board")
	}
}

// The views live in the rail; the wordmark lives in the bar above it, where
// it holds one place at every width — the rail is only ever navigation.
func TestTheRailCarriesEveryViewAndTheBarTheWordmark(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	rail := body[strings.Index(body, `<nav class="sidebar"`):]
	rail = rail[:strings.Index(rail, "</nav>")]

	for _, view := range []string{"Today", "Leaderboard", "Months", "Grid", "Players"} {
		if !strings.Contains(rail, ">"+view+"<") {
			t.Errorf("the rail is missing %q", view)
		}
	}
	if strings.Contains(rail, `class="brand"`) {
		t.Error("the rail carries the wordmark, which belongs in the bar")
	}

	bar := body[strings.Index(body, `<header class="topbar">`):]
	bar = bar[:strings.Index(bar, "</header>")]
	if !strings.Contains(bar, `<a class="brand" href="/today">`) {
		t.Error("the bar's wordmark does not link to Today")
	}

	// And the bar is above the shell rather than inside it, which is what
	// lets it span the rail as well as the page.
	if strings.Index(body, `<header class="topbar">`) > strings.Index(body, `<div class="shell">`) {
		t.Error("the bar is rendered inside the shell rather than above it")
	}
}

// The rail says where in the application you are; which screen inside the
// admin area you are on is the strip at the top of that screen's job. One row
// for the area, not five — and the four screens listed in both places would
// be two lists of the same destinations to keep in step.
func TestTheRailCarriesOneAdminRow(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/pending", session).Body.String()
	rail := body[strings.Index(body, `<nav class="sidebar"`):]
	rail = rail[:strings.Index(rail, "</nav>")]

	if !strings.Contains(rail, ">Admin area<") {
		t.Error("the rail does not offer the admin area")
	}
	for _, screen := range []string{"Pending results", "Activity log", "Diagnostics"} {
		if strings.Contains(rail, ">"+screen+"<") {
			t.Errorf("the rail lists %q, which the page's own bar carries", screen)
		}
	}

	// And that bar is on the page — the heading, which is also the control
	// that changes which section the card is.
	if !strings.Contains(body, `<details class="switcher menu" name="menu-group">`) {
		t.Error("the admin screen has no section bar")
	}
	if !strings.Contains(body, `<h1 class="switcher-label">Pending results</h1>`) {
		t.Error("the bar does not carry the section's name as the page's heading")
	}
}

// Every admin screen carries the bar, including the one that is not a section
// of its own.
//
// The activity detail is the sixth admin screen and the easy one to forget:
// it is a row of the activity log opened up, so it has a subject of its own
// and does not appear in the list the other five are built from. Losing the
// bar there would leave one screen with no way back out of it except the
// browser's own, which is the failure this pins.
func TestEveryAdminScreenCarriesTheSectionBar(t *testing.T) {
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
		if n := strings.Count(body, `<details class="switcher menu"`); n != 1 {
			t.Errorf("%s renders %d section bars, want exactly one", path, n)
			continue
		}
		if !strings.Contains(body, `<h1 class="switcher-label">`+section+`</h1>`) {
			t.Errorf("%s: the bar does not say you are in %q", path, section)
		}
	}
}
