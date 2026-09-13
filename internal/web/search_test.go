package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// A query matches a player by name or by slug, case-insensitively — the two
// often agree (seedBoard's "harda" is also named "Harda"), but an admin
// searching a slug typed into a URL should still find the right person.
func TestSearchFindsPlayersByNameOrSlug(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/search?q=hard", session).Body.String()
	for _, want := range []string{"Harda", "Hardb"} {
		if !strings.Contains(body, want) {
			t.Errorf("q=hard: missing %q", want)
		}
	}
	if strings.Contains(body, "Normala") {
		t.Error("q=hard: matched a player it should not have")
	}

	// "lapsed" only matches on the slug — Lapsed the name still contains
	// it, so this also exercises the case-insensitive compare.
	bySlug := fetchAs(t, srv, "/search?q=LAPSED", session).Body.String()
	if !strings.Contains(bySlug, "Lapsed") {
		t.Error("an uppercase query did not match the lowercase slug")
	}
}

// The views every reader has, Settings, and — for an admin only — the four
// admin screens are all searchable by their own displayed label.
func TestSearchFindsPagesByLabelAndAdminOnlyForAdmins(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, adminCookie := adminSession(t, srv)
	ordinary, err := store.CreateUser(context.Background(), srv.db, store.AdminActor(admin.ID), "player@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	ordinaryCookie := signIn(t, srv, ordinary.ID)

	months := fetchAs(t, srv, "/search?q=month", adminCookie).Body.String()
	if !strings.Contains(months, `href="/months"`) {
		t.Error("q=month did not find the Months view")
	}

	adminBody := fetchAs(t, srv, "/search?q=diagnostics", adminCookie).Body.String()
	if !strings.Contains(adminBody, `href="/admin/diagnostics"`) {
		t.Error("an admin's search did not find the diagnostics screen")
	}

	// "diagnostics" matches nothing else a non-admin can see, so this is a
	// real no-results case rather than a coincidental match elsewhere.
	nonAdminBody := fetchAs(t, srv, "/search?q=diagnostics", ordinaryCookie).Body.String()
	if strings.Contains(nonAdminBody, `href="/admin/diagnostics"`) {
		t.Error("a non-admin's search found an admin-only screen")
	}
	if !strings.Contains(nonAdminBody, "No matches for that search.") {
		t.Error("a non-admin's search for an admin-only page shows no no-results message")
	}
}

// The bare page and the overlay's fetch are one implementation, not two:
// "?partial=1" renders only the results block, with none of the page
// around it, for the exact same query the full page would have answered.
func TestSearchPartialRendersOnlyTheResultsBlock(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	full := fetchAs(t, srv, "/search?q=hard", session).Body.String()
	if !strings.Contains(full, "<html") || !strings.Contains(full, "topbar") {
		t.Error("the full page lost its layout")
	}

	partial := fetchAs(t, srv, "/search?q=hard&partial=1", session)
	if partial.Code != http.StatusOK {
		t.Fatalf("GET /search?partial=1 = %d", partial.Code)
	}
	partialBody := partial.Body.String()
	if strings.Contains(partialBody, "<html") || strings.Contains(partialBody, "topbar") {
		t.Error("the partial rendered the page layout around the results")
	}
	if !strings.Contains(partialBody, "Harda") {
		t.Error("the partial dropped the actual match")
	}
}

// A query nobody typed into yet, or one that matches nothing, both need
// their own copy rather than an empty page that looks broken.
func TestSearchEmptyQueryAndNoResults(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	empty := fetchAs(t, srv, "/search", session).Body.String()
	if !strings.Contains(empty, `href="/today"`) {
		t.Error("an empty query did not list the views to browse")
	}

	none := fetchAs(t, srv, "/search?q=zzzznothingmatchesthis", session).Body.String()
	if !strings.Contains(none, "No matches for that search.") {
		t.Error("a query with no matches did not show the no-results copy")
	}
}

// The topbar's search link is the feature's server-rendered baseline: it
// must be there for anyone signed in, and absent exactly where the route
// itself would refuse them — signed out, and on the read-only share view.
func TestTopbarSearchLinkMatchesWhoCanUseTheRoute(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	_, session := adminSession(t, srv)

	signedIn := fetchAs(t, srv, "/leaderboard", session).Body.String()
	if !strings.Contains(signedIn, `href="/search"`) {
		t.Error("no search link for a signed-in reader")
	}

	loggedOut := fetchAs(t, srv, "/", nil).Body.String()
	if strings.Contains(loggedOut, `href="/search"`) {
		t.Error("the sign-in page offers a search link nobody can use yet")
	}

	shared := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	if strings.Contains(shared, `href="/search"`) {
		t.Error("the read-only share view offers a search link the route would reject")
	}
}

// The overlay must default to hidden across a browser's own cascade, not
// just in the markup: an author rule and the UA stylesheet's
// [hidden]{display:none} tie on specificity, and the author rule wins that
// tie regardless of source order — so a bare ".search-overlay { display:
// flex }" showed the overlay on every page load and left app.js's `hidden
// = true` unable to hide it again. This pins the fix: the flex declaration
// must live on a :not([hidden]) rule, and the bare selector must not
// declare display at all.
func TestSearchOverlayDisplayYieldsToTheHiddenAttribute(t *testing.T) {
	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	if !strings.Contains(css, ".search-overlay:not([hidden])") {
		t.Fatal("no .search-overlay:not([hidden]) rule — the overlay's display is not scoped away from [hidden]")
	}

	start := strings.Index(css, ".search-overlay {")
	if start < 0 {
		t.Fatal("no bare .search-overlay rule found")
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatal("the bare .search-overlay rule is never closed")
	}
	if bare := css[start : start+end]; strings.Contains(bare, "display") {
		t.Errorf("the bare .search-overlay rule sets display itself, which beats [hidden] on a specificity tie: %q", bare)
	}
}

// app.js's search block is the only thing that can open an overlay on a
// keystroke; this checks it is wired to the right elements and does not
// reach into the picker menus, which are a separate concern (see
// TestPopupPositioningScriptIsWiredUpAndScoped).
func TestSearchOverlayScriptIsWiredUpAndScoped(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	if strings.Count(body, `id="search-overlay"`) != 1 {
		t.Error("expected exactly one search overlay in the page")
	}
	if !strings.Contains(body, `<div id="search-overlay" class="search-overlay" hidden>`) {
		t.Error("the overlay is not hidden by default")
	}

	script := fetchAs(t, srv, "/static/app.js", nil).Body.String()
	if !strings.Contains(script, `getElementById("search-overlay")`) {
		t.Error("the script does not reach the search overlay")
	}
	if !strings.Contains(script, `"/search?partial=1&q="`) {
		t.Error("the overlay does not fetch the partial results route")
	}
	if strings.Contains(script, `getAttribute("name") === "topbar-menu"`) {
		t.Error("the search script reaches into the topbar-menu group, which is a separate concern")
	}
}
