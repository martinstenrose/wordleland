package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// playerOrder reads the rendered board back as the order it displays.
func playerOrder(body string) []string {
	var out []string
	rest := body
	for {
		i := strings.Index(rest, `class="player" href="`)
		if i < 0 {
			return out
		}
		rest = rest[i+len(`class="player" href="`):]
		j := strings.Index(rest, `"`)
		href := rest[:j]
		out = append(out, href[strings.LastIndex(href, "/")+1:])
	}
}

func TestBoardSortsByEachColumn(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	base := "/share/" + slug + "/board"
	def := playerOrder(fetchAs(t, srv, base, nil).Body.String())
	if len(def) < 4 {
		t.Fatalf("only %d players on the board", len(def))
	}
	// The default is's own ordering: average ascending.
	if def[0] != "normalb" {
		t.Errorf("default order starts with %q, want the lowest average", def[0])
	}

	// Games descending puts the longest histories first. Every seeded
	// regular has the same count, so this checks the thin players sink.
	games := playerOrder(fetchAs(t, srv, base+"?sort=games", nil).Body.String())
	if games[len(games)-1] == def[len(def)-1] && games[0] == def[0] {
		t.Error("sorting by games produced the default order")
	}

	// Player name, ascending.
	byName := playerOrder(fetchAs(t, srv, base+"?sort=player", nil).Body.String())
	for i := 1; i < len(byName); i++ {
		// Only the ranked group is contiguous; stop at the first drop,
		// which is where the not-ranked group starts.
		if byName[i] < byName[i-1] {
			if i < 4 {
				t.Errorf("name sort is out of order at %d: %v", i, byName)
			}
			break
		}
	}
}

// Rank is's figure. Sorting by another column reorders rows; it must not
// renumber them, or the board would claim the streak leader is number one.
func TestSortingDoesNotRenumberRanks(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	ranks := func(path string) map[string]string {
		body := fetchAs(t, srv, path, nil).Body.String()
		out := map[string]string{}
		for _, slug := range playerOrder(body) {
			row := rowFor(t, body, slug)
			start := strings.Index(row, `<td class="num">`)
			if start < 0 {
				continue
			}
			cell := row[start+len(`<td class="num">`):]
			out[slug] = strings.TrimSpace(cell[:strings.Index(cell, "</td>")])
		}
		return out
	}

	base := "/share/" + slug + "/board"
	before := ranks(base)
	after := ranks(base + "?sort=streak")

	if len(before) == 0 {
		t.Fatal("no ranks read from the board")
	}
	for player, rank := range before {
		if got := after[player]; got != rank {
			t.Errorf("%s ranked %q by default but %q under a streak sort", player, rank, got)
		}
	}
}

// The not-ranked group stays below the divider whatever the sort, or a
// player with four games would climb above one with two hundred.
func TestSortingKeepsTheNotRankedGroupSeparate(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, path := range []string{"?sort=player", "?sort=games&dir=asc", "?sort=streak", "?sort=average&dir=desc"} {
		body := fetchAs(t, srv, "/share/"+slug+"/board"+path, nil).Body.String()
		divider := strings.Index(body, "Not ranked")
		if divider < 0 {
			t.Fatalf("%s: no not-ranked divider", path)
		}
		for _, thin := range []string{"thin", "lapsed"} {
			if strings.Index(body, "/p/"+thin) < divider {
				t.Errorf("%s: %s appears above the divider", path, thin)
			}
		}
		for _, ranked := range []string{"harda", "normalb"} {
			if strings.Index(body, "/p/"+ranked) > divider {
				t.Errorf("%s: %s fell below the divider", path, ranked)
			}
		}
	}
}

// A withheld figure is absent, not extreme: it must not float to the top of
// a descending sort and read as the worst average on the board.
func TestWithheldFiguresSortLastInBothDirections(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for _, dir := range []string{"asc", "desc"} {
		body := fetchAs(t, srv, "/share/"+slug+"/board?sort=form&dir="+dir, nil).Body.String()
		order := playerOrder(body)

		// The unranked players have no form; they are the tail either way.
		tail := order[len(order)-2:]
		for _, want := range []string{"thin", "lapsed"} {
			var found bool
			for _, got := range tail {
				if got == want {
					found = true
				}
			}
			if !found {
				t.Errorf("dir=%s: %s is not in the tail %v", dir, want, tail)
			}
		}
	}
}

// Clicking the active column flips it; the header says which way.
func TestHeaderLinksToggleDirection(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board?sort=average", nil).Body.String()
	if !strings.Contains(body, `aria-sort="ascending"`) {
		t.Error("the active column does not report its direction")
	}

	href := hrefFor(t, body, "Average")
	href = strings.ReplaceAll(href, "&amp;", "&")
	if !strings.Contains(href, "dir=desc") {
		t.Errorf("clicking the active column links to %q, want the flip", href)
	}

	flipped := fetchAs(t, srv, href, nil).Body.String()
	if !strings.Contains(flipped, `aria-sort="descending"`) {
		t.Error("following the flip did not change the direction")
	}
	if playerOrder(flipped)[0] == playerOrder(body)[0] {
		t.Error("flipping the direction did not change the order")
	}
}

// Sorting must not discard the filters, and vice versa.
func TestSortAndFiltersCoexist(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board?mode=hard&sort=games", nil).Body.String()
	if !strings.Contains(body, "players hidden") {
		t.Error("sorting dropped the hard-mode filter")
	}

	// And a control link carries the sort onward.
	href := hrefFor(t, body, "Count missed as 7")
	href = strings.ReplaceAll(href, "&amp;", "&")
	for _, want := range []string{"sort=games", "mode=hard", "missed=1"} {
		if !strings.Contains(href, want) {
			t.Errorf("the toggle link %q dropped %q", href, want)
		}
	}
}

// The default ordering leaves no sort parameters in the URL.
func TestDefaultSortHasACleanURL(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	body := fetchAs(t, srv, "/share/"+slug+"/board?sort=games", nil).Body.String()
	href := hrefFor(t, body, "#")
	if strings.Contains(href, "sort=") || strings.Contains(href, "dir=") {
		t.Errorf("the default column links to %q, want no sort parameters", href)
	}
}

func TestUnknownSortFallsBackToTheDefault(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	base := fetchAs(t, srv, "/share/"+slug+"/board", nil)
	junk := fetchAs(t, srv, "/share/"+slug+"/board?sort=nonsense&dir=sideways", nil)

	if junk.Code != http.StatusOK {
		t.Fatalf("junk sort = %d", junk.Code)
	}
	if a, b := playerOrder(base.Body.String()), playerOrder(junk.Body.String()); strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("an unrecognised sort changed the order: %v vs %v", a, b)
	}
}
