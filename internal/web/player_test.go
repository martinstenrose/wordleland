package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

func TestPlayerPageShowsTheSameFiguresAsTheBoard(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	board := fetch(t, srv, "/share/"+slug+"/board").Body.String()
	row := rowFor(t, board, "harda")

	rec := fetch(t, srv, "/share/"+slug+"/p/harda")
	if rec.Code != http.StatusOK {
		t.Fatalf("player page = %d", rec.Code)
	}
	page := rec.Body.String()

	// The board says 3.00; the page must not say something else, or the two
	// views are computing the roster on different terms.
	if !strings.Contains(row, "3.00") {
		t.Fatalf("fixture changed: the board no longer shows 3.00\n%s", row)
	}
	if !strings.Contains(page, "3.00") {
		t.Error("the player page does not show the average the board shows")
	}
	if !strings.Contains(page, "Harda") {
		t.Error("the player page does not name the player")
	}
}

// The filter has to mean the same thing here as on the board: a player the
// board left out has no page, rather than an empty one contradicting it.
func TestPlayerPageHonoursTheFilter(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	if got := fetch(t, srv, "/share/"+slug+"/p/normala").Code; got != http.StatusOK {
		t.Errorf("unfiltered player page = %d, want 200", got)
	}
	if got := fetch(t, srv, "/share/"+slug+"/p/normala?mode=hard").Code; got != http.StatusNotFound {
		t.Errorf("player page under mode=hard = %d, want 404 — the board excludes them", got)
	}
	if got := fetch(t, srv, "/share/"+slug+"/p/harda?mode=hard").Code; got != http.StatusOK {
		t.Errorf("hard-mode player page under mode=hard = %d, want 200", got)
	}
}

func TestUnknownPlayerIs404(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{
		"/share/" + slug + "/p/nobody",
		"/share/" + slug + "/p/NotASlug",
	} {
		if got := fetch(t, srv, path).Code; got != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, got)
		}
	}
}

// Below the ranking threshold the derived figures are withheld here too,
// and the raw results are shown instead of charts.
func TestThinPlayerGetsScoresRatherThanCharts(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/p/thin").Body.String()

	if strings.Contains(page, "3.00") {
		t.Error("a player below the ranking threshold is showing a computed average")
	}
	if !strings.Contains(page, "Too few games to chart") {
		t.Error("the page does not explain why the charts are missing")
	}
	if strings.Contains(page, `class="chart"`) {
		t.Error("a chart rendered for a player with too little history")
	}
	// The individual results are still there.
	if !strings.Contains(page, `class="strip"`) {
		t.Error("the raw results are missing")
	}
}

func TestPlayerPageWithNoGamesExplainsItself(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()

	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	if _, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Newcomer", "newcomer"); err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	page := fetch(t, srv, "/share/"+slug+"/p/newcomer").Body.String()
	if !strings.Contains(page, "No results yet for this player") {
		t.Errorf("a player with no games gets no explanation:\n%s", page)
	}
	if strings.Contains(page, `class="dist"`) {
		t.Error("a distribution rendered for a player with no games")
	}
}

// The shared page must stay read-only and keep its links under the prefix,
// exactly as the shared board does.
func TestSharedPlayerPageExposesNoAuthenticatedSurface(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/p/harda").Body.String()
	if strings.Contains(page, "/logout") {
		t.Error("the shared player page offers sign-out")
	}
	if strings.Contains(page, `href="/leaderboard`) {
		t.Error("the shared player page links into authenticated routing")
	}
	if !strings.Contains(page, `href="/share/`+slug+`/`) {
		t.Error("the shared player page has no link back to the shared board")
	}
}

// The authenticated page is reachable only with a session, and links back to
// /leaderboard rather than to a share URL.
func TestAuthenticatedPlayerPageRequiresASession(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	if got := fetchAs(t, srv, "/p/harda", nil).Code; got != http.StatusSeeOther {
		t.Errorf("anonymous GET /p/harda = %d, want a redirect to login", got)
	}

	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	page := fetchAs(t, srv, "/p/harda", signIn(t, srv, admin.ID))
	if page.Code != http.StatusOK {
		t.Fatalf("signed-in GET /p/harda = %d", page.Code)
	}
	// The design gives the panel no back-link: the picker above it is how
	// you move between players, and the mark is how you leave. What matters
	// is that the page is not a dead end.
	body := page.Body.String()
	if !strings.Contains(body, `class="pick`) {
		t.Error("the player page has no picker to move with")
	}
	if !strings.Contains(body, `class="brand"`) {
		t.Error("the player page has no way back out")
	}
}

// A player with plenty of history who simply stopped is not short of games,
// and must not be told they are.
func TestLapsedPlayerIsNotCalledThin(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/p/lapsed").Body.String()
	if strings.Contains(page, "Too few games") {
		t.Error("a player with a long history is described as having too few games")
	}
	if !strings.Contains(page, "No games in the last 30 days") {
		t.Error("the page does not say why the chart is missing")
	}
	// And the reason chip still matches the board's.
	if !strings.Contains(page, "no recent games") {
		t.Error("the page does not carry the board's reason")
	}
}
