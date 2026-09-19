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

// The chart note, calendar legend, and recent-strip legend all use .hint
// inside a .panel; that rule should match the Leaderboard's footer-note
// styling so explanatory copy reads consistently across pages.
func TestPlayerPanelHintMatchesLeaderboardFootNote(t *testing.T) {
	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	at := strings.Index(css, ".panel .hint")
	if at < 0 {
		t.Fatal("nothing styles the player panel's hint text")
	}
	rule := css[at:]
	rule = rule[:strings.Index(rule, "}")]
	for _, want := range []string{"font-size: 10.5px", "color: var(--color-text-35)"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the panel hint rule does not set %q", want)
		}
	}
}

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

	page := withoutRoster(fetch(t, srv, "/share/"+slug+"/p/thin").Body.String())

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
	strip, ok := sectionOf(page, `<ol class="strip">`, "</ol>")
	if !ok {
		t.Fatal("the strip is missing")
	}
	if got := strings.Count(strip, `<details class="cell-pop" name="popup">`); got != 26 {
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

// Every day in the calendar opens the same kind of popup, and says what the
// day took as well as which puzzle it was: the square is blank, so unlike a
// cell in the strip there is no digit on it to read the result off.
func TestCalendarSquaresOpenAPopupWithTheResult(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/p/harda").Body.String()
	calendar, ok := sectionOf(page, `<ol class="calendar">`, "</ol>")
	if !ok {
		t.Fatal("the calendar is missing")
	}

	// One per day played, and none on the padding or on a day not played —
	// there is nothing to open for a day with no result.
	if got := strings.Count(calendar, `<details class="cell-pop" name="popup">`); got != 26 {
		t.Errorf("expected 26 popups, one per day played, got %d", got)
	}
	for _, class := range []string{"cal pad", "cal miss"} {
		at := strings.Index(calendar, class)
		if at < 0 {
			continue
		}
		if strings.Contains(calendar[at:at+120], "cell-pop") {
			t.Errorf("a %q square opens a popup", class)
		}
	}

	current := currentPuzzle()
	date, err := wordle.DateForPuzzle(current)
	if err != nil {
		t.Fatalf("DateForPuzzle(%d): %v", current, err)
	}
	// harda solves everything in three, so the detail names the puzzle, the
	// day, and what it took.
	want := fmt.Sprintf("#%d (%s) · 3 guesses", current, date.Format("2006-01-02"))
	if !strings.Contains(calendar, want) {
		t.Errorf("the calendar popup does not show %q", want)
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
	// The design gives the panel no back-link: the bar above it — the name,
	// which is also the control that opens the roster — is how you move
	// between players, and the mark is how you leave. What matters is that
	// the page is not a dead end.
	body := page.Body.String()
	if !strings.Contains(body, `class="menu-row switcher-row`) {
		t.Error("the player page has no roster to move with")
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
	// The reason chip itself is not shown on the player page at all: the
	// heading already carries the trait, and the copy above says why the
	// chart is missing.
	if strings.Contains(page, "no recent puzzles") {
		t.Error("the reason chip should not show on the player page")
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

// withoutRoster cuts the open-the-roster menu out of a player page.
//
// Every player is listed in it with their own rank and average, so a figure
// found anywhere on the page is not necessarily a figure about the player the
// page is about — which is the only thing the tests below are asking.
func withoutRoster(page string) string {
	i := strings.Index(page, `<div class="switcher-panel">`)
	if i < 0 {
		return page
	}
	j := strings.Index(page[i:], "</details>")
	if j < 0 {
		return page
	}
	return page[:i] + page[i+j:]
}

// The roster lists everyone, so it is a second place the withheld figures
// could leak out of — and the one nobody would think to look at.
func TestTheRosterWithholdsFiguresBelowTheThreshold(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/p/harda").Body.String()
	i := strings.Index(page, `<div class="switcher-panel">`)
	if i < 0 {
		t.Fatal("the player page has no roster")
	}
	menu := page[i : i+strings.Index(page[i:], "</details>")]

	row := menu[strings.Index(menu, "/p/thin"):]
	row = row[:strings.Index(row, "</a>")]
	if !strings.Contains(row, `<span class="switcher-avg num">—</span>`) {
		t.Errorf("the roster gives a player below the threshold an average: %s", row)
	}
	if !strings.Contains(row, `<span class="switcher-rank">—</span>`) {
		t.Errorf("the roster gives an unranked player a rank: %s", row)
	}
	// And a ranked one still has both, or the dash above means nothing.
	ranked := menu[strings.Index(menu, "/p/harda"):]
	ranked = ranked[:strings.Index(ranked, "</a>")]
	if strings.Contains(ranked, "—") {
		t.Errorf("a ranked player's figures are withheld too: %s", ranked)
	}
}

// The roster's script asks for the card alone, and what it gets has to be the
// same card the full page draws — otherwise picking a name with a script and
// picking one without it land on two different pages.
func TestThePlayerCardIsTheSameWholeOrInPart(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	full := fetch(t, srv, "/share/"+slug+"/p/harda").Body.String()
	part := fetch(t, srv, "/share/"+slug+"/p/harda?partial=1").Body.String()

	if strings.Contains(part, "<html") || strings.Contains(part, `class="sidebar"`) {
		t.Error("the partial carries the page around the card")
	}
	if !strings.Contains(part, `<h1 class="switcher-label">Harda`) {
		t.Error("the partial is not the player's card")
	}

	card := full[strings.Index(full, `<section class="card`):]
	card = card[:strings.LastIndex(card, "</section>")+len("</section>")]
	if strings.TrimSpace(part) != strings.TrimSpace(card) {
		t.Error("the card differs between the whole page and the partial")
	}
}
