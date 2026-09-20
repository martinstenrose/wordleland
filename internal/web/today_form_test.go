package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// The sparkline showed the shape of a month and none of its scores: a 2 and
// an X looked like a dip and a spike. Five chips read the same way and are
// legible, and they are the chip today's own table draws the score with, so
// the two lists share one vocabulary. Newest on the right, a day not played
// a gap rather than a score.
func TestTodaysFormSpellsOutTheLastFiveInTheDaysOwnChip(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()
	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	current := currentPuzzle()
	p, err := store.CreatePlayer(ctx, srv.db, store.AdminActor(admin.ID), "Fiver", "fiver")
	if err != nil {
		t.Fatal(err)
	}
	for puzzle := current - 30; puzzle < current-4; puzzle++ {
		seedResult(t, srv, p.ID, puzzle, 4, false)
	}
	seedResult(t, srv, p.ID, current-4, 2, false)
	// current-3 not played.
	seedResult(t, srv, p.ID, current-2, 0, false)
	seedResult(t, srv, p.ID, current-1, 4, true)
	seedResult(t, srv, p.ID, current, 5, false)

	body := fetchAs(t, srv, "/today", signIn(t, srv, admin.ID)).Body.String()
	pane := body[strings.Index(body, `class="today-form"`):]
	pane = pane[:strings.Index(pane, "card-foot")]

	row := regexp.MustCompile(`(?s)<li class="form-row">.*?<span class="form-last-five">(.*?)</span>\s*<span class="form-avg`).FindStringSubmatch(pane)
	if row == nil {
		t.Fatal("the form row has no last-five cell between the name and the average")
	}
	chip := regexp.MustCompile(`<span class="cell (?:t\d tiny">\s*<details class="cell-pop" name="popup">\s*<summary>([^<]*)</summary>|(gap) tiny">)`)
	var got []string
	for _, m := range chip.FindAllStringSubmatch(row[1], -1) {
		if m[2] != "" {
			got = append(got, "-")
			continue
		}
		got = append(got, m[1])
	}
	if want := []string{"2", "-", "X", "4*", "5"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the last five read %v, want %v", got, want)
	}
	if strings.Contains(pane, "<svg") {
		t.Error("the form list still draws a sparkline")
	}

	// Today's score is the same chip, and the average sits before the delta
	// it explains, as it does in the form list.
	if !strings.Contains(body, `<span class="cell t5 tiny result-score">5</span>`) {
		t.Error("today's score is not drawn by the same chip as the last five")
	}
	if !regexp.MustCompile(`<span class="result-avg num">\d\.\d\d</span>\s*<span class="result-delta delta worse">▲ `).MatchString(body) {
		t.Error("the results row does not print the average before the delta it explains")
	}
}

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
		if !strings.Contains(pane, "Form · last 30 days") || strings.Contains(pane, "7 days") || strings.Contains(pane, "form-periods") || !strings.Contains(pane, "form-last-five") {
			t.Error("Today is not fixed to 30-day form with the last five beside it")
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
