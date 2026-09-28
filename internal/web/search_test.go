package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// A query matches a player by name or by slug, case-insensitively — the two
// often agree (seedBoard's "harda" is also named "Harda"), but an admin
// searching a slug typed into a URL should still find the right person.
func TestSearchFindsPlayersByNameOrSlug(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	// "Hard" rather than the full names: each label is bolded only where
	// the query actually matched, so the literal names are never
	// contiguous in the markup — see TestSearchHitsMarkTheMatchedText.
	body := fetchAs(t, srv, "/search?q=hard", session).Body.String()
	for _, want := range []string{"<mark>Hard</mark>a", "<mark>Hard</mark>b"} {
		if !strings.Contains(body, want) {
			t.Errorf("q=hard: missing %q", want)
		}
	}
	if strings.Contains(body, "Normala") {
		t.Error("q=hard: matched a player it should not have")
	}

	// "lapsed" only matches on the slug — Lapsed the name still contains
	// it, so this also exercises the case-insensitive compare. The whole
	// name matches here, so it stays contiguous inside one <mark>.
	bySlug := fetchAs(t, srv, "/search?q=LAPSED", session).Body.String()
	if !strings.Contains(bySlug, "<mark>Lapsed</mark>") {
		t.Error("an uppercase query did not match the lowercase slug")
	}
}

// The views every reader has, Settings, and — for an admin only — the four
// admin screens are all searchable by their own displayed label.
func TestSearchFindsPagesByLabelAndAdminOnlyForAdmins(t *testing.T) {
	t.Parallel()

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
	if !strings.Contains(nonAdminBody, "Nothing matches") {
		t.Error("a non-admin's search for an admin-only page shows no no-results message")
	}
}

// The bare page and the overlay's fetch are one implementation, not two:
// "?partial=1" renders only the results block, with none of the page
// around it, for the exact same query the full page would have answered.
func TestSearchPartialRendersOnlyTheResultsBlock(t *testing.T) {
	t.Parallel()

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
	if !strings.Contains(partialBody, "<mark>Hard</mark>a") {
		t.Error("the partial dropped the actual match")
	}
}

// A query nobody typed into yet, or one that matches nothing, both need
// their own copy rather than an empty page that looks broken.
func TestSearchEmptyQueryAndNoResults(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	empty := fetchAs(t, srv, "/search", session).Body.String()
	if !strings.Contains(empty, `href="/today"`) {
		t.Error("an empty query did not list the views to browse")
	}

	none := fetchAs(t, srv, "/search?q=zzzznothingmatchesthis", session).Body.String()
	if !strings.Contains(none, "Nothing matches") {
		t.Error("a query with no matches did not show the no-results copy")
	}
}

// The topbar's search link is the feature's server-rendered baseline: it
// must be there for anyone signed in, and absent exactly where the route
// itself would refuse them — signed out, and on the read-only share view.
func TestTopbarSearchLinkMatchesWhoCanUseTheRoute(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	_, session := adminSession(t, srv)

	signedIn := fetchAs(t, srv, "/leaderboard", session).Body.String()
	if !strings.Contains(signedIn, `href="/search"`) {
		t.Error("no search link for a signed-in reader")
	}

	loggedOut := fetchAs(t, srv, "/", nil).Body.String()
	if strings.Contains(loggedOut, `search-btn`) {
		t.Error("the sign-in page offers a search link nobody can use yet")
	}

	// The read-only share view gets its own search, under the share
	// prefix — the bare /search route would just redirect an anonymous
	// visitor to sign-in.
	shared := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	wantSharedHref := `href="/share/` + slug + `/search"`
	if !strings.Contains(shared, wantSharedHref) {
		t.Errorf("the read-only share view has no search link at %q", wantSharedHref)
	}
	if strings.Contains(shared, `href="/search"`) {
		t.Error("the read-only share view's search link points at the authenticated route")
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
	t.Parallel()

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
// keystroke; the fetching once it is open is htmx's, declared on the
// overlay's input. This checks both halves are wired to the right elements:
// the script reaches the overlay and raises the event the input listens
// for, and the input asks the partial results route — the same one under
// the share prefix, see TestSharedSearchWorksUnderThePrefix — dropping a
// reply still in flight when the next keystroke comes.
func TestSearchOverlayScriptIsWiredUpAndScoped(t *testing.T) {
	t.Parallel()

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
	input, ok := sectionOf(body, `<input type="search" name="q" class="search-overlay-input"`, ">")
	if !ok {
		t.Fatal("the overlay has no named search input")
	}
	for _, attr := range []string{
		`hx-get="/search?partial=1"`,
		`hx-trigger="input changed delay:120ms, search-open"`,
		`hx-target="next .search-overlay-results"`,
		`hx-sync="this:replace"`,
	} {
		if !strings.Contains(input, attr) {
			t.Errorf("the overlay's input is missing %s", attr)
		}
	}

	script := fetchAs(t, srv, "/static/app.js", nil).Body.String()
	if !strings.Contains(script, `getElementById("search-overlay")`) {
		t.Error("the script does not reach the search overlay")
	}
	if !strings.Contains(script, `htmx.trigger(input, "search-open")`) {
		t.Error("opening the overlay does not ask htmx for the empty query's results")
	}
	if strings.Contains(script, `getAttribute("name") === "menu-group"`) {
		t.Error("the search script reaches into the menu-group group, which is a separate concern")
	}
}

// The read-only share view gets search too, under its own prefix, but
// without Settings or the admin screens — neither exists for an anonymous
// reader, and the bare labels ("players", "settings") could otherwise
// coincidentally match a player named after one.
func TestSharedSearchWorksUnderThePrefix(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/search?q=hard", nil).Body.String()
	// The overlay's input asks the same prefixed route, so the palette
	// works for an anonymous reader too.
	if !strings.Contains(body, `hx-get="/share/`+slug+`/search?partial=1"`) {
		t.Error("the overlay's input does not ask the share prefix's search route")
	}
	if fetchAs(t, srv, "/share/"+slug+"/search?q=hard", nil).Code != http.StatusOK {
		t.Fatal("the shared search route is not reachable without a session")
	}
	for _, want := range []string{"<mark>Hard</mark>a", "<mark>Hard</mark>b"} {
		if !strings.Contains(body, want) {
			t.Errorf("shared search q=hard: missing %q", want)
		}
	}
	// Player links stay under the share prefix, the same as every other
	// link on this view — see TestShareBoardMirrorsTheAuthenticatedOne.
	if !strings.Contains(body, `href="/share/`+slug+`/players/harda"`) {
		t.Error("a shared search result does not link back into the share prefix")
	}

	months := fetchAs(t, srv, "/share/"+slug+"/search?q=month", nil).Body.String()
	if !strings.Contains(months, `href="/share/`+slug+`/months"`) {
		t.Error("shared search q=month did not find the Months view under the share prefix")
	}

	for _, query := range []string{"settings", "diagnostics", "activity", "pending"} {
		body := fetchAs(t, srv, "/share/"+slug+"/search?q="+query, nil).Body.String()
		if !strings.Contains(body, "Nothing matches") {
			t.Errorf("shared search q=%s found something an anonymous reader cannot use", query)
		}
	}
}

// A result's icon is what a bullet point used to be, and it must actually
// say something: a player's row draws the person icon, not whichever one a
// nearby page or admin row happens to use.
func TestSearchHitsCarryTheirKindsIcon(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	// The full name, so it matches whole and "Harda" stays one contiguous
	// <mark>Harda</mark> to search for — q=hard would split it across a
	// tag boundary (see TestSearchHitsMarkTheMatchedText).
	body := fetchAs(t, srv, "/search?q=harda", session).Body.String()
	if i := strings.Index(body, "<mark>Harda</mark>"); i < 0 || !strings.Contains(body[:i], `search-tile`) {
		t.Error("a player row does not lead with their tile today")
	}
	if !strings.Contains(body, `<span class="search-meta num">#`) {
		t.Error("a ranked player row does not carry its rank")
	}

	months := fetchAs(t, srv, "/search?q=month", session).Body.String()
	if !strings.Contains(months, symbolPaths["calendar_month"][0]) {
		t.Error("a page row does not draw its view's own icon")
	}

	settings := fetchAs(t, srv, "/search?q=settings", session).Body.String()
	if !strings.Contains(settings, symbolPaths["settings"][0]) {
		t.Error("the Settings row does not draw the gear icon")
	}

	admin := fetchAs(t, srv, "/search?q=diagnostics", session).Body.String()
	if !strings.Contains(admin, symbolPaths["admin_panel_settings"][0]) {
		t.Error("an admin row does not draw the shield icon")
	}

	if strings.Contains(body, `<li><a class="search-hit" href="/players/harda">Harda</a></li>`) {
		t.Error("a result row is still the old bullet-point markup with no icon")
	}
}

// A result marks only the part of its label the query actually matched,
// case-insensitively, preserving the label's own casing rather than the
// query's — so typing "oda" finds "T<mark>oda</mark>y", not a re-cased
// "T<mark>ODA</mark>y" or "T<mark>Toda</mark>y".
func TestSearchHitsMarkTheMatchedText(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/search?q=oda", session).Body.String()
	if !strings.Contains(body, "T<mark>oda</mark>y") {
		t.Error("q=oda did not mark the matched substring inside Today")
	}

	// The query's own case must not leak into the mark: the label keeps
	// its own capitalisation.
	upper := fetchAs(t, srv, "/search?q=ODA", session).Body.String()
	if !strings.Contains(upper, "T<mark>oda</mark>y") {
		t.Error("an uppercase query re-cased the label instead of matching it as-is")
	}

	// An empty query — the overlay's opening state — marks nothing at all.
	empty := fetchAs(t, srv, "/search", session).Body.String()
	if strings.Contains(empty, "<mark>") {
		t.Error("an empty query marked something anyway")
	}
}

// A number is a puzzle: typed with or without its #, it finds that puzzle's
// page with how long ago it was, and nothing typed yet offers today's.
// A number past today is no puzzle at all.
func TestSearchFindsAPuzzleByNumber(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	current := currentPuzzle()

	for _, q := range []string{strconv.Itoa(current - 3), "%23" + strconv.Itoa(current-3)} {
		body := fetchAs(t, srv, "/search?q="+q, session).Body.String()
		if !strings.Contains(body, fmt.Sprintf(`href="/puzzle/%d"`, current-3)) {
			t.Errorf("q=%s did not find puzzle %d", q, current-3)
		}
		if !strings.Contains(body, "3 days ago") {
			t.Errorf("q=%s did not say how long ago the puzzle was", q)
		}
	}

	empty := fetchAs(t, srv, "/search", session).Body.String()
	if !strings.Contains(empty, fmt.Sprintf(`href="/puzzle/%d"`, current)) {
		t.Error("an empty query does not offer today's puzzle")
	}

	future := fetchAs(t, srv, "/search?q="+strconv.Itoa(current+1), session).Body.String()
	if strings.Contains(future, "/puzzle/") {
		t.Error("a puzzle that has not been played yet was found")
	}
}
