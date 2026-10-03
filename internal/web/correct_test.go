package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// correctFixture is the seeded board with a login linked to harda, who has a
// 3 in hard mode on every recent puzzle, and that login's session.
func correctFixture(t *testing.T) (*Server, store.User, store.Player, *http.Cookie, int) {
	t.Helper()
	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()

	user, session := settingsUser(t, srv, "harda@example.tld", "correct horse battery staple", false)
	admin, _ := store.UserByEmail(ctx, srv.db, "admin@example.tld")
	player, _ := store.PlayerBySlug(ctx, srv.db, "harda")
	if _, err := store.LinkPlayer(ctx, srv.db, store.AdminActor(admin.ID), player.ID, &user.ID); err != nil {
		t.Fatalf("LinkPlayer: %v", err)
	}
	return srv, user, player, session, wordle.PuzzleForDate(time.Now()) - 1
}

// A player corrects their own score; it lands on the board, wins over the
// bridge, and the Puzzle page tells everyone it was corrected and from what.
func TestAPlayerCorrectsTheirOwnResult(t *testing.T) {
	t.Parallel()

	srv, user, player, session, n := correctFixture(t)
	ctx := context.Background()
	path := "/puzzle/" + strconv.Itoa(n) + "/correct"

	form := fetchAs(t, srv, path, session)
	if form.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, form.Code)
	}
	if !strings.Contains(form.Body.String(), `<option value="3" selected>`) {
		t.Error("the form does not start from the score as it stands")
	}

	rec := postSettings(t, srv, path, url.Values{
		"score": {"4"}, "reason": {"typed it wrong"},
	}, session)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/puzzle/"+strconv.Itoa(n) {
		t.Fatalf("POST = %d to %q, want 303 to the puzzle", rec.Code, rec.Header().Get("Location"))
	}

	got, err := store.ResultFor(ctx, srv.db, n, player.ID)
	if err != nil {
		t.Fatalf("ResultFor: %v", err)
	}
	if *got.Guesses != 4 || got.HardMode || got.EnteredBy == nil || *got.EnteredBy != user.ID {
		t.Fatalf("result = %+v, want a 4, not hard mode, entered by the player's login", got)
	}

	// The trail, as everyone reads it — the share view included.
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)
	for _, p := range []string{"/puzzle/" + strconv.Itoa(n), "/share/" + slug + "/puzzle/" + strconv.Itoa(n)} {
		body := fetchAs(t, srv, p, session).Body.String()
		if !strings.Contains(body, "Corrected 3* → 4 · ") || !strings.Contains(body, "“typed it wrong”") {
			t.Errorf("%s does not show the correction", p)
		}
	}

	// And the admin's log, with who did it.
	events, _, err := store.ListActivity(ctx, srv.db, store.ActivityResults, 10)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(events) == 0 || events[0].Action != store.ActionResultUpdated || events[0].ActorKind != store.ActorPlayer ||
		!strings.Contains(events[0].Detail, `"previous"`) {
		t.Fatalf("latest result activity = %+v, want the player's update with the previous score", events)
	}
}

// Only the reader's own row offers the correction, and only where the reader
// is signed in: the share link changes nothing, even opened by a reader
// who has a session.
func TestOnlyTheReadersOwnRowOffersACorrection(t *testing.T) {
	t.Parallel()

	srv, _, _, session, n := correctFixture(t)
	page := "/puzzle/" + strconv.Itoa(n)
	link := `href="` + page + `/correct"`

	if got := strings.Count(fetchAs(t, srv, page, session).Body.String(), link); got != 1 {
		t.Errorf("the puzzle page carries %d correction links, want 1", got)
	}
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	if body := fetchAs(t, srv, "/share/"+slug+page, session).Body.String(); strings.Contains(body, "/correct") {
		t.Error("the share view offers a correction")
	}
	admin, _ := store.UserByEmail(context.Background(), srv.db, "admin@example.tld")
	if body := fetchAs(t, srv, page, signIn(t, srv, admin.ID)).Body.String(); strings.Contains(body, link) {
		t.Error("a login with no player is offered a correction")
	}
}

// Nothing that is not the reader's own existing result can be corrected,
// whatever is typed into the address bar.
func TestACorrectionIsRefusedWhereItIsNotTheReaders(t *testing.T) {
	t.Parallel()

	srv, _, player, session, n := correctFixture(t)
	ctx := context.Background()

	// A puzzle harda never played.
	if rec := postSettings(t, srv, "/puzzle/5/correct", url.Values{"score": {"2"}}, session); rec.Code != http.StatusNotFound {
		t.Errorf("correcting a result that does not exist = %d, want 404", rec.Code)
	}
	// A login linked to nobody.
	_, stranger := settingsUser(t, srv, "stranger@example.tld", "correct horse battery staple", false)
	path := "/puzzle/" + strconv.Itoa(n) + "/correct"
	if rec := postSettings(t, srv, path, url.Values{"score": {"2"}}, stranger); rec.Code != http.StatusNotFound {
		t.Errorf("correcting with no linked player = %d, want 404", rec.Code)
	}
	// Signed out.
	if rec := fetch(t, srv, path); rec.Code == http.StatusOK {
		t.Error("the form is served signed out")
	}

	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"unchanged", url.Values{"score": {"3"}, "hard_mode": {"1"}}},
		{"out of range", url.Values{"score": {"7"}}},
		{"reason too long", url.Values{"score": {"4"}, "reason": {strings.Repeat("a", store.MaxCorrectionReason+1)}}},
	} {
		if rec := postSettings(t, srv, path, tc.form, session); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", tc.name, rec.Code)
		}
	}
	got, _ := store.ResultFor(ctx, srv.db, n, player.ID)
	if *got.Guesses != 3 || got.EnteredBy != nil {
		t.Errorf("a refused correction changed the result: %+v", got)
	}
}
