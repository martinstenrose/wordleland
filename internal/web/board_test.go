package web

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// seedBoard creates a roster shaped like the real one: a few players who
// play hard mode near-exclusively, and more who never do. An evenly spread
// roster would hide the filter's edge cases entirely.
func seedBoard(t *testing.T, srv *Server) {
	t.Helper()
	ctx := context.Background()

	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	actor := store.AdminActor(admin.ID)

	current := wordle.PuzzleForDate(time.Now())

	add := func(slug string, from, to, guesses int, hardMode bool) store.Player {
		p, err := store.CreatePlayer(ctx, srv.db, actor, strings.ToUpper(slug[:1])+slug[1:], slug)
		if err != nil {
			t.Fatalf("CreatePlayer(%s) failed: %v", slug, err)
		}
		for puzzle := from; puzzle <= to; puzzle++ {
			date, err := wordle.DateForPuzzle(puzzle)
			if err != nil {
				t.Fatalf("DateForPuzzle: %v", err)
			}
			g := guesses
			if _, _, err := store.UpsertResult(ctx, srv.db, store.Result{
				PuzzleNo: puzzle, Date: date, PlayerID: p.ID,
				Guesses: &g, Solved: true, HardMode: hardMode,
			}, nil); err != nil {
				t.Fatalf("UpsertResult: %v", err)
			}
		}
		return p
	}

	// Two hard-mode regulars, ranked.
	add("harda", current-25, current, 3, true)
	add("hardb", current-25, current, 4, true)
	// Two who never play hard mode, also ranked.
	add("normala", current-25, current, 5, false)
	add("normalb", current-25, current, 2, false)
	// One with too few games, and one who stopped long ago.
	add("thin", current-3, current, 3, false)
	add("lapsed", current-200, current-170, 3, false)
}

func fetch(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = clientAddr(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// The share board is the same board, which is what people are sent the link
// for — but every link on it must stay under the share prefix.
func TestShareBoardMirrorsTheAuthenticatedOne(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	slug, _, err := store.EnsureShareSlug(context.Background(), srv.db)
	if err != nil {
		t.Fatalf("EnsureShareSlug: %v", err)
	}

	shared := fetch(t, srv, "/share/"+slug+"/board")
	if shared.Code != http.StatusOK {
		t.Fatalf("share board status = %d", shared.Code)
	}
	body := shared.Body.String()

	// The same players appear.
	for _, slugName := range []string{"harda", "normala"} {
		if !strings.Contains(body, slugName) {
			t.Errorf("share board does not mention %s", slugName)
		}
	}
	// Player links stay under the share prefix: following /p/... directly
	// would send an anonymous visitor into authenticated routing.
	if strings.Contains(body, `href="/p/`) {
		t.Error("the share board links into authenticated routing")
	}
	if !strings.Contains(body, `href="/share/`+slug+`/p/`) {
		t.Error("the share board does not link players under its own prefix")
	}
	// And it offers no authenticated surface.
	if strings.Contains(body, "/logout") {
		t.Error("the share board offers sign-out")
	}
}

// rowFor returns the table row belonging to one player, so an assertion
// lands on that player's cells rather than anywhere on the page.
func rowFor(t *testing.T, body, slug string) string {
	t.Helper()
	i := strings.Index(body, "/p/"+slug+`"`)
	if i < 0 {
		t.Fatalf("no row for %s", slug)
	}
	start := strings.LastIndex(body[:i], "<tr")
	end := strings.Index(body[i:], "</tr>")
	if start < 0 || end < 0 {
		t.Fatalf("malformed row for %s", slug)
	}
	return body[start : i+end]
}

// An unranked player keeps their raw scores, but the average, form and
// trend are withheld — a figure over three games invites exactly the
// comparison that separating them out exists to prevent.
func TestUnrankedPlayersHaveTheirFiguresWithheld(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	body := fetch(t, srv, "/share/"+slug+"/board").Body.String()

	// "thin" has four games, all of them 3s.
	thin := rowFor(t, body, "thin")
	if strings.Contains(thin, "3.00") {
		t.Errorf("an unranked player is showing a computed average:\n%s", thin)
	}
	if !strings.Contains(thin, "low data") {
		t.Errorf("the unranked row carries no reason:\n%s", thin)
	}
	if !strings.Contains(thin, "—") {
		t.Errorf("a withheld figure is not shown as a dash:\n%s", thin)
	}
	// The games they did play are still reported.
	if !strings.Contains(thin, ">4<") {
		t.Errorf("the unranked row hides the game count too:\n%s", thin)
	}

	// A ranked player still shows theirs, so the check above is not passing
	// simply because no average renders anywhere.
	if ranked := rowFor(t, body, "harda"); !strings.Contains(ranked, "3.00") {
		t.Errorf("a ranked player is missing their average:\n%s", ranked)
	}
}

// Ineligible players are listed separately with their reason, not ranked.
func TestUnrankedPlayersAreSeparatedWithAReason(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	body := fetch(t, srv, "/share/"+slug+"/board").Body.String()

	if !strings.Contains(body, "Not ranked") {
		t.Error("no not-ranked divider")
	}
	for _, reason := range []string{"low data", "no recent puzzles"} {
		if !strings.Contains(body, reason) {
			t.Errorf("reason %q is missing from the board", reason)
		}
	}
}

// Last five shows the last five calendar days, including today: a day the
// player didn't reach is a gap, not a score.
func TestLastFiveShowsGapsForUnplayedDays(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	body := fetch(t, srv, "/share/"+slug+"/board").Body.String()

	// "thin" only started three days before today, so one of the five most
	// recent calendar days has no result.
	thin := rowFor(t, body, "thin")
	if got := strings.Count(thin, `class="cell gap tiny"`); got != 1 {
		t.Errorf("thin's last five has %d gaps, want 1:\n%s", got, thin)
	}
	if got := strings.Count(thin, `class="cell t3 tiny"`); got != 4 {
		t.Errorf("thin's last five has %d threes, want 4:\n%s", got, thin)
	}

	// "lapsed" hasn't played in the last five days at all.
	lapsed := rowFor(t, body, "lapsed")
	if got := strings.Count(lapsed, `class="cell gap tiny"`); got != 5 {
		t.Errorf("lapsed's last five has %d gaps, want 5:\n%s", got, lapsed)
	}
}

// A last-five cell carries its detail — which puzzle, and when — behind a
// tap rather than a hover, which never worked on a phone. Same treatment as
// the grid and the player page's own recent strip.
func TestLastFiveCellsOpenAPopupInsteadOfHovering(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	body := fetch(t, srv, "/share/"+slug+"/board").Body.String()

	harda := rowFor(t, body, "harda")
	if strings.Contains(harda, `tiny" title="`) {
		t.Error("a last-five cell still carries the hover the popup replaced")
	}
	if got := strings.Count(harda, `<details class="cell-pop" name="popup">`); got != 5 {
		t.Errorf("harda's last five has %d popups, want 5:\n%s", got, harda)
	}

	current := wordle.PuzzleForDate(time.Now())
	date, err := wordle.DateForPuzzle(current)
	if err != nil {
		t.Fatalf("DateForPuzzle(%d): %v", current, err)
	}
	if !strings.Contains(harda, ">3*<") {
		t.Errorf("harda's last five does not show the hard-mode asterisk:\n%s", harda)
	}
	want := fmt.Sprintf(">#%d (%s)<", current, date.Format(time.DateOnly))
	if !strings.Contains(harda, want) {
		t.Errorf("the popup does not show %q:\n%s", want, harda)
	}
}

// Last game looks at the whole history, not just the last five or thirty
// days, so a lapsed player still shows their real last game rather than
// nothing.
func TestLastGameShowsTheRealLastPuzzleEvenWhenLapsed(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)

	current := wordle.PuzzleForDate(time.Now())
	lastPuzzle := current - 170
	date, err := wordle.DateForPuzzle(lastPuzzle)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}

	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	body := fetch(t, srv, "/share/"+slug+"/board").Body.String()

	lapsed := rowFor(t, body, "lapsed")
	if want := ">" + strconv.Itoa(lastPuzzle) + "<"; !strings.Contains(lapsed, want) {
		t.Errorf("lapsed's last game does not show puzzle %d:\n%s", lastPuzzle, lapsed)
	}
	if want := date.Format(time.DateOnly); !strings.Contains(lapsed, want) {
		t.Errorf("lapsed's last game does not show its date %s:\n%s", want, lapsed)
	}
	// It joins the page's one shared popup group (see base.html), the same
	// as a trait's explanation or a recent-strip cell's detail.
	if !strings.Contains(lapsed, `<details class="lastgame-pop" name="popup">`) {
		t.Error("the last-game popup does not share name=\"popup\" with the rest of the page")
	}
}

// The controls have to change the board, not just the URL.
func TestHardModeFilterChangesTheBoard(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	all := fetch(t, srv, "/share/"+slug+"/board").Body.String()
	hard := fetch(t, srv, "/share/"+slug+"/board?mode=hard").Body.String()

	if !strings.Contains(all, "normala") {
		t.Fatal("a normal-mode player is missing from the unfiltered board")
	}
	// Under the filter they are absent entirely rather than labelled
	// inactive: they are not ranked low, they play a different game.
	if strings.Contains(hard, ">Normala<") {
		t.Error("a player with no hard-mode games is still listed under the filter")
	}
	// Match the sentence, not the word: "hidden" alone also matches the
	// aria-hidden on every sparkline, so the assertion would pass regardless.
	if !strings.Contains(hard, "4 players hidden") {
		t.Error("the board does not say how many players the filter excluded")
	}
	if !strings.Contains(hard, "harda") {
		t.Error("a hard-mode player is missing from the filtered board")
	}
}

func TestTogglesAreReflectedInTheBoard(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	def := fetch(t, srv, "/share/"+slug+"/board").Body.String()
	if !strings.Contains(def, "Averages count X as 7") {
		t.Error("the default board does not state the X-as-7 default")
	}

	off := fetch(t, srv, "/share/"+slug+"/board?failed=0").Body.String()
	if !strings.Contains(off, "Failures are excluded") {
		t.Error("turning the toggle off is not reflected in the footer")
	}

	missed := fetch(t, srv, "/share/"+slug+"/board?missed=1").Body.String()
	if !strings.Contains(missed, "Missed days count as 7") {
		t.Error("counting missed days is not reflected in the footer")
	}
}

// A player drops off the ranked table for either of two reasons — too few
// games ever, or none recently — but the footer only used to explain the
// first. A reader watching a lapsed friend disappear from the board had no
// way to learn why from the page itself.
func TestFooterExplainsBothRankingThresholds(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetch(t, srv, "/share/"+slug+"/board").Body.String()
	if !strings.Contains(body, "10 or more puzzles") {
		t.Error("the footer does not state the minimum-games threshold")
	}
	if !strings.Contains(body, "last 30 days") {
		t.Error("the footer does not state the recent-activity threshold")
	}
}

// The two toggles turn independently, but "count missed" has no effect
// without "count failed" — the page has to say so, or a reader who selects
// both, then turns failures off, sees no reason their average did not move.
func TestCountMissedIsMarkedMootWithoutCountFailed(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	def := fetch(t, srv, "/share/"+slug+"/board?missed=1").Body.String()
	if strings.Contains(def, "toggle on moot") {
		t.Error("count missed is marked moot while count failed is on")
	}

	off := fetch(t, srv, "/share/"+slug+"/board?failed=0&missed=1").Body.String()
	if !strings.Contains(off, "toggle on moot") {
		t.Error("count missed is not marked moot once count failed is turned off")
	}
}

// A missing key must be visible rather than blank, or a translation gap
// looks like a data problem and gets debugged as one.
func TestMissingTranslationKeyRendersTheKey(t *testing.T) {
	tr := translator{locale: "en", strings: catalogue{}, fallback: catalogue{}}
	if got := tr.T("board.title"); got != "board.title" {
		t.Errorf("T() = %q for a missing key, want the key itself", got)
	}
}

func TestTranslatorFallsBackToEnglish(t *testing.T) {
	srv := testServer(t)
	tr := translator{
		locale:   "sv",
		strings:  catalogue{},
		fallback: srv.catalogues["en"],
	}
	if got := tr.T("board.title"); got != "The board" {
		t.Errorf("T() = %q, want the English fallback", got)
	}
}

// An empty board must explain itself rather than render an empty table.
func TestEmptyBoardExplainsItself(t *testing.T) {
	srv := testServer(t)
	slug, _, err := store.EnsureShareSlug(context.Background(), srv.db)
	if err != nil {
		t.Fatalf("EnsureShareSlug: %v", err)
	}

	body := fetch(t, srv, "/share/"+slug+"/board").Body.String()
	if !strings.Contains(body, "No results yet") {
		t.Errorf("an empty board says nothing:\n%s", body)
	}
}

func TestPluralFormsAreSelected(t *testing.T) {
	srv := testServer(t)
	tr := translator{locale: "en", strings: srv.catalogues["en"], fallback: srv.catalogues["en"]}

	if got := tr.TN("board.mode.excluded", 1); !strings.HasPrefix(got, "1 player ") {
		t.Errorf("TN(1) = %q, want the singular form", got)
	}
	if got := tr.TN("board.mode.excluded", 4); !strings.HasPrefix(got, "4 players ") {
		t.Errorf("TN(4) = %q, want the plural form", got)
	}
}

// "GET /share/{slug}/" matches its whole subtree, so an unknown path under
// the prefix must be refused rather than quietly answered with the board.
func TestUnknownPathsUnderTheSharePrefixAre404(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{
		"/share/" + slug + "/utter/nonsense",
		"/share/" + slug + "/p/nobody",
	} {
		if got := fetch(t, srv, path).Code; got != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, got)
		}
	}
	// The board itself still answers.
	if got := fetch(t, srv, "/share/"+slug+"/board").Code; got != http.StatusOK {
		t.Errorf("GET the share board = %d, want 200", got)
	}
}

// signIn gives the test a live session cookie without walking the whole
// password-and-TOTP flow, which is covered elsewhere.
func signIn(t *testing.T, srv *Server, userID int64) *http.Cookie {
	t.Helper()
	session, err := store.CreateSession(context.Background(), srv.db, userID, false)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &http.Cookie{
		Name:  sessionCookieName,
		Value: base64.RawURLEncoding.EncodeToString(session.ID),
	}
}

func fetchAs(t *testing.T, srv *Server, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = clientAddr(t)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// hrefFor returns the target of the control whose label is text, so a test
// follows the link the page really renders.
func hrefFor(t *testing.T, body, text string) string {
	t.Helper()
	i := strings.Index(body, ">"+text+"<")
	if i < 0 {
		t.Fatalf("no control labelled %q on the page", text)
	}
	tag := body[strings.LastIndex(body[:i], "<a "):i]
	j := strings.Index(tag, `href="`)
	if j < 0 {
		t.Fatalf("control %q has no href", text)
	}
	rest := tag[j+len(`href="`):]
	return html.UnescapeString(rest[:strings.Index(rest, `"`)])
}

// The controls have to work on the authenticated board too, not only on the
// shared one. They were built from the share prefix, which is empty on
// /leaderboard, so every control pointed at "/" — the login route — and doing
// anything with them silently returned an unchanged board. Following the
// rendered href rather than a hand-written URL is the whole point of this
// test: the previous version asserted against URLs it built itself and so
// never touched the broken ones.
func TestControlsWorkOnBothBoards(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	admin, err := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	cookie := signIn(t, srv, admin.ID)

	for _, board := range []struct {
		name   string
		path   string
		cookie *http.Cookie
	}{
		{"authenticated", "/leaderboard", cookie},
		{"shared", "/share/" + slug + "/board", nil},
	} {
		t.Run(board.name, func(t *testing.T) {
			rec := fetchAs(t, srv, board.path, board.cookie)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", board.path, rec.Code)
			}
			body := rec.Body.String()

			for _, control := range []string{"Hard mode", "Count missed as 7", "All"} {
				href := hrefFor(t, body, control)
				if !strings.HasPrefix(href, board.path) {
					t.Errorf("%q links to %q, which is not under the board at %q",
						control, href, board.path)
				}
				if got := fetchAs(t, srv, href, board.cookie).Code; got != http.StatusOK {
					t.Errorf("following %q to %q = %d, want 200", control, href, got)
				}
			}

			// And the filter link does not merely resolve — it changes the
			// board it resolves to.
			hard := fetchAs(t, srv, hrefFor(t, body, "Hard mode"), board.cookie).Body.String()
			if !strings.Contains(hard, "players hidden") {
				t.Errorf("following the hard-mode control left the board unfiltered")
			}
			if strings.Contains(hard, ">Normala<") {
				t.Error("a player with no hard-mode games survived the filter")
			}
		})
	}
}

func currentPuzzle() int { return wordle.PuzzleForDate(time.Now()) }

func seedResult(t *testing.T, srv *Server, playerID int64, puzzle, guesses int, hardMode bool) {
	t.Helper()
	date, err := wordle.DateForPuzzle(puzzle)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}
	g := guesses
	if _, _, err := store.UpsertResult(context.Background(), srv.db, store.Result{
		PuzzleNo: puzzle, Date: date, PlayerID: playerID,
		Guesses: &g, Solved: guesses > 0, HardMode: hardMode,
	}, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}
}
