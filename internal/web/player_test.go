package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
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
	if !strings.Contains(page, "Too few puzzles to chart") {
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

// Each played day in the strip opens its own popup naming the puzzle and
// its date — the two things the box itself cannot show. The guess count and
// hard mode are already the box's label, so the popup does not repeat them.
func TestRecentStripCellsOpenAPopupWithThePuzzleDetail(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/p/harda").Body.String()

	// harda plays every one of the 26 puzzles in the fixture's window, all
	// hard mode, all in 3 guesses — so every cell in the strip opens.
	if got := strings.Count(page, `<details class="cell-pop" name="popup">`); got != 26 {
		t.Errorf("expected 26 popups, one per played day, got %d", got)
	}
	// The asterisk marks hard mode on the box itself, so the popup needs no
	// row for it.
	if !strings.Contains(page, ">3*<") {
		t.Error("a hard-mode result does not carry the asterisk in its box")
	}

	current := currentPuzzle()
	date, err := wordle.DateForPuzzle(current)
	if err != nil {
		t.Fatalf("DateForPuzzle(%d): %v", current, err)
	}
	want := fmt.Sprintf(">#%d (%s)<", current, date.Format("2006-01-02"))
	if !strings.Contains(page, want) {
		t.Errorf("the popup does not show %q", want)
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
	if strings.Contains(page, "Too few puzzles") {
		t.Error("a player with a long history is described as having too few puzzles")
	}
	if !strings.Contains(page, "No puzzles in the last 30 days") {
		t.Error("the page does not say why the chart is missing")
	}
	// And the reason chip still matches the board's.
	if !strings.Contains(page, "no recent puzzles") {
		t.Error("the page does not carry the board's reason")
	}
}

// The rank-by-month chart plots the current month alongside finished ones,
// but its rank can still move: the segment leading into it must be dashed
// rather than drawn as a settled result.
func TestBuildMonthRanksDashesTheSegmentIntoAnUnfinishedMonth(t *testing.T) {
	const playerID = 1
	others := []stats.MonthPlayer{{Player: store.Player{ID: 2}}, {Player: store.Player{ID: 3}}}

	monthWith := func(year int, month time.Month, rank int) stats.Month {
		return stats.Month{
			Year:  year,
			Month: month,
			Ranked: append([]stats.MonthPlayer{
				{Player: store.Player{ID: playerID}, Rank: rank},
			}, others...),
		}
	}

	// Newest first, matching stats.ComputeMonths's documented order.
	months := []stats.Month{
		monthWith(2026, time.September, 1),
		monthWith(2026, time.August, 2),
		monthWith(2026, time.July, 3),
	}

	srv := testServer(t)
	tr := translator{locale: "en", strings: srv.catalogues["en"], fallback: srv.catalogues["en"]}

	t.Run("current month still running", func(t *testing.T) {
		now := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)
		_, path, dashedPath := buildMonthRanks(months, playerID, now, tr)

		wantPath := "M0.0 64.0 L150.0 32.0"
		wantDashed := "M150.0 32.0 L300.0 0.0"
		if path != wantPath {
			t.Errorf("path = %q, want %q", path, wantPath)
		}
		if dashedPath != wantDashed {
			t.Errorf("dashedPath = %q, want %q", dashedPath, wantDashed)
		}
	})

	t.Run("current month finished", func(t *testing.T) {
		now := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
		_, path, dashedPath := buildMonthRanks(months, playerID, now, tr)

		wantPath := "M0.0 64.0 L150.0 32.0 L300.0 0.0"
		if path != wantPath {
			t.Errorf("path = %q, want %q", path, wantPath)
		}
		if dashedPath != "" {
			t.Errorf("dashedPath = %q, want none once the month has closed", dashedPath)
		}
	})
}
