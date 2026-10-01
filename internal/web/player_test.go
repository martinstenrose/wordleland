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

// A card's hint on a player's page — the strip's legend, the heatmap's, the
// reason a chart is missing — reads like the notes at the foot of the
// leaderboard: the same size, the same muted colour.
func TestPlayerPanelHintMatchesLeaderboardFootNote(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	hint := cssRule(t, css, ".panel-card .hint {")
	notes := cssRule(t, css, ".board-card.card > .b-notes {")
	for _, want := range []string{"font-size: 12px", "color: var(--color-muted)"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the panel hint does not set %q: %s", want, hint)
		}
		if !strings.Contains(notes, want) {
			t.Errorf("the board's notes do not set %q: %s", want, notes)
		}
	}
}

func TestPlayerPageShowsTheSameFiguresAsTheBoard(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	board := fetch(t, srv, "/share/"+slug+"/board").Body.String()
	row := rowFor(t, board, "harda")

	rec := fetch(t, srv, "/share/"+slug+"/players/harda")
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
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	if got := fetch(t, srv, "/share/"+slug+"/players/normala").Code; got != http.StatusOK {
		t.Errorf("unfiltered player page = %d, want 200", got)
	}
	if got := fetch(t, srv, "/share/"+slug+"/players/normala?mode=hard").Code; got != http.StatusNotFound {
		t.Errorf("player page under mode=hard = %d, want 404 — the board excludes them", got)
	}
	if got := fetch(t, srv, "/share/"+slug+"/players/harda?mode=hard").Code; got != http.StatusOK {
		t.Errorf("hard-mode player page under mode=hard = %d, want 200", got)
	}
}

func TestUnknownPlayerIs404(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{
		"/share/" + slug + "/players/nobody",
		"/share/" + slug + "/players/NotASlug",
	} {
		if got := fetch(t, srv, path).Code; got != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, got)
		}
	}
}

// Below the ranking threshold the derived figures are withheld here too,
// and the raw results are shown instead of charts.
func TestThinPlayerGetsScoresRatherThanCharts(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := withoutRoster(fetch(t, srv, "/share/"+slug+"/players/thin").Body.String())

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

// A player with a rank in only one month has no line to draw: the panel
// stays, saying why it is empty, rather than leaving a hole in the grid.
//
// Not "thin" from seedBoard: their last four days cross into a new month
// on the first three days of one, which is two months with a rank. This
// player's games stay inside the current month whatever today is.
func TestOneMonthPlayerGetsNoRankChart(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	ctx := context.Background()
	p, err := store.CreatePlayer(ctx, srv.db, store.SystemActor(), "Fresh", "fresh")
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	now := time.Now()
	current := wordle.PuzzleForDate(now)
	first := max(current-3, wordle.PuzzleForDate(time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, now.Location())))
	for puzzle := first; puzzle <= current; puzzle++ {
		date, err := wordle.DateForPuzzle(puzzle)
		if err != nil {
			t.Fatalf("DateForPuzzle: %v", err)
		}
		g := 3
		if _, _, err := store.UpsertResult(ctx, srv.db, store.Result{
			PuzzleNo: puzzle, Date: date, PlayerID: p.ID, Guesses: &g, Solved: true,
		}, nil, nil); err != nil {
			t.Fatalf("UpsertResult: %v", err)
		}
	}

	page := withoutRoster(fetch(t, srv, "/share/"+slug+"/players/fresh").Body.String())
	if strings.Contains(page, `class="rank-plot"`) {
		t.Error("a rank-by-month chart rendered for a player ranked in one month")
	}
	if !strings.Contains(page, "Rank by month") || !strings.Contains(page, "Not enough months yet") {
		t.Error("the rank-by-month panel does not explain why it is empty")
	}
}

// Each played day in the strip opens its own popup: the puzzle, the day and
// what it took, how the group did, and the way to the day's page.
func TestRecentStripCellsOpenAPopupWithThePuzzleDetail(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/players/harda").Body.String()

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
	// Which puzzle, the day, and what it took; then the day's own page.
	want := fmt.Sprintf(">#%d · %s %d %s · 3 guesses *<", current, date.Weekday().String()[:3], date.Day(), date.Format("Jan"))
	if !strings.Contains(page, want) {
		t.Errorf("the popup does not show %q", want)
	}
	if !strings.Contains(page, fmt.Sprintf(`<a class="day-popup-open" href="/share/%s/puzzle/%d">Open puzzle ›</a>`, slug, current)) {
		t.Error("the popup does not open the day's puzzle")
	}
}

// Every day in the calendar opens the same kind of popup, and says what the
// day took as well as which puzzle it was: the square is blank, so unlike a
// cell in the strip there is no digit on it to read the result off.
func TestCalendarSquaresOpenAPopupWithTheResult(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/players/harda").Body.String()
	calendar, ok := sectionOf(page, `<ol class="heat">`, "</ol>")
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
	want := fmt.Sprintf("#%d · %s %d %s · 3 guesses", current, date.Weekday().String()[:3], date.Day(), date.Format("Jan"))
	if !strings.Contains(calendar, want) {
		t.Errorf("the calendar popup does not show %q", want)
	}
}

func TestPlayerPageWithNoGamesExplainsItself(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()

	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	if _, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Newcomer", "newcomer"); err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	page := fetch(t, srv, "/share/"+slug+"/players/newcomer").Body.String()
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
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/players/harda").Body.String()
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
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)

	if got := fetchAs(t, srv, "/players/harda", nil).Code; got != http.StatusSeeOther {
		t.Errorf("anonymous GET /players/harda = %d, want a redirect to login", got)
	}

	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	page := fetchAs(t, srv, "/players/harda", signIn(t, srv, admin.ID))
	if page.Code != http.StatusOK {
		t.Fatalf("signed-in GET /players/harda = %d", page.Code)
	}
	// The design gives the panel no back-link: the pill row above it is how
	// you move between players, and the mark is how you leave. What matters
	// is that the page is not a dead end.
	body := page.Body.String()
	if !strings.Contains(body, `<nav class="pills"`) {
		t.Error("the player page has no roster to move with")
	}
	if !strings.Contains(body, `class="bar-home"`) {
		t.Error("the player page has no way back out")
	}
}

// A player with plenty of history who simply stopped is not short of games,
// and must not be told they are.
func TestLapsedPlayerIsNotCalledThin(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/players/lapsed").Body.String()
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
	t.Parallel()

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
		_, _, path, dashedPath, _ := buildMonthRanks(months, playerID, now, tr)

		// In the plot's units, the design's: places 3, 2 and 1 of three
		// fall at 128, 68 and 8 down, the months at 36, 174 and 312 across.
		wantPath := "M36.0 128.0 L174.0 68.0"
		wantDashed := "M174.0 68.0 L312.0 8.0"
		if path != wantPath {
			t.Errorf("path = %q, want %q", path, wantPath)
		}
		if dashedPath != wantDashed {
			t.Errorf("dashedPath = %q, want %q", dashedPath, wantDashed)
		}
	})

	t.Run("current month finished", func(t *testing.T) {
		now := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
		_, _, path, dashedPath, _ := buildMonthRanks(months, playerID, now, tr)

		wantPath := "M36.0 128.0 L174.0 68.0 L312.0 8.0"
		if path != wantPath {
			t.Errorf("path = %q, want %q", path, wantPath)
		}
		if dashedPath != "" {
			t.Errorf("dashedPath = %q, want none once the month has closed", dashedPath)
		}
	})
}

// withoutRoster cuts the roster's pill row out of a player page.
//
// Every player is listed in it with their own rank and latest score, so a figure
// found anywhere on the page is not necessarily a figure about the player the
// page is about — which is the only thing the tests below are asking.
func withoutRoster(page string) string {
	i := strings.Index(page, `<nav class="pills"`)
	if i < 0 {
		return page
	}
	j := strings.Index(page[i:], "</nav>")
	if j < 0 {
		return page
	}
	return page[:i] + page[i+j:]
}

// The roster lists everyone, so it is a second place the withheld figures
// could leak out of — and the one nobody would think to look at.
func TestTheRosterWithholdsFiguresBelowTheThreshold(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	page := fetch(t, srv, "/share/"+slug+"/players/harda").Body.String()
	i := strings.Index(page, `<nav class="pills"`)
	if i < 0 {
		t.Fatal("the player page has no roster")
	}
	menu := page[i : i+strings.Index(page[i:], "</nav>")]

	row := menu[strings.Index(menu, "/players/thin"):]
	row = row[:strings.Index(row, "</a>")]
	if !strings.Contains(row, `<span class="pill-rank num">—</span>`) {
		t.Errorf("the roster gives an unranked player a rank: %s", row)
	}
	// And a ranked one still has a rank, or the dash above means nothing.
	ranked := menu[strings.Index(menu, "/players/harda"):]
	ranked = ranked[:strings.Index(ranked, "</a>")]
	if strings.Contains(ranked, "—") {
		t.Errorf("a ranked player's figures are withheld too: %s", ranked)
	}
}

// With nobody on the board, the players view says so where it stands. It used
// to redirect to its own address, and the browser gave up after twenty.
func TestThePlayersViewWithNobodyIsAPlaceholder(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	admin, err := store.CreateUser(context.Background(), srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	for _, tt := range []struct {
		path   string
		cookie *http.Cookie
	}{
		{"/players", signIn(t, srv, admin.ID)},
		{"/share/" + slug + "/players", nil},
	} {
		rec := fetchAs(t, srv, tt.path, tt.cookie)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want the placeholder at 200 (Location %q)", tt.path, rec.Code, rec.Header().Get("Location"))
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<h1 class="page-title">Players</h1>`) || !strings.Contains(body, "No players yet.") {
			t.Errorf("%s does not say there is nobody to show", tt.path)
		}
		if strings.Contains(body, `<nav class="pills"`) {
			t.Errorf("%s draws an empty row of pills", tt.path)
		}
	}
}

// The eyebrow says when a player last played the way the design does, so it
// stays one line on a phone: today, yesterday, or the day and month — with
// the year only when it is not this one.
func TestLastPlayedIsShortAndRelative(t *testing.T) {
	t.Parallel()

	tr := translator{strings: catalogue{
		"player.lastPlayedToday":     "last played today",
		"player.lastPlayedYesterday": "last played yesterday",
		"player.lastPlayedOn":        "last played %s",
		"month.9":                    "September",
		"month.12":                   "December",
	}}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.Local)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	for _, tt := range []struct {
		date time.Time
		want string
	}{
		{day(2026, 9, 28), "last played today"},
		{day(2026, 9, 27), "last played yesterday"},
		{day(2026, 9, 20), "last played 20 Sep"},
		{day(2025, 12, 31), "last played 31 Dec 2025"},
	} {
		if got := lastPlayed(tr, tt.date, now); got != tt.want {
			t.Errorf("lastPlayed(%s) = %q, want %q", tt.date.Format(time.DateOnly), got, tt.want)
		}
	}
}
