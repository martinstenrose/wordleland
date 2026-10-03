package web

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Every badge has a grid to draw beside it, and a grid badge's example
// earns the badge it stands for: an example that does not is a picture of
// the wrong thing.
func TestEveryBadgeHasAnExampleThatEarnsIt(t *testing.T) {
	t.Parallel()
	for _, b := range stats.Badges {
		example, ok := badgeExamples[b]
		if !ok {
			t.Errorf("%s has no example grid", b)
			continue
		}
		if slices.Contains(wordle.GridBadges, b) && !slices.Contains(example.Badges(), b) {
			t.Errorf("%s's example %q earns %v instead", b, example, example.Badges())
		}
	}
}

// seedGrid files a solved result with its squares, the score read off them.
func seedGrid(t *testing.T, srv *Server, slug string, puzzle int, grid string) {
	t.Helper()
	ctx := context.Background()
	p, err := store.PlayerBySlug(ctx, srv.db, slug)
	if err != nil {
		t.Fatalf("PlayerBySlug(%s): %v", slug, err)
	}
	date, err := wordle.DateForPuzzle(puzzle)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}
	n := strings.Count(grid, "/") + 1
	if _, _, err := store.UpsertResult(ctx, srv.db, store.Result{
		PuzzleNo: puzzle, Date: date, PlayerID: p.ID, Guesses: &n, Solved: true, Grid: grid,
	}, nil, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}
}

// A day's page names what each grid earned beside it — its own shape and
// what it shares with another's that day — and says why on a tap.
func TestPuzzlePageShowsWhatEachGridEarned(t *testing.T) {
	t.Parallel()
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	puzzle := currentPuzzle() - 40
	seedGrid(t, srv, "harda", puzzle, "gynnn/gnynn/gnnyn/ggggg")
	seedGrid(t, srv, "hardb", puzzle, "gynnn/gnynn/gnnyn/ggggg")
	seedGrid(t, srv, "normala", puzzle, "nnnnn/ggggg")

	page := fetch(t, srv, fmt.Sprintf("/share/%s/puzzle/%d", slug, puzzle)).Body.String()
	for _, want := range []string{
		`>Staircase</summary>`,
		`>Twins</summary>`,
		`>From nothing</summary>`,
		"Somebody else posted the very same grid that day.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the puzzle page does not show %q", want)
		}
	}
	if got := strings.Count(page, `>Twins</summary>`); got != 2 {
		t.Errorf("Twins shown %d times, want once for each of the pair", got)
	}
}

// A player's page lists every badge, the earned ones with how often and
// the day they last were, and how many first rows the player has opened
// with.
func TestPlayerPageListsBadges(t *testing.T) {
	t.Parallel()
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	first, last := currentPuzzle()-3, currentPuzzle()-2
	seedGrid(t, srv, "harda", first, "nnnnn/ggggg")
	seedGrid(t, srv, "harda", last, "nnnnn/ggggg")

	page := fetch(t, srv, "/share/"+slug+"/players/harda").Body.String()
	list, ok := sectionOf(page, `<ul class="badge-list">`, "</ul>")
	if !ok {
		t.Fatal("the badge list is missing")
	}
	if got := strings.Count(list, `<li class="badge-item`); got != len(stats.Badges) {
		t.Errorf("%d badges listed, want every one of %d", got, len(stats.Badges))
	}
	from, ok := sectionOf(list, `<span class="badge-name">From nothing</span>`, "</li>")
	if !ok {
		t.Fatal("From nothing is not listed")
	}
	if !strings.Contains(from, ">×2<") || !strings.Contains(from, fmt.Sprintf(`href="/share/%s/puzzle/%d">last #%d<`, slug, last, last)) {
		t.Errorf("From nothing does not say twice, last on #%d: %s", last, from)
	}
	if !strings.Contains(list, `<li class="badge-item unearned">`) {
		t.Error("an unearned badge is not marked as one")
	}
	if !strings.Contains(page, "1 of 243 possible first rows") {
		t.Error("the openings figure is missing")
	}

	none := fetch(t, srv, "/share/"+slug+"/players/hardb").Body.String()
	if !strings.Contains(none, "No grids recorded for this player yet.") {
		t.Error("a player without grids is not told why nothing is earned")
	}
}
