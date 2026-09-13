package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

func TestTodayShowsCompactFormRanksChartsAndLeaderboardLastFive(t *testing.T) {
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
		at := strings.Index(body, `class="card today-form"`)
		if at < 0 {
			t.Fatal("no Today form table")
		}
		pane := body[at:]
		if !strings.Contains(pane, "Form · last 30 days") || strings.Contains(pane, "7 days") || strings.Contains(pane, "form-periods") || !strings.Contains(pane, "podium-spark") || !strings.Contains(pane, "spark-col") {
			t.Error("Today is not fixed to 30-day form with charts and Last Five")
		}
		if !strings.Contains(pane, `class="podium-figure">3.23`) {
			t.Error("the form score does not use 30 days")
		}
		if got := strings.Count(pane, `class="last-five"`); got != 5 {
			t.Errorf("got %d Last Five displays, want one for each player", got)
		}
		// Level B is tied at form rank 2, but its card keeps overall rank 3.
		levelB := strings.Index(pane, `>Level B</a>`)
		if levelB < 0 || !strings.Contains(pane[levelB:levelB+180], `title="Overall rank">#3</span>`) {
			t.Error("the top card does not retain overall rank")
		}
		headerAt := strings.Index(pane, "<thead>")
		header := pane[headerAt:]
		header = header[:strings.Index(header, "</tr>")]
		if !strings.Contains(header, `>#<`) || !strings.Contains(header, "Overall rank") {
			t.Error("the table should label its two rank columns \"#\" and \"Overall rank\"")
		}
		rowRanks := regexp.MustCompile(`(?s)<tr>\s*<td class="num" title="Form rank">([^<]+)</td>\s*<td[^>]*>[^<]*</td>\s*<td><a class="player" href="[^"]+">([^<]+)</a>`)
		matches := rowRanks.FindAllStringSubmatch(pane, -1)
		if len(matches) != 2 || matches[0][1] != "4" || matches[0][2] != "Sprinter" || matches[1][1] != "—" || matches[1][2] != "Sparse" {
			t.Errorf("wrong table form ranks: %v", matches)
		}
		board := fetchAs(t, srv, surface.board, surface.cookie).Body.String()
		for _, player := range []string{"steady", "sprinter", "sparse"} {
			lastFive := func(html string) string {
				t.Helper()
				at := strings.Index(html, `href="`+surface.prefix+"/p/"+player+`"`)
				if at < 0 {
					t.Fatalf("no link for %s", player)
				}
				html = html[at:]
				at = strings.Index(html, `<ol class="last-five">`)
				if at < 0 {
					t.Fatalf("no Last Five for %s", player)
				}
				html = html[at:]
				return strings.Join(strings.Fields(html[:strings.Index(html, "</ol>")+5]), " ")
			}
			if lastFive(pane) != lastFive(board) {
				t.Errorf("%s: Today differs from leaderboard Last Five", player)
			}
		}
		body = fetchAs(t, srv, surface.path+"?lang=sv", surface.cookie).Body.String()
		for _, label := range []string{"Form · senaste 30 dagarna", "Formplacering", "Totalplacering", "Senaste fem", "Senaste 30 dagarna"} {
			if !strings.Contains(body, label) {
				t.Errorf("Swedish label missing: %s", label)
			}
		}
	}
}
