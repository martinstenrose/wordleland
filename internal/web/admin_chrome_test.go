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
			if strings.Contains(body, `class="view`) && strings.Contains(body, ">"+v+"<") {
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

// The pickers sit on the top row beside the mark. They used to flow after
// the links at the foot of the card, and those links differ per page —
// sign-in has two, the two-factor step two others, the reset page one — so
// the pickers sat at a different height on each and jumped when moving
// between pages. Nothing above the top row varies.
func TestAuthPickersSitBesideTheMark(t *testing.T) {
	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	if !strings.Contains(css, ".auth-top {") {
		t.Fatal("the top row has no rule")
	}

	for _, path := range []string{"/", "/forgot-password", "/reset-password?token=x", "/invite?token=x"} {
		body := fetchAs(t, srv, path, nil).Body.String()

		top := strings.Index(body, `class="auth-top"`)
		if top < 0 {
			t.Errorf("%s has no top row", path)
			continue
		}
		row := body[top:]
		row = row[:strings.Index(row, "</div>")]
		if !strings.Contains(row, "brand-lg") {
			t.Errorf("%s: the mark is not on the top row", path)
		}
		if !strings.Contains(row, "signin-switchers") {
			t.Errorf("%s: the pickers are not on the top row", path)
		}

		// Exactly one block, and nothing left behind in the footer.
		if n := strings.Count(body, `class="signin-switchers"`); n != 1 {
			t.Errorf("%s renders %d picker blocks, want 1", path, n)
		}
		if foot := strings.Index(body, `class="signin-foot"`); foot >= 0 && foot < top {
			t.Errorf("%s renders the footer above the top row", path)
		}
	}
}

// The top bar is one thing, built in one place. It used to be assembled per
// page, so the subtitle beside the wordmark appeared on the five board
// views and vanished on Settings and in the admin area.
func TestTopBarIsIdenticalOnEveryPage(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	bars := map[string]string{}
	for _, path := range []string{
		"/today", "/leaderboard", "/months", "/grid", "/players",
		"/settings", "/admin/players", "/admin/pending", "/admin/activity",
		"/admin/diagnostics",
	} {
		body := fetchAs(t, srv, path, session).Body.String()
		bar := body[strings.Index(body, `class="topbar`):]
		bar = bar[:strings.Index(bar, "</header>")]
		// Three things are meant to differ: which view is marked current,
		// the theme and language links, which point back at the page you
		// are on so switching keeps you there, and the sign-out form's
		// CSRF token: these isolated requests do not share a cookie jar.
		bar = strings.ReplaceAll(bar, " on", "")
		bar = strings.ReplaceAll(bar, ` aria-current="page"`, "")
		bar = selfLink.ReplaceAllString(bar, `href="?$1`)
		bar = csrfValue.ReplaceAllString(bar, `name="csrf_token"`)
		bars[path] = bar
	}

	want := bars["/today"]
	if !strings.Contains(want, "brand-sub") {
		t.Fatal("the wordmark has no subtitle to compare")
	}
	for path, got := range bars {
		if got != want {
			t.Errorf("%s renders a different top bar than /today", path)
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

// One nav, shown the same way at both widths, and the wordmark is not a
// second way to reach the front page.
func TestNavIsOneListAndTheWordmarkIsNotALink(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	header := body[strings.Index(body, `class="topbar`):strings.Index(body, "</header>")]

	for _, view := range []string{"Today", "Leaderboard", "Months", "Grid", "Players"} {
		if !strings.Contains(header, ">"+view+"<") {
			t.Errorf("the top bar is missing %q", view)
		}
	}

	// The wordmark carries no href, so there is one control per destination.
	brand := header[strings.Index(header, `class="brand"`):]
	brand = brand[:strings.Index(brand, "</span>")]
	if strings.Contains(brand, "href=") {
		t.Error("the wordmark still links somewhere; Today is already a pill")
	}

	// And the desktop row and the narrow row are the same list.
	mobile := body[strings.Index(body, `class="views-mobile"`):]
	mobile = mobile[:strings.Index(mobile, "</nav>")]
	for _, view := range []string{"Today", "Leaderboard", "Months", "Grid", "Players"} {
		if !strings.Contains(mobile, ">"+view+"<") {
			t.Errorf("the narrow row is missing %q", view)
		}
	}
}
