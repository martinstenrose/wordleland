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

// The three admin screens are one area and have to look like it: same tab
// row in the same place, same card header shape.
func TestAdminScreensShareTheirChrome(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	tag := regexp.MustCompile(`<(section|nav|div|h1|p)[^>]*class="([^"]*)"`)
	shapes := map[string][]string{}

	for _, path := range []string{"/admin/players", "/admin/pending", "/admin/activity"} {
		body := fetchAs(t, srv, path, session).Body.String()
		start := strings.Index(body, `<section class="card"`)
		if start < 0 {
			t.Fatalf("%s has no card section", path)
		}
		head := body[start:]
		if cut := strings.Index(head, "</div>"); cut > 0 {
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

// The pickers sit in the top bar, in one fixed place on every page. They used
// to flow after the links at the foot of the auth card, and those links differ
// per page — sign-in has two, the two-factor step two others, the reset page
// one — so the pickers sat at a different height on each and jumped when
// moving between pages. Then they moved to a row of their own at the top of
// each card, which fixed the jumping and left every auth page carrying its own
// copy of the chrome; the shell carries it now, and the cards carry none.
func TestAuthPickersAreInTheTopBarOnly(t *testing.T) {
	srv := testServer(t)

	for _, path := range []string{"/", "/forgot-password", "/reset-password?token=x", "/invite?token=x"} {
		body := fetchAs(t, srv, path, nil).Body.String()

		bar := strings.Index(body, `<header class="topbar">`)
		if bar < 0 {
			t.Errorf("%s has no top bar", path)
			continue
		}
		for _, control := range []string{`class="theme-track"`, `<details class="menu" name="topbar-menu">`} {
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

// A visitor who has not signed in is not told how much history exists.
func TestTopBarSubtitleIsNotShownSignedOut(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	if body := fetchAs(t, srv, "/", nil).Body.String(); strings.Contains(body, "brand-sub") {
		t.Error("the sign-in page reports the size of the board")
	}
}

// The views live in the rail, with a brand link to Today above them.
func TestTheRailCarriesEveryViewAndTheWordmark(t *testing.T) {
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

	if !strings.Contains(rail, `<a class="brand" href="/today">`) {
		t.Error("the wordmark does not link to Today")
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
			t.Errorf("the rail lists %q, which the page's own strip carries", screen)
		}
	}

	// And that strip is on the page.
	if !strings.Contains(body, `class="pill-nav"`) {
		t.Error("the admin screen has no tab strip")
	}
}
