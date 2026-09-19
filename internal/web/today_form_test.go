package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

func TestTodayShowsThirtyDayFormWithBothRanks(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()
	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	current := currentPuzzle()
	for _, player := range []struct {
		name, slug  string
		old, recent int
	}{
		{"Sprinter", "sprinter", 6, 2},
		{"Steady", "steady", 3, 4},
		{"Level A", "level-a", 4, 4},
		{"Level B", "level-b", 4, 4},
		{"Sparse", "sparse", 5, 4},
	} {
		p, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), player.name, player.slug)
		if err != nil {
			t.Fatal(err)
		}
		if player.slug == "sparse" {
			for puzzle := current - 60; puzzle <= current-51; puzzle++ {
				seedResult(t, srv, p.ID, puzzle, 5, false)
			}
			seedResult(t, srv, p.ID, current-2, 4, false)
			continue
		}
		for puzzle := current - 39; puzzle <= current; puzzle++ {
			score := player.old
			if puzzle >= current-6 {
				score = player.recent
			}
			seedResult(t, srv, p.ID, puzzle, score, true)
		}
	}
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)
	session := signIn(t, srv, admin.ID)
	for _, surface := range []struct {
		path, board, prefix string
		cookie              *http.Cookie
	}{
		{"/today", "/leaderboard", "", session},
		{"/share/" + slug + "/", "/share/" + slug + "/board", "/share/" + slug, nil},
	} {
		body := fetchAs(t, srv, surface.path+"?form=7", surface.cookie).Body.String()
		at := strings.Index(body, `class="today-form"`)
		if at < 0 {
			t.Fatal("no Today form list")
		}
		pane := body[at:]
		if !strings.Contains(pane, "Form · last 30 days") || strings.Contains(pane, "7 days") || strings.Contains(pane, "form-periods") || !strings.Contains(pane, "form-spark") {
			t.Error("Today is not fixed to 30-day form with charts")
		}
		if !strings.Contains(pane, `class="form-avg num">3.23`) {
			t.Error("the form score does not use 30 days")
		}

		// One cell per row: the form rank, then the overall rank in
		// parentheses. Level B is tied at form rank 2 and keeps overall rank
		// 3 beside it; Sprinter's recent run puts it fourth on form while its
		// old sixes leave it last overall; Sparse has no form at all in the
		// window and is unranked on it while still ranked overall.
		rank := regexp.MustCompile(`(?s)<li class="form-row">.*?<summary>([^<]*?)\s*<span class="rank-overall">\(([^)]+)\)</span></summary>.*?<a class="player form-name" href="[^"]+">([^<]+)</a>`)
		got := map[string][2]string{}
		for _, m := range rank.FindAllStringSubmatch(pane, -1) {
			got[m[3]] = [2]string{m[1], m[2]}
		}
		for name, want := range map[string][2]string{
			"Level B":  {"2", "3"},
			"Sprinter": {"4", "5"},
			"Sparse":   {"—", "4"},
		} {
			if got[name] != want {
				t.Errorf("%s: got form rank %q, overall rank %q; want %q, %q",
					name, got[name][0], got[name][1], want[0], want[1])
			}
		}
		if len(got) != 5 {
			t.Errorf("got %d form rows, want one per player: %v", len(got), got)
		}

		// The popup is what says which number is which.
		popup := pane[strings.Index(pane, `class="rank-detail`):]
		popup = popup[:strings.Index(popup, "</dl>")]
		for _, label := range []string{"Form rank", "Overall rank"} {
			if !strings.Contains(popup, label) {
				t.Errorf("the rank popup does not spell out %q: %s", label, popup)
			}
		}

		body = fetchAs(t, srv, surface.path+"?lang=sv", surface.cookie).Body.String()
		for _, label := range []string{"Form · senaste 30 dagarna", "Formplacering", "Totalplacering", "Dagens resultat"} {
			if !strings.Contains(body, label) {
				t.Errorf("Swedish label missing: %s", label)
			}
		}
	}
}
