package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// The nav offers only views that exist. A tab leading to a 404 is worse
// than an absent one.
func TestNavLinksAllResolve(t *testing.T) {
	t.Parallel()

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

			// The brand and Today pill both lead to the front page.
			home := hrefOfClass(t, body, "bar-home")
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
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/today", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("today = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "5 of 6 in") {
		t.Error("the day does not say how many have filed")
	}
	// seedBoard gives lapsed no recent games, so they are still out today.
	// The names are behind a disclosure, but they are in the markup either
	// way — that is the point of a disclosure rather than a round trip.
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
	t.Parallel()

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
	t.Parallel()

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

func TestMonthsRanksPlayers(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/months", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("months = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, `<h1 class="page-title">Months</h1>`) {
		t.Error("the month view did not render")
	}
	for _, col := range []string{"Behind", "Played", "Fails"} {
		if !strings.Contains(body, col) {
			t.Errorf("column %q is missing", col)
		}
	}
	// normalb scores 2s throughout and must win the current month.
	if !strings.Contains(body, "Normalb") {
		t.Error("the month winner is missing")
	}
	table, ok := sectionOf(body, `<ol class="month-rows">`, "</ol>")
	if !ok {
		t.Fatal("the ranked month table is missing")
	}
	// Thin has fewer than ten games. That still excludes them on the main
	// board, but the monthly view ranks every scorable appearance.
	if !strings.Contains(table, "/players/thin") {
		t.Error("a player below ten games is missing from the monthly ranking")
	}
}

// Selecting a month is a link, and it changes what is shown.
func TestMonthSelectionChangesTheTable(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	if !strings.Contains(body, "month=") {
		t.Fatal("the month chips are not links")
	}

	// Follow the last pill, which is the oldest month in the fixture.
	pills, _ := sectionOf(body, `<nav class="pills"`, "</nav>")
	idx := strings.LastIndex(pills, `href="/share/`+slug+`/months?`)
	rest := pills[idx+len(`href="`):]
	href := rest[:strings.Index(rest, `"`)]
	href = strings.ReplaceAll(href, "&amp;", "&")

	other := fetchAs(t, srv, href, nil)
	if other.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", href, other.Code)
	}
	if other.Body.String() == body {
		t.Error("selecting a different month rendered an identical page")
	}
	// A finished month does not count down: no days left, no running mark.
	hero, _ := sectionOf(other.Body.String(), `<section class="card month-hero">`, "</section>")
	if strings.Contains(hero, " left") || strings.Contains(hero, "month-daybar") {
		t.Error("a completed month shows the current month's calendar progress")
	}
}

// The filters carry across the views, so a reader is not comparing numbers
// computed on different terms as they move between them.
func TestViewsHonourTheHardModeFilter(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{"/share/" + slug + "/today", "/share/" + slug + "/months"} {
		body := fetchAs(t, srv, path, nil).Body.String()
		if strings.Contains(body, "/logout") {
			t.Errorf("%s offers sign-out", path)
		}
		if strings.Contains(body, `href="/players/`) || strings.Contains(body, `href="/leaderboard`) {
			t.Errorf("%s links into authenticated routing", path)
		}
	}
}

func TestAuthenticatedViewsRequireASession(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

func TestSwedishWinnerListsUseOch(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	tr := translator{
		locale: "sv", strings: srv.catalogues["sv"], fallback: srv.catalogues["en"],
	}
	avg := 3.25
	players := []stats.MonthPlayer{
		{Player: store.Player{Name: "Alice"}, Average: &avg},
		{Player: store.Player{Name: "Bob"}, Average: &avg},
	}
	if got, want := joinNames(tr, players), "Alice och Bob"; got != want {
		t.Errorf("joinNames() = %q, want %q", got, want)
	}
}

func TestGridRendersDaysByPlayers(t *testing.T) {
	t.Parallel()

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
	if !strings.Contains(body, `title="Harda"`) {
		t.Error("a player with games is missing from the grid")
	}
	// Each column carries its average, and the eyebrow names the window
	// they cover rather than a fixed one the grid may not be showing.
	if !strings.Contains(body, `<span class="grid-avg num">`) {
		t.Error("the columns carry no average")
	}
	if !strings.Contains(body, `<p class="page-eyebrow">`) || !strings.Contains(body, " days</p>") {
		t.Error("the grid does not name the window it covers")
	}
}

// A grid cell carries the same two signals the player page's recent strip
// does: hard mode as a trailing * on the label, and the detail — here, which
// player and which day, since both the column heading and the row's own
// date can scroll out of view — behind a tap rather than a hover, which
// never worked on a phone. The note explains the asterisk in the same words
// the strip's own legend does.
func TestGridCellsCarryTheAsteriskAndOpenAPopup(t *testing.T) {
	t.Parallel()

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
	// The legend explains the asterisk with a tile carrying one.
	if legend, _ := sectionOf(body, `<ul class="grid-legend">`, "</ul>"); !strings.Contains(legend, ">4*<") || !strings.Contains(legend, "Hard mode") {
		t.Error("the grid legend does not explain the asterisk")
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

// Go's time.Format renders a month abbreviation in English regardless of
// locale — "Aug", never "aug" — so the grid's date column has to build its
// own from the catalogue instead, the way the months view already does.
func TestGridDateUsesTheLocalisedMonthAbbreviation(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	swedish := map[time.Month]string{
		time.January: "jan", time.February: "feb", time.March: "mar",
		time.April: "apr", time.May: "maj", time.June: "jun",
		time.July: "jul", time.August: "aug", time.September: "sep",
		time.October: "okt", time.November: "nov", time.December: "dec",
	}
	month := time.Now().Month()
	english := month.String()[:3]

	en := fetch(t, srv, "/share/"+slug+"/grid").Body.String()
	if !strings.Contains(en, " "+english) {
		t.Fatalf("fixture assumption changed: the grid does not show %q", english)
	}

	sv := fetchAs(t, srv, "/share/"+slug+"/grid?lang=sv", nil).Body.String()
	if !strings.Contains(sv, swedish[month]) {
		t.Errorf("the Swedish grid does not show the localised abbreviation %q", swedish[month])
	}
	if strings.Contains(sv, english) {
		t.Errorf("the Swedish grid still shows the English abbreviation %q", english)
	}
}

// Zero-game players are hidden by default and shown on request.
func TestGridInactiveToggle(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	if _, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Ghost", "ghost"); err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	hidden := fetchAs(t, srv, "/share/"+slug+"/grid", nil).Body.String()
	if strings.Contains(hidden, `title="Ghost"`) {
		t.Error("a player with no games is a column by default")
	}

	// The label carries a count, so the control is found by its class.
	href := strings.ReplaceAll(hrefOfClass(t, hidden, "head-toggle"), "&amp;", "&")
	shown := fetchAs(t, srv, href, nil).Body.String()
	if !strings.Contains(shown, `title="Ghost"`) {
		t.Error("following the toggle did not show them")
	}
}

// The players view opens on whoever leads the board.
//
// It used to open on an empty page asking which player to show. That question
// had one answer nearly every time and it cost a tap to give it; the roster is
// one press away in the bar either way, and the bar now names the player it is
// showing rather than looking like a control that has not been used yet.
func TestThePlayersViewOpensOnTheLeader(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	rec := fetchAs(t, srv, "/share/"+slug+"/players", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("players = %d, want a redirect", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/share/"+slug+"/players/normalb" {
		t.Errorf("players opens on %q, want the top of the board", got)
	}

	body := fetchAs(t, srv, "/share/"+slug+"/players/normalb", nil).Body.String()
	if !strings.Contains(body, `<h1 class="page-title">Normalb`) {
		t.Error("the leader's page is not what opened")
	}
	// Every player is still one press away, ranked and not, with the one
	// being shown marked.
	for _, want := range []string{"/players/harda", "/players/normala", "/players/thin", "/players/lapsed"} {
		if !strings.Contains(body, want) {
			t.Errorf("the roster is missing %s", want)
		}
	}
	if !strings.Contains(body, `<a class="pill on" href="/share/`+slug+`/players/normalb"`) {
		t.Error("the roster does not mark the player being shown")
	}
}

// Reaching a player directly is still the same view, so the nav keeps its
// place rather than losing the highlight.
func TestPlayerDetailHighlightsThePlayersTab(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/players/harda", nil).Body.String()
	pages, ok := sectionOf(body, `<nav class="bar-pages glass"`, "</nav>")
	if !ok {
		t.Fatal("the bar's pages are missing")
	}
	i := strings.Index(pages, `class="bar-page on"`)
	if i < 0 {
		t.Fatal("no page marked current on the player page")
	}
	if !strings.Contains(pages[i:], "<span>Players</span>") {
		t.Errorf("the page marked current is not Players: %q", pages[i:i+160])
	}
	// And the phone's capsule names it.
	if !strings.Contains(body, `<span class="bar-capsule-label">Players</span>`) {
		t.Error("the phone's capsule does not say this is Players")
	}
}

// sectionOf returns the markup from the first occurrence of open up to the
// next close after it.
func sectionOf(body, open, close string) (string, bool) {
	at := strings.Index(body, open)
	if at < 0 {
		return "", false
	}
	rest := body[at:]
	end := strings.Index(rest, close)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// A trait is earned from the figures and explained on hover, so a player
// can find out why rather than guess.
func TestTraitsAppearAndExplainThemselves(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	// seedBoard's hard-mode regulars have several earned descriptions. The
	// one shown rotates with the puzzle, but it must always explain itself.
	page := fetchAs(t, srv, "/share/"+slug+"/players/harda", nil).Body.String()
	if !strings.Contains(page, `class="trait"`) {
		t.Error("a regular with earned descriptions has no trait")
	}
	if !strings.Contains(page, `class="trait-why popup-panel"`) {
		t.Error("the trait does not explain itself")
	}

	// And one who has played three games is a newcomer, not a purist.
	thin := fetchAs(t, srv, "/share/"+slug+"/players/thin", nil).Body.String()
	if !strings.Contains(thin, "New here") {
		t.Error("a player with few games has the wrong trait")
	}
}

// Nothing earned means nothing shown: padding everyone out would make the
// ones that mean something worthless.
func TestNoTraitWhenNothingIsEarned(t *testing.T) {
	t.Parallel()

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

	// Ordinary spread, flat form, gaps so there is no streak to name. The
	// deliberate failure is picked by position among the puzzles actually
	// seeded, not by an absolute puzzle number: current-35 landed on a
	// multiple of 6 on some calendar days, colliding with the gap below and
	// silently dropping the only failure, which left this player flawless
	// instead of earning nothing.
	current := currentPuzzle()
	pattern := []int{3, 4, 5, 3, 4, 5, 3, 4, 6, 3, 4, 5, 2, 4}
	seeded := 0
	for i, puzzle := 0, current-40; puzzle <= current; i, puzzle = i+1, puzzle+1 {
		if puzzle%6 == 0 {
			continue
		}
		if seeded == 5 {
			seedResult(t, srv, p.ID, puzzle, 0, false)
			seeded++
			continue
		}
		seedResult(t, srv, p.ID, puzzle, pattern[i%len(pattern)], false)
		seeded++
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/players/ordinary", nil).Body.String()
	if strings.Contains(body, `class="trait"`) {
		start := strings.Index(body, `class="trait"`)
		t.Errorf("a player who has earned nothing was given a trait anyway: %.120s", body[start:])
	}
}

// The labels are localised like everything else.
func TestTraitsAreLocalised(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	// Thin has too little data, so its fixed newcomer trait does not rotate
	// with the current puzzle as active players' earned traits do.
	sv := fetchAs(t, srv, "/share/"+slug+"/players/thin?lang=sv", nil).Body.String()
	if !strings.Contains(sv, "Nykomling") {
		t.Error("the Swedish page has no trait")
	}
	if !strings.Contains(sv, "Färre än 10 pussel hittills") {
		t.Error("the explanation is still English under ?lang=sv")
	}
}

// The front page is what a signed-in reader lands on, and what the bare
// share URL shows. The leaderboard is a click away, not the doorstep.
func TestTodayIsTheFrontPage(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	root := fetchAs(t, srv, "/share/"+slug+"/", nil)
	if root.Code != http.StatusOK {
		t.Fatalf("the bare share URL = %d", root.Code)
	}
	if !strings.Contains(root.Body.String(), `<h1 class="page-title">Today</h1>`) {
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
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	board := fetchAs(t, srv, "/share/"+slug+"/board", nil).Body.String()
	href := followRule(t, srv, board, "mode", nil)
	if !strings.HasPrefix(href, "/share/"+slug+"/board") {
		t.Errorf("the leaderboard's filter leads to %q, not back to itself", href)
	}
	if !strings.Contains(fetchAs(t, srv, href, nil).Body.String(), "players hidden") {
		t.Error("following the leaderboard's own filter did not filter it")
	}
}

// A player who has left the group is behind the toggle even though they
// were playing right up to the day they left.
func TestGridHidesRetiredPlayersUntilToggled(t *testing.T) {
	t.Parallel()

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
	if strings.Contains(hidden, `title="Harda"`) {
		t.Error("a player who has left the group is still a column")
	}
	if !strings.Contains(hidden, "Inactive (") {
		t.Error("the toggle does not offer them")
	}

	shown := fetchAs(t, srv, "/share/"+slug+"/grid?inactive=1", nil).Body.String()
	if !strings.Contains(shown, `title="Harda"`) {
		t.Error("the toggle did not bring them back")
	}
}

// The winner pane carries the four figures the design puts beside the name.
func seedCompletedWinningMonth(t *testing.T, srv *Server) time.Time {
	t.Helper()
	ctx := context.Background()

	// Give a completed month enough results to have a winner regardless of
	// which side of a month boundary the test happens to run on.
	now := time.Now()
	target := time.Date(now.Year(), now.Month()-2, 1, 0, 0, 0, 0, time.Local)
	for slug, guesses := range map[string]int{"harda": 3, "hardb": 4} {
		player, err := store.PlayerBySlug(ctx, srv.db, slug)
		if err != nil {
			t.Fatalf("PlayerBySlug(%s): %v", slug, err)
		}
		for day := 1; day <= 12; day++ {
			date := time.Date(target.Year(), target.Month(), day, 0, 0, 0, 0, time.Local)
			seedResult(t, srv, player.ID, wordle.PuzzleForDate(date), guesses, true)
		}
	}
	return target
}

// A finished month's own card names who won it, with what, and by how much;
// its table says how far behind everybody else finished and how many of the
// month's days they played.
func TestAFinishedMonthNamesItsWinner(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	target := seedCompletedWinningMonth(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	month := fmt.Sprintf("%d-%d", target.Year(), target.Month())
	body := fetchAs(t, srv, "/share/"+slug+"/months?month="+month, nil).Body.String()
	hero, ok := sectionOf(body, `<section class="card month-hero">`, "</section>")
	if !ok {
		t.Fatal("the completed month has no card of its own")
	}
	if !strings.Contains(hero, " won ") {
		t.Errorf("the completed month does not say who won it:\n%s", hero)
	}
	if !strings.Contains(hero, " ahead of ") {
		t.Errorf("the completed month does not say by how much:\n%s", hero)
	}
	table, _ := sectionOf(body, `<ol class="month-rows">`, "</ol>")
	table = html.UnescapeString(table)
	if !strings.Contains(table, "+0.") || !strings.Contains(table, "/") {
		t.Error("the table does not say how far behind, or played over possible")
	}
}

// A month pill is small: the abbreviated month and no year, and who won it
// on a wide window. The row scrolls rather than wrapping, and the month
// chosen is named in full in its own card below.
func TestMonthPillsAreSmallAndTheMonthIsNamedInFullBelow(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	pills, ok := sectionOf(body, `<nav class="pills"`, "</nav>")
	if !ok {
		t.Fatal("there is no row of months")
	}
	now := time.Now()
	short := now.Month().String()[:3]
	name, ok := sectionOf(pills, `<span class="pill-label month-pill-name">`, "</span>")
	if !ok || !strings.Contains(name, short) {
		t.Errorf("the newest pill %q is not the abbreviated month %q", name, short)
	}
	if strings.Contains(name, strconv.Itoa(now.Year())) {
		t.Errorf("the pill %q carries the year, which the card below names", name)
	}

	full := now.Month().String() + " " + strconv.Itoa(now.Year())
	if hero, _ := sectionOf(body, `<section class="card month-hero">`, "</section>"); !strings.Contains(hero, full) {
		t.Errorf("the chosen month is not named %q in full in its card", full)
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	if !strings.Contains(cssRule(t, css, ".pills {"), "overflow-x: auto") {
		t.Error("the row of months does not scroll")
	}
}

// The front page's form table is headed "form, last 30 days" and prints the
// form figure, so it has to be ordered by form. It was ordered by the
// board's all-time ranking, which put a 3.90 above a 3.59.
func TestFrontPageFormTableIsOrderedByForm(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	list := body[strings.Index(body, "today-form"):]

	// Read the form figures in the order they are printed. The bench rows
	// print a game count in the same cell, so the list stops at the
	// disclosure they live behind.
	if at := strings.Index(list, `class="today-bench"`); at > 0 {
		list = list[:at]
	}
	var figures []float64
	for _, m := range regexp.MustCompile(`class="form-avg num">([^<]+)<`).FindAllStringSubmatch(list, -1) {
		var v float64
		if _, err := fmt.Sscanf(strings.TrimSpace(m[1]), "%f", &v); err == nil {
			figures = append(figures, v)
		}
	}

	if len(figures) < 3 {
		t.Fatalf("only read %d form figures from the list", len(figures))
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

func TestRunningMonthReadsAsUnfinished(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	// The newest month is the one being played, and it is what the view
	// opens on.
	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	hero, ok := sectionOf(body, `<section class="card month-hero">`, "</section>")
	if !ok {
		t.Fatal("the current month has no card")
	}
	if !strings.Contains(hero, "still running") {
		t.Fatalf("the current month is not marked as running:\n%s", hero)
	}
	if strings.Contains(hero, " won ") {
		t.Errorf("a running month is described as finished:\n%s", hero)
	}
	if !strings.Contains(hero, " leads at ") && !strings.Contains(hero, " are level at ") {
		t.Errorf("a running month is not described in the present tense:\n%s", hero)
	}
	if !strings.Contains(hero, " left.") && !strings.Contains(hero, "Last day.") {
		t.Errorf("a running month does not say how much of it is left:\n%s", hero)
	}
	now := time.Now()
	daysInMonth := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
	if want := fmt.Sprintf("Day %d of %d", now.Day(), daysInMonth); !strings.Contains(hero, want) {
		t.Errorf("the current month does not show %q", want)
	}
}

// The month view against the design: the running month's leader carries the
// rising glyph and a finished month's winner the trophy, and the season card
// has its line of places, its wins and a legend for the tiles.
func TestMonthViewMatchesTheDesign(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	target := seedCompletedWinningMonth(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	for _, want := range []string{"Season so far", "place per month", "Won", "Top three", "Other place", "Running"} {
		if !strings.Contains(body, want) {
			t.Errorf("the season card is missing %q", want)
		}
	}
	table, _ := sectionOf(body, `<ol class="month-rows">`, "</ol>")
	if !strings.Contains(table, `title="Leading"`) || strings.Contains(table, `title="Winner"`) {
		t.Error("a running month does not mark a leader, or names a winner")
	}

	month := fmt.Sprintf("%d-%d", target.Year(), target.Month())
	finished := fetchAs(t, srv, "/share/"+slug+"/months?month="+month, nil).Body.String()
	if table, _ := sectionOf(finished, `<ol class="month-rows">`, "</ol>"); !strings.Contains(table, `title="Winner"`) {
		t.Error("a finished month does not mark its winner")
	}
}

// A trophy marks a title, a placing is a number, not ranked is a dot, and a
// month still running is outlined rather than filled.
func TestSeasonMarksReadAtAGlance(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	seedCompletedWinningMonth(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	season, ok := sectionOf(body, `<div class="season-grid"`, `<ul class="season-legend">`)
	if !ok {
		t.Fatal("there is no season grid")
	}
	if !strings.Contains(season, `class="season-tile won`) || !strings.Contains(season, symbolPaths["trophy"][0]) {
		t.Error("no trophy marks a monthly win")
	}
	if !strings.Contains(season, `class="season-tile running`) {
		t.Error("the running month is not marked as running")
	}
}

// The form list: one shape for everybody, and no figure in a row that
// nothing names.
//
// It used to be three cards and a six-column table, and the number beside a
// name read as a game count in the cards and a streak in the table. One row
// shape settles that by construction — every cell in it is either the rank
// (whose popup names both numbers), the form average the section heading
// names, or the delta against it — so this pins the shape rather than the
// headings that used to carry the explanation.
func TestFormPaneIsConsistent(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
	pane := body[strings.Index(body, "today-form"):]
	pane = pane[:strings.Index(pane, "card-foot")]

	if !strings.Contains(pane, `<h2 class="today-list-title">Form</h2>`) || !strings.Contains(pane, ">last five · 30 days<") {
		t.Error("the form list has no heading naming the window")
	}

	// Split on the row opener rather than matched to </li>: the last-five
	// strip inside a row is a list of its own.
	rows := strings.Split(pane, `<li class="form-row">`)[1:]
	if len(rows) == 0 {
		t.Fatal("the form list has no rows")
	}
	ranked, benched := 0, 0
	for _, cell := range rows {
		if strings.Contains(cell, `class="rank-pop form-rank"`) {
			ranked++
			for _, part := range []string{`player form-name"`, `class="form-last-five"`, `class="form-avg num"`, `class="form-delta`} {
				if !strings.Contains(cell, part) {
					t.Errorf("a form row is missing %s", part)
				}
			}
			continue
		}
		benched++
	}
	if ranked == 0 {
		t.Error("no ranked rows in the form list")
	}

	// The unranked are in the markup, behind a disclosure rather than behind
	// a round trip — and never mixed in with the ranked rows.
	if benched > 0 {
		bench := pane[strings.Index(pane, `class="today-bench"`):]
		if strings.Count(bench, `<li class="form-row">`) != benched {
			t.Error("an unranked row is outside the disclosure it belongs in")
		}
	}
}

// A tooltip is a desktop-only affordance. The explanation has to be
// reachable by tapping, which means an element that opens without script.
func TestTraitExplanationIsReachableWithoutHover(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{
		"/share/" + slug + "/",
		"/share/" + slug + "/months",
		"/share/" + slug + "/players/harda",
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

// The board has more columns than a phone is wide. It re-flows there rather
// than scrolling — the design's stacked row, from the same markup — so the
// head and every row carry the same cells in the same order, and the phone
// rule that re-flows them is the one that hides the head.
func TestBoardRowsShareTheHeadsCells(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board", nil).Body.String()
	cells := regexp.MustCompile(`<(?:span|a) class="(b-[a-z]+)`)
	order := func(fragment string) []string {
		var out []string
		for _, m := range cells.FindAllStringSubmatch(fragment, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
		return out
	}
	head, ok := sectionOf(body, `<div class="b-row b-head">`, "</div>")
	if !ok {
		t.Fatal("the board has no head row")
	}
	row := rowFor(t, body, "harda")
	row = row[strings.Index(row, ">")+1:]
	// The row's own cells only: its name line and gap sit inside b-id.
	var rowCells []string
	for _, c := range order(row) {
		if c != "b-name" && c != "b-gap" && c != "b-move" {
			rowCells = append(rowCells, c)
		}
	}
	if got, want := strings.Join(rowCells, " "), strings.Join(order(head), " "); got != want {
		t.Errorf("a row's cells are %q, the head's %q", got, want)
	}

	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	phone := css[strings.LastIndex(css, "@media (max-width: 860px) {\n  .b-head { display: none; }"):]
	if end := strings.Index(phone, "\n}\n"); end < 0 || !strings.Contains(phone[:end], ".b-id { display: contents; }") {
		t.Error("the phone rule does not re-flow the row")
	}
}

// The grid's columns are the standings for the window it is showing, each
// heading carrying the average. A player who was poor all year and excellent
// lately leads the recent view and trails the whole-history one.
//
// That costs what name order bought — a player's column moves when the
// range changes — and it is the design's call: the standings rail that sat
// beside alphabetical columns folded into the header.
func TestGridColumnsFollowTheStandings(t *testing.T) {
	t.Parallel()

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
			}, nil, nil); err != nil {
				t.Fatalf("UpsertResult: %v", err)
			}
		}
	}
	add("steady", 3, 3)   // always 3
	add("improver", 6, 2) // dreadful, then the best lately

	session := signIn(t, srv, admin.ID)
	first := func(path string) string {
		body := fetchAs(t, srv, path, session).Body.String()
		head, ok := sectionOf(body, "<thead>", "</thead>")
		if !ok {
			t.Fatalf("%s rendered without a table head", path)
		}
		steady, improver := strings.Index(head, `title="Steady"`), strings.Index(head, `title="Improver"`)
		if steady < improver {
			return "steady"
		}
		return "improver"
	}
	if got := first("/grid?span=90"); got != "improver" {
		t.Errorf("over the last 90 days %s leads the columns, want improver", got)
	}
	if got := first("/grid?span=all"); got != "steady" {
		t.Errorf("over the whole history %s leads the columns, want steady", got)
	}
}

// A range control that cannot change anything is not offered: a history
// shorter than the recent window is the same grid under both.
func TestGridHidesTheSpanToggleWhenThereIsNothingToChoose(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	ctx := context.Background()
	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Short", "short")
	if err != nil {
		t.Fatal(err)
	}
	current := currentPuzzle()
	for puzzle := current - 9; puzzle <= current; puzzle++ {
		seedResult(t, srv, p.ID, puzzle, 4, false)
	}

	body := fetchAs(t, srv, "/grid", signIn(t, srv, admin.ID)).Body.String()
	if strings.Contains(body, `span=all`) {
		t.Error("the range toggle is offered on a history shorter than the window")
	}
}

// The months head states the rule the code applies, including the missed
// day, and drops the clause when the toggle it depends on is off.
func TestMonthsKickerStatesTheScoringRule(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	const clause = "A missed day counts as 7"

	body := fetchAs(t, srv, "/share/"+slug+"/months", nil).Body.String()
	if !strings.Contains(body, clause) {
		t.Errorf("the head does not say %q, though the average counts them", clause)
	}
	if unwanted := "puzzles minimum"; strings.Contains(body, unwanted) {
		t.Errorf("the head still contains the removed monthly minimum: %q", unwanted)
	}

	plain := fetchAs(t, srv, "/share/"+slug+"/months?failed=0", nil).Body.String()
	if strings.Contains(plain, clause) {
		t.Errorf("the head still says %q with X not counted as 7", clause)
	}
}

func TestTodayShowsFourBanterHeadlinesInBothLanguages(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	p, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Banter", "banter")
	if err != nil {
		t.Fatal(err)
	}
	current := currentPuzzle()
	seedResult(t, srv, p.ID, current-3, 1, false)
	seedResult(t, srv, p.ID, current-2, 2, true)
	seedResult(t, srv, p.ID, current-1, 6, false)
	seedResult(t, srv, p.ID, current, 0, false)
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)
	for _, locale := range []string{"en", "sv"} {
		for _, surface := range []struct {
			path   string
			cookie *http.Cookie
		}{
			{"/today", signIn(t, srv, admin.ID)},
			{"/share/" + slug + "/today", nil},
		} {
			body := fetchAs(t, srv, surface.path+"?lang="+locale, surface.cookie).Body.String()
			if got := strings.Count(body, `class="card callout"`); got != 4 {
				t.Errorf("%s (%s): got %d banter headlines, want four", surface.path, locale, got)
			}
			if got := strings.Count(body, `class="callout-meta"`); got != 4 {
				t.Errorf("%s (%s): got %d detail lines, want four", surface.path, locale, got)
			}
			if strings.Contains(body, "callout.") || strings.Contains(body, "%!") {
				t.Errorf("%s (%s): untranslated or malformed banter", surface.path, locale)
			}
		}
	}
}

func TestEveryBanterHasDetails(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	for _, locale := range []string{"en", "sv"} {
		tr := translator{locale: locale, strings: srv.catalogues[locale], fallback: srv.catalogues["en"]}
		for _, kind := range []string{
			stats.CalloutUnbroken, stats.CalloutOneAndDone, stats.CalloutOnForm,
			stats.CalloutOffForm, stats.CalloutMissing, stats.CalloutQuickSolves,
			stats.CalloutCloseShaves, stats.CalloutStumped, stats.CalloutHardMode,
		} {
			for _, count := range []int{1, 2} {
				c := stats.Callout{Kind: kind, Name: "Banter", Count: count, PuzzleNo: 1890}
				if count == 1 {
					c.Slug = "banter"
				}
				view := srv.calloutFor(c, "", tr)
				if view.Meta == "" || strings.Contains(view.Meta, "callout.") || strings.Contains(view.Meta, "%!") {
					t.Errorf("%s (%s, count %d): missing or malformed details %q", kind, locale, count, view.Meta)
				}
				if kind == stats.CalloutOneAndDone {
					date, _ := wordle.DateForPuzzle(1890)
					want := "Wordle #" + i18n.Identifier(1890) + " · " + date.Format(time.DateOnly)
					if count > 1 {
						prefix := "Latest: "
						if locale == "sv" {
							prefix = "Senaste: "
						}
						want = prefix + want
					}
					if view.Meta != want {
						t.Errorf("%s, count %d: details = %q, want %q", locale, count, view.Meta, want)
					}
				}
			}
		}
	}
}

// Traits are a reading of a player's whole history. Months is about one month
// at a time, so a badge saying "Late finisher" beside a September average
// claims a relation that is not there; the leaderboard already carries eight
// columns of the same reading in numbers; Today's form list is a row of
// figures about the last thirty days; and the admin roster is for renaming
// people, retiring them and attaching logins, none of which a trait bears on.
// In all four the name column is for the name.
func TestTraitBadgesAreOnlyWhereTheyMeanSomething(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	session := signIn(t, srv, admin.ID)

	for _, path := range []string{"/months", "/leaderboard", "/today", "/admin/players"} {
		if strings.Contains(fetchAs(t, srv, path, session).Body.String(), `class="trait"`) {
			t.Errorf("%s carries a trait badge", path)
		}
	}

	// Still where they belong, so this cannot pass by the badge having been
	// deleted everywhere: a player's own page, where the reading is of that
	// player and of nothing else.
	if !strings.Contains(fetchAs(t, srv, "/players/harda", session).Body.String(), `class="trait"`) {
		t.Error("a player's own page lost its trait badge too")
	}
}

// A season's places are centred in their rows, never set on a text
// baseline: a trophy and a digit have different baselines, and a row of
// places that sat at two heights read as a grid that had slipped.
func TestASeasonMarkIsNotAlignedOnTheTextBaseline(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()
	if !strings.Contains(cssRule(t, css, ".season-row {"), "align-items: center") {
		t.Error("the season's rows do not centre their places")
	}
	if !strings.Contains(cssRule(t, css, ".season-tile {"), "align-items: center") {
		t.Error("a place does not centre its trophy or digit")
	}
}

// Today's two lists are read side by side on a wide screen, so a row in one
// has to sit level with the row beside it. They share one rule rather than
// two that agree today.
//
// No vertical padding is the load-bearing part: the page is border-box
// throughout, so a min-height with padding under it counts the padding, and
// a row with nothing tall in it settled short of one carrying a score tile.
// With none, the min-height is the row, whatever is in it.
func TestTodaysTwoListsShareOneRowShape(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	css := fetchAs(t, srv, "/static/app.css", nil).Body.String()

	rule := cssRule(t, css, ".result-row, .form-row {")
	for _, want := range []string{"min-height: 50px", "padding: 0 18px"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the shared row rule is missing %q: %s", want, rule)
		}
	}
}
