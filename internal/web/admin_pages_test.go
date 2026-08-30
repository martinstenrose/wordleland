package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/bridge"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/version"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// holdPending parks a result for an unclaimed sender, which is what the
// pending page exists to resolve.
func holdPending(t *testing.T, srv *Server, externalID, hint string, puzzle, guesses int) {
	t.Helper()
	g := guesses
	err := store.HoldPendingResult(context.Background(), srv.db, "signal", externalID, hint,
		store.PendingResult{PuzzleNo: puzzle, Solved: true, Guesses: &g})
	if err != nil {
		t.Fatalf("HoldPendingResult: %v", err)
	}
}

// Both new pages are admin-only, like the players page they are linked from.
func TestAdminActivityAndPendingAreAdminOnly(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	user := seedLogin(t, srv, "member@example.tld", false)
	session := signIn(t, srv, user.ID)

	for _, path := range []string{"/admin/activity", "/admin/pending"} {
		if got := fetchAs(t, srv, path, session).Code; got != http.StatusNotFound {
			t.Errorf("GET %s as a member = %d, want 404", path, got)
		}
		if got := fetchAs(t, srv, path, nil).Code; got == http.StatusOK {
			t.Errorf("GET %s signed out = 200, want a redirect or 404", path)
		}
	}
}

// The players page is where an admin starts, so it has to lead to the
// other two rather than leaving them to be typed in.
func TestAdminPagesLinkToEachOther(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	for _, from := range []string{"/admin/players", "/admin/activity", "/admin/pending"} {
		body := fetchAs(t, srv, from, session).Body.String()
		for _, to := range []string{"/admin/players", "/admin/activity", "/admin/pending"} {
			if !strings.Contains(body, `href="`+to+`"`) {
				t.Errorf("%s does not link to %s", from, to)
			}
		}
	}
}

// The log is the record of who changed what. An edit made through the UI
// has to show up in it, in the admin's name.
func TestActivityLogShowsAnEdit(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)

	form := url.Values{"name": {"Renamed"}, "slug": {"harda"}, "active": {"1"}}
	if rec := postAdmin(t, srv, "/admin/players/harda", form, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body.String())
	}

	body := fetchAs(t, srv, "/admin/activity", session).Body.String()
	// The slug, not the display name: it is what an admin acts on, it is
	// unique where a display name is not, and it survives the rename this
	// very row is recording.
	if !strings.Contains(body, "harda") {
		t.Error("the log does not name the edited player by slug")
	}
	if strings.Contains(body, "Renamed") {
		t.Error("the log shows the display name rather than the slug")
	}
	if !strings.Contains(body, admin.Email) {
		t.Error("the log does not name the admin who made the change")
	}
}

// Filtering narrows the log rather than emptying it: the categories exist
// so a busy log can be read at all.
func TestActivityFilterNarrowsTheLog(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	form := url.Values{"name": {"Renamed"}, "slug": {"harda"}, "active": {"1"}}
	postAdmin(t, srv, "/admin/players/harda", form, session)

	players := fetchAs(t, srv, "/admin/activity?kind=players", session).Body.String()
	if !strings.Contains(players, "harda") {
		t.Error("the players filter hides a player change")
	}
	results := fetchAs(t, srv, "/admin/activity?kind=results", session).Body.String()
	if strings.Contains(results, "Player renamed") {
		t.Error("the results filter shows a player change")
	}
}

// Assigning a held sender to a player replays what was held, which is the
// whole point: the scores were filed, just not attributable yet.
func TestPendingAssignReplaysHeldResults(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)
	player, err := store.PlayerBySlug(context.Background(), srv.db, "harda")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}
	_ = admin
	holdPending(t, srv, "sender-1", "Martin", 1400, 4)
	holdPending(t, srv, "sender-1", "Martin", 1401, 3)

	body := fetchAs(t, srv, "/admin/pending", session).Body.String()
	if !strings.Contains(body, "1400") {
		t.Error("the pending page does not show what is held")
	}

	form := url.Values{
		"source":      {"signal"},
		"external_id": {"sender-1"},
		"player":      {player.Slug},
	}
	if rec := postAdmin(t, srv, "/admin/pending/assign", form, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("assign = %d: %s", rec.Code, rec.Body.String())
	}

	var replayed, held int
	if err := srv.db.QueryRow(
		`SELECT COUNT(*) FROM results WHERE player_id = ? AND puzzle_no IN (1400, 1401)`,
		player.ID).Scan(&replayed); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if replayed != 2 {
		t.Errorf("replayed %d of the held results, want 2", replayed)
	}
	if err := srv.db.QueryRow(
		`SELECT COUNT(*) FROM pending_results WHERE external_id = 'sender-1'`).Scan(&held); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if held != 0 {
		t.Errorf("%d results still held after assigning", held)
	}
}

// Discarding drops the held results and leaves no scores behind.
func TestPendingDiscardDropsHeldResults(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	holdPending(t, srv, "sender-2", "", 1400, 5)

	form := url.Values{"source": {"signal"}, "external_id": {"sender-2"}}
	if rec := postAdmin(t, srv, "/admin/pending/discard", form, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("discard = %d: %s", rec.Code, rec.Body.String())
	}

	var held, filed int
	srv.db.QueryRow(`SELECT COUNT(*) FROM pending_results WHERE external_id = 'sender-2'`).Scan(&held)
	srv.db.QueryRow(`SELECT COUNT(*) FROM results WHERE puzzle_no = 1400`).Scan(&filed)
	if held != 0 || filed != 0 {
		t.Errorf("held = %d, results filed = %d, want 0 and 0", held, filed)
	}
}

// A suggestion is offered only on an exact name match. Guessing would
// attribute one player's scores to another, which is worse than no help.
func TestPendingSuggestsOnlyOnAnExactMatch(t *testing.T) {
	players := []store.Player{{ID: 1, Name: "Martin"}, {ID: 2, Name: "Alex"}}

	if p, ok := suggestPlayer("martin", players); !ok || p.ID != 1 {
		t.Errorf("case-insensitive exact match not suggested: %v %v", p, ok)
	}
	for _, hint := range []string{"", "Mart", "Martin S", "Sam"} {
		if p, ok := suggestPlayer(hint, players); ok {
			t.Errorf("suggested %q for hint %q", p.Name, hint)
		}
	}
}

// A result event names its puzzle. The number arrives from JSON, where it
// may be a float64 or a string, and the copy's verb is %d — a mismatch
// renders as "Wordle %!d(string=1895)" rather than failing.
func TestActivityLogFormatsEveryLine(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	// Exercise a result event, which is the one carrying a number.
	player, err := store.PlayerBySlug(context.Background(), srv.db, "harda")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}
	holdPending(t, srv, "sender-fmt", "", 1895, 4)
	form := url.Values{"source": {"signal"}, "external_id": {"sender-fmt"}, "player": {player.Slug}}
	if rec := postAdmin(t, srv, "/admin/pending/assign", form, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("assign = %d", rec.Code)
	}

	for _, path := range []string{
		"/admin/activity", "/admin/activity?kind=results",
		"/admin/activity?kind=players", "/admin/activity?kind=users",
	} {
		body := fetchAs(t, srv, path, session).Body.String()
		if i := strings.Index(body, "%!"); i >= 0 {
			t.Errorf("%s renders a formatting error: %q", path, body[i:min(i+60, len(body))])
		}
	}

	results := fetchAs(t, srv, "/admin/activity?kind=results", session).Body.String()
	if !strings.Contains(results, "Wordle 1895") {
		t.Error("the log does not name the puzzle a result belongs to")
	}
	// A result's subject is the player it belongs to. It used to print the
	// raw id, which tells an admin nothing without a second lookup.
	if !strings.Contains(results, player.Slug) {
		t.Errorf("a result row does not name the player %q", player.Slug)
	}
	// Scoped to the log's own text: the page also carries hex colours in
	// its flag icons, which look like ids to a bare pattern.
	rowText := regexp.MustCompile(`(?s)<span class="activity-text">(.*?)</span>`)
	for _, m := range rowText.FindAllStringSubmatch(results, -1) {
		if regexp.MustCompile(`#\d+`).MatchString(m[1]) {
			t.Errorf("a row still falls back to a bare id: %q", m[1])
		}
	}
}

// The actor column holds an email on one row and "matched automatically"
// on the next, so it needs a label saying what the column is. And a header
// that does not line up with its cells is worse than none.
func TestActivityLogHasAlignedHeaders(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/activity", session).Body.String()
	list := body[strings.Index(body, `<ul class="activity">`):]
	list = list[:strings.Index(list, "</ul>")]

	if !strings.Contains(list, "activity-head") {
		t.Fatal("the log has no header row")
	}
	for _, col := range []string{"Type", "Event", "Changed by", "When"} {
		if !strings.Contains(list, ">"+col+"<") {
			t.Errorf("the header is missing %q", col)
		}
	}

	// Count the grid's direct children, whatever element they are: the
	// text cell is a link rather than a span, and the point is that the
	// cells and the header agree, not what tag they use.
	cells := regexp.MustCompile(`<(?:span|a)[^>]*(?:class="(?:chip|activity-text|muted|right)|>)`)
	rows := strings.Split(list, "<li")
	if len(rows) < 3 {
		t.Fatal("no rows under the header to compare against")
	}
	want := len(cells.FindAllString(rows[1], -1))
	if want != 4 {
		t.Errorf("the header has %d cells, want 4", want)
	}
	for i, row := range rows[2:] {
		if got := len(cells.FindAllString(row, -1)); got != want {
			t.Errorf("row %d lays out %d cells against a %d-column header", i, got, want)
		}
	}
}

// A row about an account names it. It used to print the bare user id, so
// "Two-factor set up · #1" told an admin nothing about whose it was.
func TestActivityLogNamesUserSubjects(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)

	if err := store.SetPendingTOTPSecret(context.Background(), srv.db, admin.ID, []byte("secret")); err != nil {
		t.Fatalf("SetPendingTOTPSecret: %v", err)
	}
	if err := store.PromotePendingTOTPSecret(context.Background(), srv.db,
		store.PlayerActor(admin.ID), admin.ID, 1); err != nil {
		t.Fatalf("PromotePendingTOTPSecret: %v", err)
	}

	body := fetchAs(t, srv, "/admin/activity?kind=users", session).Body.String()
	if !strings.Contains(body, "Two-factor set up") {
		t.Fatal("the enrolment is not in the log")
	}
	rowText := regexp.MustCompile(`(?s)<span class="activity-text">(.*?)</span>`)
	for _, m := range rowText.FindAllStringSubmatch(body, -1) {
		if strings.Contains(m[1], "Two-factor set up") {
			if !strings.Contains(m[1], admin.Email) {
				t.Errorf("the row does not name the account: %q", m[1])
			}
			return
		}
	}
}

// A row opens what it actually changed. The scoreline is the point for a
// result: "Score corrected" without the score says nothing.
func TestActivityDetailShowsAResultChange(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)
	player, err := store.PlayerBySlug(ctx, srv.db, "harda")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}

	// Overwrite a result, so the event carries a before and an after.
	date, _ := wordle.DateForPuzzle(1500)
	actor := store.AdminActor(admin.ID)
	for _, g := range []int{4, 5} {
		n := g
		r := store.Result{
			PuzzleNo: 1500, Date: date, PlayerID: player.ID,
			Guesses: &n, Solved: true, EnteredBy: &admin.ID,
		}
		outcome, previous, err := store.UpsertResult(ctx, srv.db, r, &admin.ID)
		if err != nil {
			t.Fatalf("UpsertResult: %v", err)
		}
		action := store.ActionResultCreated
		if outcome == store.OutcomeUpdated {
			action = store.ActionResultUpdated
		}
		if err := store.AuditResult(ctx, srv.db, actor, action, player.ID, r, previous); err != nil {
			t.Fatalf("AuditResult: %v", err)
		}
	}

	list := fetchAs(t, srv, "/admin/activity?kind=results", session).Body.String()
	href := regexp.MustCompile(`/admin/activity/(\d+)`).FindString(list)
	if href == "" {
		t.Fatal("no row links to its detail")
	}

	body := fetchAs(t, srv, href, session).Body.String()
	if !strings.Contains(body, "5/6") {
		t.Error("the detail does not show the score that was stored")
	}
	if !strings.Contains(body, "4/6") {
		t.Error("the detail does not show the score it replaced")
	}
	if !strings.Contains(body, player.Slug) {
		t.Errorf("the detail does not name the player %q", player.Slug)
	}
	if strings.Contains(body, "%!") {
		t.Error("the detail renders a formatting error")
	}
}

// A rename shows both names, which is the whole reason to open the row.
func TestActivityDetailShowsARename(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	form := url.Values{"name": {"Renamed"}, "slug": {"harda"}, "active": {"1"}}
	if rec := postAdmin(t, srv, "/admin/players/harda", form, session); rec.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d", rec.Code)
	}

	list := fetchAs(t, srv, "/admin/activity?kind=players", session).Body.String()
	href := regexp.MustCompile(`/admin/activity/(\d+)`).FindString(list)
	body := fetchAs(t, srv, href, session).Body.String()

	if !strings.Contains(body, "Renamed") {
		t.Error("the detail does not show the new name")
	}
	if !strings.Contains(body, "Harda") {
		t.Error("the detail does not show the name it replaced")
	}
}

// The detail is admin-only and bounded to what the log surfaces, or it
// becomes a way to read rows the list deliberately filters out.
func TestActivityDetailIsGuarded(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	member := seedLogin(t, srv, "member@example.tld", false)
	memberSession := signIn(t, srv, member.ID)
	_, session := adminSession(t, srv)

	if got := fetchAs(t, srv, "/admin/activity/1", memberSession).Code; got != http.StatusNotFound {
		t.Errorf("as a member = %d, want 404", got)
	}
	if got := fetchAs(t, srv, "/admin/activity/999999", session).Code; got != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", got)
	}
	if got := fetchAs(t, srv, "/admin/activity/not-a-number", session).Code; got != http.StatusNotFound {
		t.Errorf("non-numeric id = %d, want 404", got)
	}
}

// "Changed by" asks who made the change. A token write is done by whatever
// the operator labelled the token, so the column says that — it used to say
// "matched automatically", which answers how the score was attributed
// rather than who entered it.
func TestActivityNamesTheTokenThatWrote(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	seedBoard(t, srv)
	admin, session := adminSession(t, srv)

	_, token, err := store.CreateAPIToken(ctx, srv.db, store.AdminActor(admin.ID), "import-script", nil)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	player, err := store.PlayerBySlug(ctx, srv.db, "harda")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}

	date, _ := wordle.DateForPuzzle(1501)
	guesses := 3
	r := store.Result{PuzzleNo: 1501, Date: date, PlayerID: player.ID, Guesses: &guesses, Solved: true}
	if _, _, err := store.UpsertResult(ctx, srv.db, r, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}
	if err := store.AuditResult(ctx, srv.db, store.TokenActor(token.ID),
		store.ActionResultCreated, player.ID, r, nil); err != nil {
		t.Fatalf("AuditResult: %v", err)
	}

	body := fetchAs(t, srv, "/admin/activity?kind=results", session).Body.String()
	if !strings.Contains(body, "import-script") {
		t.Error("the log does not name the token that wrote the result")
	}
	if strings.Contains(body, "matched automatically") {
		t.Error("the actor column still describes the matching rather than the actor")
	}
}

// A result the Signal bridge filed says so. It writes as the application
// itself, so without this the column reads "system" — the same word the
// share slug gets, on a log that is mostly bridge writes.
func TestActivityNamesTheBridge(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	player, err := store.PlayerBySlug(ctx, srv.db, "harda")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}
	date, _ := wordle.DateForPuzzle(1502)
	guesses := 4
	r := store.Result{PuzzleNo: 1502, Date: date, PlayerID: player.ID, Guesses: &guesses, Solved: true}
	if _, _, err := store.UpsertResult(ctx, srv.db, r, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}
	if err := store.AuditResultVia(ctx, srv.db, store.SystemActor(),
		store.ActionResultCreated, player.ID, r, nil, bridge.SourceSignal); err != nil {
		t.Fatalf("AuditResultVia: %v", err)
	}

	body := fetchAs(t, srv, "/admin/activity?kind=results", session).Body.String()
	if !strings.Contains(body, "Signal bridge") {
		t.Error("a bridge-filed result is not attributed to the bridge")
	}
}

// A system row with no source keeps the generic name: minting the share
// slug is the application acting, not the bridge.
func TestActivityLeavesOtherSystemRowsGeneric(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	// EnsureShareSlug audits as the system actor with no via.
	if _, _, err := store.EnsureShareSlug(context.Background(), srv.db); err != nil {
		t.Fatalf("EnsureShareSlug: %v", err)
	}

	body := fetchAs(t, srv, "/admin/activity", session).Body.String()
	if strings.Contains(body, "Signal bridge") {
		t.Error("a system row with no source was attributed to the bridge")
	}
}

// A configuration signal-cli says cannot work is shown as broken, with the
// reason, rather than leaving an admin to read "connected" and believe it.
func TestDiagnosticsShowsAFailedVerification(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	srv.SetBridge(fakeBridge{
		alive: true,
		status: bridge.Status{
			Connected: true,
			Since:     time.Now().Add(-time.Hour),
			Account:   "46700000000",
			Verification: bridge.Verification{
				Done:    true,
				Problem: "SIGNAL_ACCOUNT is not registered with signal-cli",
			},
		},
	})

	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
	if !strings.Contains(body, "cannot work") {
		t.Error("a failed verification is not shown as a failure")
	}
	if !strings.Contains(body, "SIGNAL_ACCOUNT is not registered") {
		t.Error("the reason is not shown, so an admin cannot act on it")
	}
	// Shown whole, so it can be compared against the environment file that
	// produced it — which is how the missing + would have been spotted.
	if !strings.Contains(body, "46700000000") {
		t.Errorf("the watched account is not shown")
	}
}

// A verified configuration says so, so "connected" means something.
func TestDiagnosticsShowsAConfirmedConfiguration(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	srv.SetBridge(fakeBridge{
		alive: true,
		status: bridge.Status{
			Connected: true,
			Since:     time.Now().Add(-time.Hour),
			Account:   "+46700000000",
			Verification: bridge.Verification{
				Done: true, AccountOK: true, GroupOK: true,
				At: time.Now().Add(-10 * time.Minute),
			},
		},
	})

	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
	// Naming the authority is not the same as naming the claim. The row
	// says what was established, so a reader can judge it rather than
	// take its word.
	if !strings.Contains(body, "the account is registered, and a member of this group") {
		t.Error("a verified configuration does not say what was verified")
	}
	if !strings.Contains(body, "Last checked") {
		t.Error("a verdict is shown without saying how old it is")
	}
	if strings.Contains(body, "cannot work") {
		t.Error("a verified configuration was reported as broken")
	}
}

// "Last message seen: never" was the decisive evidence during an eight-hour
// outage and it read as ambiguous: never since boot, or never at all? The
// two states answer different questions, so they carry different hints.
func TestDiagnosticsExplainsLastMessageSeen(t *testing.T) {
	for _, tt := range []struct {
		name    string
		last    time.Time
		want    string
		notWant string
	}{
		{
			name:    "nothing has arrived yet",
			want:    "since the app started",
			notWant: "not only results",
		},
		{
			// The count is of frames, not results. Without saying so, a
			// reader takes a recent timestamp as proof that scores are
			// being filed, which is the belief that hid the outage.
			name:    "something has arrived",
			last:    time.Now().Add(-2 * time.Minute),
			want:    "not only results",
			notWant: "since the app started",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := testServer(t)
			seedBoard(t, srv)
			_, session := adminSession(t, srv)

			srv.SetBridge(fakeBridge{
				alive: true,
				status: bridge.Status{
					Connected:   true,
					Since:       time.Now().Add(-time.Hour),
					LastMessage: tt.last,
				},
			})

			body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
			if !strings.Contains(body, tt.want) {
				t.Errorf("the hint does not explain the row: want %q", tt.want)
			}
			if strings.Contains(body, tt.notWant) {
				t.Errorf("the hint for the other state was shown: %q", tt.notWant)
			}
		})
	}
}

// Both configured values are shown in full. The page is behind requireAdmin
// and the reader is whoever set them, so a partial value only makes the
// comparison against the environment file harder — and that comparison is
// the whole purpose of these rows.
func TestDiagnosticsShowsAccountAndGroupInFull(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	srv.SetBridge(fakeBridge{
		alive: true,
		status: bridge.Status{
			Connected: true,
			Since:     time.Now().Add(-time.Hour),
			Account:   "+46700000000",
			Group:     "c2FtcGxlLWdyb3VwLWlk",
			Verification: bridge.Verification{
				Done: true, AccountOK: true, GroupOK: true,
				GroupName: "Wordle",
			},
		},
	})

	// Unescaped first: html/template writes a leading + as &#43;, so a raw
	// body assertion would quietly be checking a different string than the
	// one an admin reads.
	body := html.UnescapeString(fetchAs(t, srv, "/admin/diagnostics", session).Body.String())
	for _, want := range []string{
		"<code>+46700000000</code>",         // the account, unmasked and not linkified
		"<code>c2FtcGxlLWdyb3VwLWlk</code>", // the group id, whole
		"Wordle",               // and what signal-cli says it is called
	} {
		if !strings.Contains(body, want) {
			t.Errorf("diagnostics does not show %q", want)
		}
	}
	if strings.Contains(body, "…") {
		t.Error("a value was still elided")
	}
}

// The name is what proves the account can see the group, so it appears only
// when signal-cli actually confirmed it. Claiming a name from configuration
// alone would assert exactly the thing the row exists to establish.
func TestDiagnosticsNamesTheGroupOnlyWhenVerified(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	srv.SetBridge(fakeBridge{
		alive: true,
		status: bridge.Status{
			Connected: true,
			Since:     time.Now().Add(-time.Hour),
			Group:     "c2FtcGxlLWdyb3VwLWlk",
		},
	})

	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
	if !strings.Contains(body, "c2FtcGxlLWdyb3VwLWlk") {
		t.Error("the configured group is not shown before verification")
	}
	if strings.Contains(body, "confirms this is") {
		t.Error("an unverified group was described as confirmed")
	}
}

// Which build is running was the one question this page could not answer,
// and answering it by hand cost real time: an image was deployed, the page
// was read, and the container turned out to predate the change.
func TestDiagnosticsReportsTheRunningVersion(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
	if !strings.Contains(body, "Version") {
		t.Error("the page does not report which build is running")
	}
	if !strings.Contains(body, version.String()) {
		t.Errorf("the page does not show %q", version.String())
	}
	if !strings.Contains(body, "<code>"+version.String()+"</code>") {
		t.Error("the running version is not formatted as code")
	}
}
