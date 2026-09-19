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
		at := strings.Index(body, `class="today-form"`)
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
		// Level B is tied at form rank 2, but its card keeps overall rank 3
		// alongside it — the same "2 (3)" the table cells show, prefixed
		// with "#" since the card has no column header to carry it.
		levelB := strings.Index(pane, `>Level B</a>`)
		if levelB < 0 || !strings.Contains(pane[levelB:levelB+220], `<summary>#2 <span class="rank-overall">(3)</span></summary>`) {
			t.Error("the top card does not show form rank with overall rank alongside it")
		}
		headerAt := strings.Index(pane, "<thead>")
		header := pane[headerAt:]
		header = header[:strings.Index(header, "</tr>")]
		if !strings.Contains(header, `>#<`) || strings.Contains(header, ">Overall rank<") {
			t.Error("the table should carry one rank column, headed \"#\"")
		}
		// One cell per row now: the form rank, then the overall rank in
		// parentheses. Sprinter's recent run puts it fourth on form while
		// its old sixes leave it last overall, so the two numbers differ.
		rowRanks := regexp.MustCompile(`(?s)<tr>\s*<td class="num">.*?<summary>([^<]*?)\s*<span class="rank-overall">\(([^)]+)\)</span></summary>.*?<td><a class="player" href="[^"]+">([^<]+)</a>`)
		matches := rowRanks.FindAllStringSubmatch(pane, -1)
		want := [][3]string{{"4", "5", "Sprinter"}, {"—", "4", "Sparse"}}
		if len(matches) != len(want) {
			t.Fatalf("got %d rank cells, want %d: %v", len(matches), len(want), matches)
		}
		for i, w := range want {
			if matches[i][1] != w[0] || matches[i][2] != w[1] || matches[i][3] != w[2] {
				t.Errorf("row %d: got form rank %q, overall rank %q for %q; want %q, %q, %q",
					i, matches[i][1], matches[i][2], matches[i][3], w[0], w[1], w[2])
			}
		}
		// The popup is what says which number is which.
		popup := pane[strings.Index(pane, `class="rank-detail`):]
		popup = popup[:strings.Index(popup, "</dl>")]
		for _, label := range []string{"Form rank", "Overall rank"} {
			if !strings.Contains(popup, label) {
				t.Errorf("the rank popup does not spell out %q: %s", label, popup)
			}
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
