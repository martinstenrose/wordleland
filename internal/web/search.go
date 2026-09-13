package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/martinstenrose/wordleland/internal/store"
)

// searchHit is one result: a name to show, where it goes, and which icon
// marks it. Kind is one of "player", "page", "settings" or "admin" — see
// ui/icons.html's search-hit-icon, which is what actually reads it.
type searchHit struct {
	Kind  string
	Label string
	Href  string
}

// searchResults groups hits the way the page shows them. A page with only
// one kind of hit still gets a heading, so a result never looks unlabelled.
type searchResults struct {
	Players []searchHit
	Pages   []searchHit
}

// Empty reports whether nothing matched, so the template can show its
// no-results copy instead of two empty headings.
func (r searchResults) Empty() bool { return len(r.Players) == 0 && len(r.Pages) == 0 }

type searchPage struct {
	chrome

	Query   string
	Results searchResults
}

// searchResultCap bounds how many players a query can return. The group
// this runs for is small enough that it rarely matters, but an empty query
// matches every player — see search() — and the page should not become a
// second, worse copy of the roster.
const searchResultCap = 12

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

	page := searchPage{
		chrome:  s.newChrome(w, r, prefix, "", readOnly),
		Query:   query,
		Results: results,
	}

	if r.URL.Query().Get("partial") == "1" {
		s.renderBlock(w, r, http.StatusOK, "search.html", "search-results", page)
		return
	}
	s.render(w, r, http.StatusOK, "search.html", page)
}

// search matches players by name or slug, and the app's own destinations by
// their label in the reader's own language — so a Swedish reader searching
// "månader" finds Months the same way an English reader finds it by typing
// "month". An empty query matches everything, which is what both the bare
// /search page and the overlay's opening state want: a query nobody has
// typed into yet should not look broken, and a full list of destinations
// is a reasonable place to start browsing from.
func (s *Server) search(ctx context.Context, query, prefix string, readOnly, isAdmin bool, t translator) (searchResults, error) {
	needle := strings.ToLower(strings.TrimSpace(query))

	players, err := store.ListPlayers(ctx, s.db)
	if err != nil {
		return searchResults{}, err
	}

	var results searchResults
	for _, p := range players {
		if len(results.Players) >= searchResultCap {
			break
		}
		if strings.Contains(strings.ToLower(p.Name), needle) || strings.Contains(strings.ToLower(p.Slug), needle) {
			results.Players = append(results.Players, searchHit{Kind: "player", Label: p.Name, Href: prefix + "/p/" + p.Slug})
		}
	}

	for _, d := range s.searchDestinations(prefix, readOnly, isAdmin, t) {
		if strings.Contains(strings.ToLower(d.Label), needle) {
			results.Pages = append(results.Pages, d)
		}
	}

	return results, nil
}

// searchDestinations lists the pages a search can find: every nav view,
// always; the reader's own settings and — for an admin — the admin area's
// four screens, neither of which exist on the read-only share view. Built
// fresh per request rather than once at startup, because the labels are
// localized and an admin's list is longer than everyone else's.
func (s *Server) searchDestinations(prefix string, readOnly, isAdmin bool, t translator) []searchHit {
	destinations := make([]searchHit, 0, len(navViews)+5)
	for _, v := range navViews {
		destinations = append(destinations, searchHit{
			Kind:  "page",
			Label: t.T("nav.view." + v),
			Href:  viewPath(prefix, v),
		})
	}
	if readOnly {
		return destinations
	}

	destinations = append(destinations, searchHit{Kind: "settings", Label: t.T("nav.settings"), Href: "/settings"})
	if isAdmin {
		destinations = append(destinations,
			searchHit{Kind: "admin", Label: t.T("admin.players.title"), Href: "/admin/players"},
			searchHit{Kind: "admin", Label: t.T("pending.title"), Href: "/admin/pending"},
			searchHit{Kind: "admin", Label: t.T("activity.title"), Href: "/admin/activity"},
			searchHit{Kind: "admin", Label: t.T("diag.title"), Href: "/admin/diagnostics"},
		)
	}
	return destinations
}
