package web

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
	"time"
)

// The nav offers only views that exist. A tab leading to a 404 is worse
// than an absent one.
func TestNavLinksAllResolve(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	for _, board := range []struct {
		name   string
		path   string
		cookie *http.Cookie
	}{
		{"authenticated", "/leaderboard", session},
		{"shared", "/share/" + slug + "/board", nil},
	} {
		t.Run(board.name, func(t *testing.T) {
			body := fetchAs(t, srv, board.path, board.cookie).Body.String()

			// Today is not a tab: the mark is the way home, and home is the
			// front page. It still has to resolve.
			home := hrefOfClass(t, body, "brand")
			if rec := fetchAs(t, srv, home, board.cookie); rec.Code != http.StatusOK {
				t.Errorf("the mark links to %s = %d, want 200", home, rec.Code)
			}
			// Where the front page lives differs by surface — /today when
			// signed in, the bare prefix on a share link — so this checks
			// what it renders rather than what it is called.
			if rec := fetchAs(t, srv, home, board.cookie); !strings.Contains(rec.Body.String(), "Still out") &&
				!strings.Contains(rec.Body.String(), "results in") {
				t.Errorf("the mark links to %q, which is not the front page", home)
			}

			for _, label := range []string{"Leaderboard", "Months", "Grid"} {
				href := hrefFor(t, body, label)
				rec := fetchAs(t, srv, href, board.cookie)
				if rec.Code != http.StatusOK {
					t.Errorf("%s → %s = %d, want 200", label, href, rec.Code)
				}
				// A shared nav must never point into authenticated routing.
				if board.cookie == nil && !strings.HasPrefix(href, "/share/") {
					t.Errorf("the shared nav links to %q", href)
				}
			}
		})
	}
}

func TestTodayShowsTheCurrentPuzzleAndWhoIsOut(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/today", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("today = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Still out") {
		t.Error("the today band does not say who has not filed")
	}
	// seedBoard gives lapsed no recent games, so they are still out today.
	if !strings.Contains(body, "Lapsed") {
		t.Error("a player with no result today is not listed as out")
	}
	if !strings.Contains(body, "results in") {
		t.Error("the day's filed count is missing")
	}
}

// Go's time.Format renders a weekday and a month in English regardless of
// locale — "Monday", "January" — so the headline date builds its own from
// the catalogue instead, the same way the grid's date column does.
func TestTodayHeadlineDateIsFullyLocalised(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	current := currentPuzzle()
	date, err := wordle.DateForPuzzle(current)
	if err != nil {
		t.Fatalf("DateForPuzzle(%d): %v", current, err)
	}

	englishWeekdays := map[time.Weekday]string{
		time.Sunday: "Sunday", time.Monday: "Monday", time.Tuesday: "Tuesday",
		time.Wednesday: "Wednesday", time.Thursday: "Thursday",
		time.Friday: "Friday", time.Saturday: "Saturday",
	}
	swedishWeekdays := map[time.Weekday]string{
		time.Sunday: "söndag", time.Monday: "måndag", time.Tuesday: "tisdag",
		time.Wednesday: "onsdag", time.Thursday: "torsdag",
		time.Friday: "fredag", time.Saturday: "lördag",
	}
	swedishMonths := map[time.Month]string{
		time.January: "januari", time.February: "februari", time.March: "mars",
		time.April: "april", time.May: "maj", time.June: "juni", time.July: "juli",
		time.August: "augusti", time.September: "september", time.October: "oktober",
		time.November: "november", time.December: "december",
	}

	en := fetch(t, srv, "/share/"+slug+"/today").Body.String()
	wantEn := fmt.Sprintf("%s %d %s %d", englishWeekdays[date.Weekday()], date.Day(), date.Month().String(), date.Year())
	if !strings.Contains(en, wantEn) {
		t.Errorf("the English headline date does not show %q", wantEn)
	}

	sv := fetchAs(t, srv, "/share/"+slug+"/today?lang=sv", nil).Body.String()
	wantSv := fmt.Sprintf("%s %d %s %d", swedishWeekdays[date.Weekday()], date.Day(), swedishMonths[date.Month()], date.Year())
	if !strings.Contains(sv, wantSv) {
		t.Errorf("the Swedish headline date does not show %q", wantSv)
	}
	if strings.Contains(sv, englishWeekdays[date.Weekday()]) {
		t.Errorf("the Swedish page still shows the English weekday %q", englishWeekdays[date.Weekday()])
	}
}

// When nothing clears its threshold the card is omitted, not padded.
func TestCalloutsAreOmittedWhenNothingIsRemarkable(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()

	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// One player, flat scores, nothing to remark on.
	p, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Flat", "flat")
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	current := currentPuzzle()
	for puzzle := current - 20; puzzle <= current; puzzle++ {
		seedResult(t, srv, p.ID, puzzle, 4, false)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/today", nil).Body.String()
	for _, kicker := range []string{"On form", "Off form", "One and done"} {
		if strings.Contains(body, kicker) {
			t.Errorf("callout %q fired on a flat history", kicker)
		}
	}
}

func TestMonthsRanksAndSeparatesThinPlayers(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/months", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("months = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Month by month") {
		t.Error("the month view did not render")
	}
	for _, col := range []string{"3 or better", "Fails", "Longest streak"} {
		if !strings.Contains(body, col) {
			t.Errorf("column %q is missing", col)
		}
	}
	// normalb scores 2s throughout and must win the current month.
	if !strings.Contains(body, "Normalb") {
		t.Error("the month winner is missing")
	}
}

// Selecting a month is a link, and it changes what is shown.
func TestMonthSelectionChangesTheTable(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	if !strings.Contains(body, "month=") {
		t.Fatal("the month chips are not links")
	}

	// Follow the last chip, which is the oldest month in the fixture.
	idx := strings.LastIndex(body, `href="/share/`+slug+`/months?`)
	rest := body[idx+len(`href="`):]
	href := rest[:strings.Index(rest, `"`)]
	href = strings.ReplaceAll(href, "&amp;", "&")

	other := fetchAs(t, srv, href, nil)
	if other.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", href, other.Code)
	}
	if other.Body.String() == body {
		t.Error("selecting a different month rendered an identical page")
	}
}

// The filters carry across the views, so a reader is not comparing numbers
// computed on different terms as they move between them.
func TestViewsHonourTheHardModeFilter(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	months := fetchAs(t, srv, "/share/"+slug+"/months?mode=hard", nil).Body.String()
	if strings.Contains(months, ">Normala<") {
		t.Error("a player with no hard-mode games appears in the filtered month view")
	}
	if !strings.Contains(months, "Harda") {
		t.Error("a hard-mode player is missing from the filtered month view")
	}
}

func TestSharedViewsExposeNoAuthenticatedSurface(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{"/share/" + slug + "/today", "/share/" + slug + "/months"} {
		body := fetchAs(t, srv, path, nil).Body.String()
		if strings.Contains(body, "/logout") {
			t.Errorf("%s offers sign-out", path)
		}
		if strings.Contains(body, `href="/p/`) || strings.Contains(body, `href="/leaderboard`) {
			t.Errorf("%s links into authenticated routing", path)
		}
	}
}

func TestAuthenticatedViewsRequireASession(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	for _, path := range []string{"/today", "/months"} {
		if got := fetchAs(t, srv, path, nil).Code; got != http.StatusSeeOther {
			t.Errorf("anonymous GET %s = %d, want a redirect", path, got)
		}
	}
}

// time.Month.String() is always English, so month names come from the
// catalogue like everything else.
func TestMonthNamesAreLocalised(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	en := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	sv := fetchAs(t, srv, "/share/"+slug+"/months?lang=sv", nil).Body.String()

	months := map[string]string{
		"January": "januari", "February": "februari", "March": "mars",
		"April": "april", "May": "maj", "June": "juni", "July": "juli",
		"August": "augusti", "September": "september", "October": "oktober",
		"November": "november", "December": "december",
	}
	var checked int
	for english, swedish := range months {
		if !strings.Contains(en, english) {
			continue
		}
		checked++
		if !strings.Contains(sv, swedish) {
			t.Errorf("%s is not rendered as %q under ?lang=sv", english, swedish)
		}
		if strings.Contains(sv, english) && english != swedish {
			t.Errorf("the English month name %q survived the switch", english)
		}
	}
	if checked == 0 {
		t.Fatal("no month names on the page to check")
	}
}

func TestGridRendersDaysByPlayers(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/grid", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grid = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Every day, everybody") {
		t.Error("the grid did not render")
	}
	// A player with games is a column; the never-played one is not.
	if !strings.Contains(body, "/p/harda") {
		t.Error("a player with games is missing from the grid")
	}
	// The rail carries an average, labelled with the window it covers
	// rather than a fixed one the grid may not be showing.
	if !strings.Contains(body, "Average · all") {
		t.Error("the form rail is missing, or does not name its window")
	}
}

// A grid cell carries the same two signals the player page's recent strip
// does: hard mode as a trailing * on the label, and the detail — here, which
// player and which day, since both the column heading and the row's own
// date can scroll out of view — behind a tap rather than a hover, which
// never worked on a phone. The note explains the asterisk in the same words
// the strip's own legend does.
func TestGridCellsCarryTheAsteriskAndOpenAPopup(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetch(t, srv, "/share/"+slug+"/grid").Body.String()

	if strings.Contains(body, " hard tiny") {
		t.Error("a grid cell still carries the border class the asterisk replaced")
	}
	if strings.Contains(body, `tiny" title="`) {
		t.Error("a grid cell still carries the hover the popup replaced")
	}
	if !strings.Contains(body, ">3*<") {
		t.Error("a hard-mode result does not carry the asterisk in its box")
	}
	// The same words the recent strip's own legend uses, not a second
	// phrasing for the same fact.
	if !strings.Contains(body, "An asterisk marks hard mode") {
		t.Error("the grid note does not explain the asterisk")
	}
	if got := strings.Count(body, `<details class="cell-pop" name="popup">`); got == 0 {
		t.Error("no grid cell opens a popup")
	}

	current := currentPuzzle()
	date, err := wordle.DateForPuzzle(current)
	if err != nil {
		t.Fatalf("DateForPuzzle(%d): %v", current, err)
	}
	want := fmt.Sprintf(">Harda · #%d (%s)<", current, date.Format("2006-01-02"))
	if !strings.Contains(body, want) {
		t.Errorf("the popup does not show %q", want)
	}
}

// Zero-game players are hidden by default and shown on request.
func TestGridInactiveToggle(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	if _, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Ghost", "ghost"); err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	hidden := fetchAs(t, srv, "/share/"+slug+"/grid", nil).Body.String()
	if strings.Contains(hidden, "/p/ghost") {
		t.Error("a player with no games is a column by default")
	}

	// The label carries a count, so the control is found by its class.
	href := strings.ReplaceAll(hrefOfClass(t, hidden, "toggle"), "&amp;", "&")
	shown := fetchAs(t, srv, href, nil).Body.String()
	if !strings.Contains(shown, "/p/ghost") {
		t.Error("following the toggle did not show them")
	}
}

// The Players view is the detail panel with a picker, so the nav has
// somewhere to point and the panel can be swapped without going back.
func TestPlayersViewShowsThePickerAndTheLeader(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/players", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("players = %d", rec.Code)
	}
	body := rec.Body.String()

	// normalb tops the board, so they are who the view opens on.
	if !strings.Contains(body, "<h1>Normalb") {
		t.Error("the players view does not open on the top-ranked player")
	}
	// Every player is in the picker, ranked and not.
	for _, want := range []string{"/p/harda", "/p/normala", "/p/thin", "/p/lapsed"} {
		if !strings.Contains(body, want) {
			t.Errorf("the picker is missing %s", want)
		}
	}
	// And the picker marks who is showing.
	if !strings.Contains(body, `class="pick on"`) {
		t.Error("the picker does not mark the current player")
	}
}

// Reaching a player directly is still the same view, so the nav keeps its
// place rather than losing the highlight.
func TestPlayerDetailHighlightsThePlayersTab(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/p/harda", nil).Body.String()
	i := strings.Index(body, `class="view on"`)
	if i < 0 {
		t.Fatal("no active nav tab on the player page")
	}
	if !strings.Contains(body[i:i+120], "Players") {
		t.Errorf("the active tab is not Players: %q", body[i:i+120])
	}
}

// The narrow layout moves the views out of the top bar into their own
// scrolling row, with Today restored: on a phone the mark is a header
// rather than something to navigate by.
func TestMobileNavCoversEveryView(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	at := strings.Index(body, `class="views-mobile"`)
	if at < 0 {
		t.Fatal("the mobile view row is missing")
	}
	bar := body[at:]
	bar = bar[:strings.Index(bar, "</nav>")]

	for _, label := range []string{"Today", "Leaderboard", "Months", "Grid", "Players"} {
		if !strings.Contains(bar, ">"+label+"<") {
			t.Errorf("the mobile view row is missing %q", label)
		}
	}
	// The same list at both widths, Today included: the wordmark is not a
	// link, so a missing Today pill would leave the front page unreachable.
	top := body[:at]
	if !strings.Contains(top, ">Today<") {
		t.Error("Today is missing from the top bar")
	}
	// It is the same pill markup as the top bar, not a second kind of
	// navigation with its own classes to keep in step.
	if !strings.Contains(bar, `class="view`) {
		t.Error("the mobile row does not reuse the top bar's pills")
	}
}

// A trait is earned from the figures and explained on hover, so a player
// can find out why rather than guess.
func TestTraitsAppearAndExplainThemselves(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	// seedBoard's hard-mode regulars play it near-exclusively.
	page := fetchAs(t, srv, "/share/"+slug+"/p/harda", nil).Body.String()
	if !strings.Contains(page, "Purist") {
		t.Error("a hard-mode regular has not earned their trait")
	}
	if !strings.Contains(page, "Plays hard mode almost every day") {
		t.Error("the trait does not explain itself")
	}

	// And one who has played three games is a newcomer, not a purist.
	thin := fetchAs(t, srv, "/share/"+slug+"/p/thin", nil).Body.String()
	if !strings.Contains(thin, "New here") {
		t.Error("a player with few games has the wrong trait")
	}
}

// Nothing earned means nothing shown: padding everyone out would make the
// ones that mean something worthless.
func TestNoTraitWhenNothingIsEarned(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()

	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Ordinary", "ordinary")
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}

	// Ordinary spread, flat form, gaps so there is no streak to name.
	current := currentPuzzle()
	pattern := []int{3, 5, 4, 3, 6, 4, 2, 4, 5, 3}
	for i, puzzle := 0, current-40; puzzle <= current; i, puzzle = i+1, puzzle+1 {
		if puzzle%9 == 0 {
			continue
		}
		seedResult(t, srv, p.ID, puzzle, pattern[i%len(pattern)], false)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/p/ordinary", nil).Body.String()
	if strings.Contains(body, `class="trait"`) {
		t.Error("a player who has earned nothing was given a trait anyway")
	}
}

// The labels are localised like everything else.
func TestTraitsAreLocalised(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	sv := fetchAs(t, srv, "/share/"+slug+"/p/harda?lang=sv", nil).Body.String()
	if !strings.Contains(sv, "Purist") {
		t.Error("the Swedish page has no trait")
	}
	if !strings.Contains(sv, "hard mode så gott som varje dag") {
		t.Error("the explanation is still English under ?lang=sv")
	}
}

// The front page is what a signed-in reader lands on, and what the bare
// share URL shows. The leaderboard is a click away, not the doorstep.
func TestTodayIsTheFrontPage(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	root := fetchAs(t, srv, "/share/"+slug+"/", nil)
	if root.Code != http.StatusOK {
		t.Fatalf("the bare share URL = %d", root.Code)
	}
	if !strings.Contains(root.Body.String(), "Still out") {
		t.Error("the bare share URL does not show the front page")
	}

	// And the leaderboard has its own address under the prefix.
	board := fetchAs(t, srv, "/share/"+slug+"/board", nil)
	if board.Code != http.StatusOK {
		t.Fatalf("the shared leaderboard = %d", board.Code)
	}
	if !strings.Contains(board.Body.String(), "Not ranked") {
		t.Error("/board does not show the leaderboard")
	}
}

// Each view's controls link back to that view. One shared path would send
// every filter and sort to whichever view held the bare prefix.
func TestEachViewsControlsPointAtItself(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	board := fetchAs(t, srv, "/share/"+slug+"/board", nil).Body.String()
	href := hrefFor(t, board, "Hard mode")
	if !strings.HasPrefix(href, "/share/"+slug+"/board") {
		t.Errorf("the leaderboard's filter links to %q, not back to itself", href)
	}
	if !strings.Contains(fetchAs(t, srv, href, nil).Body.String(), "players hidden") {
		t.Error("following the leaderboard's own filter did not filter it")
	}
}

// A player who has left the group is behind the toggle even though they
// were playing right up to the day they left.
func TestGridHidesRetiredPlayersUntilToggled(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()

	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	player, err := store.PlayerBySlug(ctx, srv.db, "harda")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}
	inactive := false
	if _, err := store.UpdatePlayer(ctx, srv.db, store.AdminActor(admin.ID), player.ID,
		store.PlayerUpdate{Active: &inactive}); err != nil {
		t.Fatalf("UpdatePlayer: %v", err)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	hidden := fetchAs(t, srv, "/share/"+slug+"/grid", nil).Body.String()
	if strings.Contains(hidden, "/p/harda") {
		t.Error("a player who has left the group is still a column")
	}
	if !strings.Contains(hidden, "Show inactive") {
		t.Error("the toggle does not offer them")
	}

	shown := fetchAs(t, srv, "/share/"+slug+"/grid?inactive=1", nil).Body.String()
	if !strings.Contains(shown, "/p/harda") {
		t.Error("the toggle did not bring them back")
	}
}

// The winner pane carries the four figures the design puts beside the name.
func TestMonthWinnerPaneShowsTheStats(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	stats := body[strings.Index(body, "month-stats"):]
	stats = stats[:strings.Index(stats, "</dl>")]

	for _, want := range []string{"Average", "Puzzles", "3 or better", "Longest streak"} {
		if !strings.Contains(stats, want) {
			t.Errorf("the winner pane is missing %q", want)
		}
	}
	// Games reads as played-over-possible, not a bare count.
	if !strings.Contains(stats, "/") {
		t.Error("the games figure does not say how many days were possible")
	}
}

// A chip is two lines and shows whole words. Cutting a name with an
// ellipsis was the wrong fix for a crowded row; the row scrolls instead.
func TestMonthChipsShowWholeWords(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	chips := body[strings.Index(body, "month-chips"):]
	chips = chips[:strings.Index(chips, "</ol>")]

	// The full month name, not three letters of it.
	if !strings.Contains(chips, "August 2026") {
		t.Error("a chip does not carry the full month name")
	}
	if !strings.Contains(body, "month-chip-head") || !strings.Contains(body, "month-winner") {
		t.Error("the chip is missing its two lines")
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	strip := css[strings.Index(css, ".month-chips {"):]
	strip = strip[:strings.Index(strip, ".month-chip {")]
	if !strings.Contains(strip, "overflow-x: auto") {
		t.Error("the row of chips does not scroll")
	}
	if strings.Contains(css, ".month-chip-head") && strings.Contains(
		css[strings.Index(css, ".month-chip-head"):strings.Index(css, ".month-chip-win")], "ellipsis") {
		t.Error("the chip head still clips its text")
	}
}

// The front page's form table is headed "form, last 30 days" and prints the
// form figure, so it has to be ordered by form. It was ordered by the
// board's all-time ranking, which put a 3.90 above a 3.59.
func TestFrontPageFormTableIsOrderedByForm(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	table := body[strings.Index(body, "today-form"):]

	// Read the form figures in the order they are printed.
	var figures []float64
	rest := table
	for {
		i := strings.Index(rest, `class="podium-figure">`)
		j := strings.Index(rest, `<span class="num">`)
		if i < 0 && j < 0 {
			break
		}
		var at, skip int
		switch {
		case i >= 0 && (j < 0 || i < j):
			at, skip = i, len(`class="podium-figure">`)
		default:
			at, skip = j, len(`<span class="num">`)
		}
		rest = rest[at+skip:]
		field := strings.TrimSpace(rest[:min(24, len(rest))])
		var v float64
		if _, err := fmt.Sscanf(field, "%f", &v); err == nil {
			figures = append(figures, v)
		}
	}

	if len(figures) < 3 {
		t.Fatalf("only read %d form figures from the table", len(figures))
	}
	for i := 1; i < len(figures); i++ {
		if figures[i] < figures[i-1] {
			t.Errorf("form figures are out of order at %d: %v", i, figures)
			break
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// A month with days left in it has a leader, not a winner, so the line
// describing it belongs in the present tense.
func TestRunningMonthReadsAsUnfinished(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	// The newest month is the one being played, and it is what the view
	// opens on.
	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	head := body[strings.Index(body, "month-head"):]
	head = head[:strings.Index(head, "month-stats")]

	// "leading" rather than "winner" is how the head marks an unfinished
	// month; the chips carry "still running".
	if !strings.Contains(head, "leading") {
		t.Fatalf("the current month is not marked as running:\n%s", head)
	}
	if strings.Contains(head, "took") {
		t.Errorf("a running month is described as finished:\n%s", head)
	}
	if !strings.Contains(head, "is ahead") && !strings.Contains(head, "Level at") {
		t.Errorf("a running month is not described in the present tense:\n%s", head)
	}
}

// The month view against the design: the header names the puzzle range, the
// winner pane says whether the month is complete, rows carry a medal and a
// trait, and the season table has wins, top-three, best month and a mark
// per month.
func TestMonthViewMatchesTheDesign(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()

	if !regexp.MustCompile(`Wordle \d+–\d+`).MatchString(body) {
		t.Error("the header does not name the puzzle range")
	}
	if !strings.Contains(body, "month · ") {
		t.Error("the winner pane does not say whether the month is complete")
	}
	for _, col := range []string{"Top three", "Best month", "Season so far", "monthly wins across"} {
		if !strings.Contains(body, col) {
			t.Errorf("the season table is missing %q", col)
		}
	}

	// A month still being played has a leader, not a winner or a runner-up.
	table := body[strings.Index(body, "months-table"):strings.Index(body, "season-head")]
	if strings.Contains(table, ">Winner<") || strings.Contains(table, ">Runner-up<") {
		t.Error("a running month names a winner")
	}
	if !strings.Contains(table, ">Leading<") {
		t.Error("a running month does not name its leader")
	}

	// A finished one does. The chip for it is read off the page rather than
	// guessed at, since which months exist depends on the fixture.
	var finished string
	for _, m := range regexp.MustCompile(`href="([^"]*month=[^"]*)"`).FindAllStringSubmatch(body, -1) {
		href := strings.ReplaceAll(m[1], "&amp;", "&")
		page := fetchAs(t, srv, href, nil).Body.String()
		if !strings.Contains(page, ">Leading<") {
			finished = page
			break
		}
	}
	if finished == "" {
		t.Skip("the fixture has no completed month")
	}
	if !strings.Contains(finished, ">Winner<") {
		t.Error("a finished month does not name its winner")
	}
}

// A star marks a title; a placing is a number; not ranked is a dot.
func TestSeasonMarksReadAtAGlance(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	season := body[strings.Index(body, "season-table"):]

	if !strings.Contains(season, "★") {
		t.Error("no star marks a monthly win")
	}
	if !strings.Contains(season, "·") {
		t.Error("nothing marks a month somebody was not ranked in")
	}
	// The note explains all three, so the marks are not a puzzle.
	if !strings.Contains(body, "★ marks a win") {
		t.Error("the season note does not explain the marks")
	}
}

// The form pane: a header so the columns are readable, a chart in both the
// cards and the rows, and games reported only where it is labelled.
func TestFormPaneIsConsistent(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	pane := body[strings.Index(body, "today-form"):strings.Index(body, "bench-toggle")]

	if !strings.Contains(pane, "form-head") {
		t.Error("the form table has no header")
	}
	for _, col := range []string{"30d form", "Streak", "Last 30 days"} {
		if !strings.Contains(pane, col) {
			t.Errorf("the header is missing %q", col)
		}
	}

	// The cards carry a chart, as the rows do.
	if !strings.Contains(pane, "podium-spark") {
		t.Error("the podium cards have no chart")
	}

	// Every figure in a row sits under a heading that names it. The rows
	// once printed an unlabelled number that read as a game count in the
	// cards and a streak in the table, which is what the header fixes.
	rows := pane[strings.Index(pane, "form-rows"):]
	if strings.Count(rows, "podium") > 0 {
		t.Fatal("the row slice overlaps the cards")
	}
	head := rows[:strings.Index(rows, "</li>")]
	body_ := rows[strings.Index(rows, "</li>"):]
	if got, want := strings.Count(head, "<span"), 5; got != want {
		t.Errorf("the header has %d cells, want %d", got, want)
	}
	firstRow := body_[strings.Index(body_, "<li>"):]
	firstRow = firstRow[:strings.Index(firstRow, "</li>")]
	// The delta and the chart nest inside cells, so count only the cells
	// the grid lays out: the direct children of the row.
	if got := strings.Count(firstRow, `<span class="num`) + strings.Count(firstRow, `<span class="row-name"`) +
		strings.Count(firstRow, `<span class="spark-col"`); got != 5 {
		t.Errorf("a row lays out %d cells against a 5-column header", got)
	}
}

// A tooltip is a desktop-only affordance. The explanation has to be
// reachable by tapping, which means an element that opens without script.
func TestTraitExplanationIsReachableWithoutHover(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{
		"/share/" + slug + "/",
		"/share/" + slug + "/months",
		"/share/" + slug + "/p/harda",
	} {
		body := fetchAs(t, srv, path, nil).Body.String()
		if !strings.Contains(body, "trait-pop") {
			continue // no trait earned on this page
		}
		if !strings.Contains(body, `<details class="trait-pop" name="popup"><summary`) {
			t.Errorf("%s: the trait does not open on tap", path)
		}
		if !strings.Contains(body, `class="trait-why popup-panel"`) {
			t.Errorf("%s: the trait carries no readable explanation", path)
		}
		if strings.Contains(body, "trait-pop") && !strings.Contains(body, `title="`) {
			t.Errorf("%s: the trait lost its hover text", path)
		}
	}
}

// The board has more columns than a phone is wide. It scrolls rather than
// clipping, the same as the month tables — and a column hidden at that
// width has to be hidden in the header too, or the two rows misalign.
func TestBoardScrollsSidewaysOnNarrowScreens(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board", nil).Body.String()
	table := strings.Index(body, `<table class="board">`)
	if table < 0 {
		t.Fatal("no board table")
	}
	before := body[:table]
	if !strings.Contains(before[strings.LastIndex(before, "<div"):], "table-scroll") {
		t.Error("the board table is not inside a horizontal scroller")
	}

	head := body[strings.Index(body, "<thead>")+len("<thead>") : strings.Index(body, "</thead>")]
	firstRow := body[strings.Index(body, "<tbody>"):]
	firstRow = firstRow[strings.Index(firstRow, "<tr"):strings.Index(firstRow, "</tr>")]

	if got, want := strings.Count(head, "spark-col"), 1; got != want {
		t.Errorf("the header marks %d sparkline cells, want %d", got, want)
	}
	if got, want := strings.Count(firstRow, "spark-col"), 1; got != want {
		t.Errorf("a row marks %d sparkline cells, want %d", got, want)
	}
	if got, want := strings.Count(head, "<th"), strings.Count(firstRow, "<td"); got != want {
		t.Errorf("the header has %d cells and a row %d", got, want)
	}
}

// The grid's rank figures follow the window it is showing. A player who was
// poor all year and excellent lately leads the recent view and trails the
// whole-history one.
//
// The rank is a figure on the column, not the column's position: the
// columns stay in name order so a reader finds the same person in the same
// place whatever the range.
func TestGridRanksOverTheSelectedWindow(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	actor := store.AdminActor(admin.ID)
	current := wordle.PuzzleForDate(time.Now())

	add := func(slug string, early, late int) {
		p, err := store.CreatePlayer(ctx, srv.db, actor, strings.ToUpper(slug[:1])+slug[1:], slug)
		if err != nil {
			t.Fatalf("CreatePlayer: %v", err)
		}
		for n := current - 149; n <= current; n++ {
			g := early
			if n > current-90 {
				g = late
			}
			date, _ := wordle.DateForPuzzle(n)
			if _, _, err := store.UpsertResult(ctx, srv.db, store.Result{
				PuzzleNo: n, Date: date, PlayerID: p.ID, Guesses: &g, Solved: true,
			}, nil); err != nil {
				t.Fatalf("UpsertResult: %v", err)
			}
		}
	}
	add("steady", 3, 3)   // always 3
	add("improver", 6, 2) // dreadful, then the best lately

	session := signIn(t, srv, admin.ID)

	// The rank shown against one player's column, for a given range.
	rank := regexp.MustCompile(`class="grid-rank[^"]*"[^>]*>(\d*)<`)
	rankOf := func(path, slug string) string {
		body := fetchAs(t, srv, path, session).Body.String()
		// One rail entry per player, so find that entry rather than a
		// window around the link: a fixed slice reaches into its neighbour
		// and reports somebody else's rank.
		for _, entry := range strings.Split(body, "<li>") {
			if !strings.Contains(entry, "/p/"+slug+`"`) {
				continue
			}
			if m := rank.FindStringSubmatch(entry); m != nil {
				return m[1]
			}
			return ""
		}
		t.Fatalf("%s has no rail entry for %s", path, slug)
		return ""
	}

	if got := rankOf("/grid?span=90", "improver"); got != "1" {
		t.Errorf("over the last 90 days improver ranks %q, want 1", got)
	}
	if got := rankOf("/grid?span=all", "steady"); got != "1" {
		t.Errorf("over the whole history steady ranks %q, want 1", got)
	}
}

// Columns keep their places whatever the range, so the table can be read.
// The rail beside them is a standings table and follows the window instead.
func TestGridColumnsStayInNameOrder(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	admin, _ := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	actor := store.AdminActor(admin.ID)
	current := wordle.PuzzleForDate(time.Now())

	for _, p := range []struct {
		slug        string
		early, late int
	}{{"zoe", 3, 3}, {"adam", 6, 2}} {
		pl, _ := store.CreatePlayer(ctx, srv.db, actor, strings.ToUpper(p.slug[:1])+p.slug[1:], p.slug)
		for n := current - 149; n <= current; n++ {
			g := p.early
			if n > current-90 {
				g = p.late
			}
			d, _ := wordle.DateForPuzzle(n)
			store.UpsertResult(ctx, srv.db, store.Result{
				PuzzleNo: n, Date: d, PlayerID: pl.ID, Guesses: &g, Solved: true}, nil)
		}
	}

	session := signIn(t, srv, admin.ID)
	// Scoped to the table head: the rail carries the same two links in a
	// different order, so a search over the whole page proves nothing.
	head := func(body string) string {
		start := strings.Index(body, "<thead>")
		end := strings.Index(body, "</thead>")
		if start < 0 || end < start {
			t.Fatal("the grid rendered without a table head")
		}
		return body[start:end]
	}
	rail := func(body string) string {
		start := strings.Index(body, `<aside class="grid-rail">`)
		end := strings.Index(body, "</aside>")
		if start < 0 || end < start {
			t.Fatal("the grid rendered without a rail")
		}
		return body[start:end]
	}

	// Adam is worse than Zoe over the whole history and better over the last
	// ninety days, so the rail turns over between the two ranges and the
	// columns do not.
	for _, span := range []string{"90", "all"} {
		body := fetchAs(t, srv, "/grid?span="+span, session).Body.String()
		if strings.Index(head(body), "/p/adam") > strings.Index(head(body), "/p/zoe") {
			t.Errorf("span=%s orders the columns by rank rather than by name", span)
		}
	}

	recent := rail(fetchAs(t, srv, "/grid?span=90", session).Body.String())
	if strings.Index(recent, "/p/adam") > strings.Index(recent, "/p/zoe") {
		t.Error("over the last 90 days the rail puts Adam below Zoe, though he averages better")
	}
	all := rail(fetchAs(t, srv, "/grid?span=all", session).Body.String())
	if strings.Index(all, "/p/zoe") > strings.Index(all, "/p/adam") {
		t.Error("over the whole history the rail puts Zoe below Adam, though she averages better")
	}
}

// A range control that cannot change anything is not offered.
func TestGridHidesTheSpanToggleWhenThereIsNothingToChoose(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")

	body := fetchAs(t, srv, "/grid", signIn(t, srv, admin.ID)).Body.String()
	if strings.Contains(body, `span=all`) {
		t.Error("the range toggle is offered on a history shorter than the window")
	}
}

// The month kicker states the rule the code applies, including the missed
// day, and drops the clause when the toggle it depends on is off.
func TestMonthsKickerStatesTheScoringRule(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	const clause = "a day not played counts as 7"

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	if !strings.Contains(body, clause) {
		t.Errorf("the kicker does not say %q, though the average counts them", clause)
	}
	if want := "10 puzzles minimum"; !strings.Contains(body, want) {
		t.Errorf("the kicker does not say %q", want)
	}

	plain := fetchAs(t, srv, "/share/"+slug+"/months?failed=0", nil).Body.String()
	if strings.Contains(plain, clause) {
		t.Errorf("the kicker still says %q with X not counted as 7", clause)
	}
}
