package web

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
)

// searchHit is one result: a name to show, where it goes, and what marks
// it — a player's result today as a tile, anything else a glyph in a
// rounded square. Meta is the quiet text at the row's end: a player's rank
// and average, "Page", or how long ago a puzzle was.
//
// Label is split into Before/Match/After around the query that found it,
// rather than carried whole, so the template can mark the matched part
// without re-running the search itself — see highlightLabel. Match is
// empty, and Before holds the whole label, when there is nothing to
// highlight: no query typed yet, or (a player found by slug rather than
// name) no match within the label actually shown.
type searchHit struct {
	Icon                 string
	Tile                 *scoreCell
	Label                string
	Before, Match, After string
	Meta                 string
	Href                 string
}

// highlightLabel splits label around the first case-insensitive occurrence
// of needle. Matching case-insensitively but slicing the original label is
// why Before/Match/After preserve "Harda"'s capital H rather than whatever
// case the reader typed.
func highlightLabel(label, needle string) (before, match, after string) {
	if needle == "" {
		return label, "", ""
	}
	i := strings.Index(strings.ToLower(label), strings.ToLower(needle))
	if i < 0 {
		return label, "", ""
	}
	return label[:i], label[i : i+len(needle)], label[i+len(needle):]
}

// searchResults groups hits the way the design shows them: players, pages,
// puzzles. A group with nothing in it is left out.
type searchResults struct {
	Players []searchHit
	Pages   []searchHit
	Puzzles []searchHit
	// Example is today's puzzle number, for the no-results copy's "try a
	// puzzle number like …".
	Example string
}

// Empty reports whether nothing matched, so the template can show its
// no-results copy instead of empty headings.
func (r searchResults) Empty() bool {
	return len(r.Players) == 0 && len(r.Pages) == 0 && len(r.Puzzles) == 0
}

type searchPage struct {
	chrome

	Query   string
	Results searchResults
}

// A query caps each group at searchResultCap; the empty query, which is
// what the overlay opens on, shows the first searchPreview of each — a
// start to browse from, not the roster again.
const (
	searchResultCap = 6
	searchPreview   = 3
)

// puzzleQuery reads a puzzle number from a query: "1926" or "#1926".
var puzzleQuery = regexp.MustCompile(`^#?(\d{1,5})$`)

// handleSearchPage is the authenticated route; handleShare's "search" case
// is the read-only one. Both go through handleSearch.
func (s *Server) handleSearchPage(w http.ResponseWriter, r *http.Request) {
	s.handleSearch(w, r, "", false)
}

// handleSearch serves both the search page and the command palette's
// overlay: the same handler, the same query, the same results, at two
// depths. "?partial=1" renders just the results — see renderBlock — so the
// overlay in app.js is never a second implementation of what matches a
// query, only a different amount of page around it.
//
// prefix and readOnly carry it onto the share view the same way every other
// shared page works (see handleToday, handleBoard, ...): readOnly drops
// Settings and the admin screens from what a query can find, and a hit's
// Href is built under the share prefix so a shared search never sends an
// anonymous reader into authenticated routing.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, prefix string, readOnly bool) {
	user, _ := authenticated(r)
	isAdmin := !readOnly && user.IsAdmin
	query := r.URL.Query().Get("q")

	results, err := s.search(r.Context(), query, prefix, readOnly, isAdmin, s.translatorFor(w, r))
	if err != nil {
		s.logger.Error("search", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	ch := s.newChrome(w, r, prefix, "", readOnly)
	ch.Page = chromeOpt{Code: "search", Label: ch.T.T("search.title"), On: true}
	page := searchPage{
		chrome:  ch,
		Query:   query,
		Results: results,
	}

	if wantsPartial(r) {
		s.renderBlock(w, r, http.StatusOK, "search.html", "search-results", page)
		return
	}
	s.render(w, r, http.StatusOK, "search.html", page)
}

// search matches players by name or slug, the app's own destinations by
// their label in the reader's own language — so a Swedish reader searching
// "månader" finds Months the same way an English reader finds it by typing
// "month" — and a puzzle by its number. An empty query shows the top of
// each group, which is what both the bare /search page and the overlay's
// opening state want: a query nobody has typed into yet should not look
// broken.
func (s *Server) search(ctx context.Context, query, prefix string, readOnly, isAdmin bool, t translator) (searchResults, error) {
	needle := strings.ToLower(strings.TrimSpace(query))
	limit := searchResultCap
	if needle == "" {
		limit = searchPreview
	}

	players, err := store.ListPlayers(ctx, s.db)
	if err != nil {
		return searchResults{}, err
	}
	results, err := store.ResultsForBoard(ctx, s.db)
	if err != nil {
		return searchResults{}, err
	}
	board := stats.Compute(players, results, stats.Options{Now: time.Now()})

	found := searchResults{Example: t.Puzzle(board.CurrentPuzzle)}
	// Ranked first, in rank order, as the board has them; then the rest.
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			if len(found.Players) >= limit {
				break
			}
			if !strings.Contains(strings.ToLower(p.Name), needle) && !strings.Contains(strings.ToLower(p.Slug), needle) {
				continue
			}
			tile := recentCells(p, results, board.CurrentPuzzle, t)
			hit := searchHit{Tile: &tile[len(tile)-1], Label: p.Name, Href: prefix + "/players/" + p.Slug, Meta: t.TN("player.games", p.Games)}
			if p.Ranked() && p.Average != nil {
				hit.Meta = "#" + t.Integer(p.Rank) + " · " + t.Decimal(*p.Average, 2)
			}
			hit.Before, hit.Match, hit.After = highlightLabel(hit.Label, needle)
			found.Players = append(found.Players, hit)
		}
	}

	for _, d := range s.searchDestinations(prefix, readOnly, isAdmin, t) {
		if len(found.Pages) >= limit {
			break
		}
		if !strings.Contains(strings.ToLower(d.Label), needle) {
			continue
		}
		d.Meta = t.T("search.page")
		d.Before, d.Match, d.After = highlightLabel(d.Label, needle)
		found.Pages = append(found.Pages, d)
	}

	// A number is a puzzle, when it is one that has been played; nothing
	// typed yet offers today's.
	n := board.CurrentPuzzle
	if m := puzzleQuery.FindStringSubmatch(needle); m != nil {
		n, _ = strconv.Atoi(m[1])
	} else if needle != "" {
		n = 0
	}
	if n >= 1 && n <= board.CurrentPuzzle {
		hit := searchHit{Icon: "tag", Label: t.T("search.puzzle", t.Puzzle(n)), Href: puzzlePath(prefix, n), Meta: t.T("nav.view.today")}
		if ago := board.CurrentPuzzle - n; ago > 0 {
			hit.Meta = t.TN("search.daysAgo", ago)
		}
		hit.Before = hit.Label
		found.Puzzles = append(found.Puzzles, hit)
	}

	return found, nil
}

// searchDestinations lists the pages a search can find: every nav view,
// always; the reader's own settings and — for an admin — the admin area's
// screens, neither of which exist on the read-only share view. Built fresh
// per request rather than once at startup, because the labels are
// localized and an admin's list is longer than everyone else's.
func (s *Server) searchDestinations(prefix string, readOnly, isAdmin bool, t translator) []searchHit {
	destinations := make([]searchHit, 0, len(navViews)+6)
	for _, v := range navViews {
		destinations = append(destinations, searchHit{
			Icon:  symbolFor[v],
			Label: t.T("nav.view." + v),
			Href:  viewPath(prefix, v),
		})
	}
	if readOnly {
		return destinations
	}

	destinations = append(destinations, searchHit{Icon: "settings", Label: t.T("nav.settings"), Href: "/settings"})
	if isAdmin {
		destinations = append(destinations, searchHit{Icon: symbolFor["admin"], Label: t.T("nav.admin"), Href: "/admin"})
		for _, a := range []struct{ code, key string }{
			{"players", "admin.players.title"},
			{"pending", "pending.title"},
			{"activity", "activity.title"},
			{"diagnostics", "diag.title"},
		} {
			destinations = append(destinations, searchHit{Icon: sectionSymbols[a.code], Label: t.T(a.key), Href: "/admin/" + a.code})
		}
	}
	return destinations
}
