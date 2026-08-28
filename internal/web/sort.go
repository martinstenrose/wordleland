package web

import (
	"net/http"
	"sort"
	"strings"
)

// The sortable columns. Rank is the default and means the board's own ordering.
const (
	sortRank    = "rank"
	sortPlayer  = "player"
	sortAverage = "average"
	sortForm    = "form"
	sortStreak  = "streak"
	sortGames   = "games"
)

// sortColumns is the display order, which is also the column order in the
// table so the header links line up with what they sort.
var sortColumns = []string{sortRank, sortPlayer, sortAverage, sortForm, sortStreak, sortGames}

// descendingByDefault marks the columns where "best first" means largest
// first. Average and form are scores, where lower is better; streaks and
// game counts are tallies, where more is. Clicking a column should show the
// interesting end first rather than always starting at the bottom.
var descendingByDefault = map[string]bool{
	sortStreak: true,
	sortGames:  true,
}

func validSort(v string) bool {
	for _, c := range sortColumns {
		if c == v {
			return true
		}
	}
	return false
}

// boardSort is the requested ordering.
type boardSort struct {
	Column string
	Desc   bool
}

// IsDefault reports whether this is's own ordering, which is what keeps
// it out of the URL.
func (s boardSort) IsDefault() bool {
	return s.Column == sortRank && !s.Desc
}

func parseBoardSort(r *http.Request) boardSort {
	s := boardSort{Column: sortRank}
	if v := r.URL.Query().Get("sort"); validSort(v) {
		s.Column = v
		s.Desc = descendingByDefault[v]
	}
	switch r.URL.Query().Get("dir") {
	case "asc":
		s.Desc = false
	case "desc":
		s.Desc = true
	}
	return s
}

// sortHeader is one column header, with where it points and how it is
// currently sorted.
type sortHeader struct {
	Column string
	Label  string
	Href   string
	// Active is set on the column actually in force; Desc says which way.
	Active bool
	Desc   bool
	// Aria is the value for aria-sort: "ascending", "descending" or "none".
	Aria string
}

// headersFor builds the header links.
//
// Clicking the active column flips its direction; clicking another starts
// at that column's own natural end.
func (s *Server) headersFor(r *http.Request, current boardSort, t translator) []sortHeader {
	labels := map[string]string{
		sortRank:    "board.column.rank",
		sortPlayer:  "board.column.player",
		sortAverage: "board.column.average",
		sortForm:    "board.column.form",
		sortStreak:  "board.column.streak",
		sortGames:   "board.column.games",
	}

	headers := make([]sortHeader, 0, len(sortColumns))
	for _, col := range sortColumns {
		h := sortHeader{
			Column: col,
			Label:  t.T(labels[col]),
			Active: col == current.Column,
			Aria:   "none",
		}

		next := boardSort{Column: col, Desc: descendingByDefault[col]}
		if h.Active {
			h.Desc = current.Desc
			next.Desc = !current.Desc
			if current.Desc {
				h.Aria = "descending"
			} else {
				h.Aria = "ascending"
			}
		}
		h.Href = sortHref(r, next)
		headers = append(headers, h)
	}
	return headers
}

// sortHref builds the URL for one ordering, leaving the default out so a
// plain board keeps a clean address.
func sortHref(r *http.Request, next boardSort) string {
	q := r.URL.Query()
	if next.IsDefault() {
		q.Del("sort")
		q.Del("dir")
	} else {
		q.Set("sort", next.Column)
		if next.Desc {
			q.Set("dir", "desc")
		} else {
			q.Set("dir", "asc")
		}
	}

	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if encoded := q.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

// sortRows orders one group of rows for display.
//
// It reorders rows only. Rank is's own figure and is never recomputed
// here: sorting by streak shows the same players with the same ranks in a
// different order, rather than inventing a leaderboard where the streak
// leader is number one.
//
// Ranked and unranked players are sorted as separate groups by the caller,
// so the divider between them survives every ordering. Mixing them would
// put a player with three games above one with two hundred whenever the
// sort happened to favour them.
func sortRows(rows []boardRow, s boardSort) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]

		// A withheld figure is absent, not extreme. It sorts last in both
		// directions, so a descending sort does not read it as the worst
		// average on the board.
		if av, bv, ok := optionalPair(a, b, s.Column); ok {
			if (av == nil) != (bv == nil) {
				return bv == nil
			}
		}

		c := compareRows(a, b, s.Column)
		if c == 0 {
			return a.Slug < b.Slug
		}
		if s.Desc {
			return c > 0
		}
		return c < 0
	})
}

// optionalPair returns the two values for a column that can be withheld.
func optionalPair(a, b boardRow, column string) (*float64, *float64, bool) {
	switch column {
	case sortAverage:
		return a.Average, b.Average, true
	case sortForm:
		return a.Form, b.Form, true
	}
	return nil, nil, false
}

// compareRows orders two rows on one column, ascending.
func compareRows(a, b boardRow, column string) int {
	switch column {
	case sortPlayer:
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	case sortAverage:
		return compareOptional(a.Average, b.Average)
	case sortForm:
		return compareOptional(a.Form, b.Form)
	case sortStreak:
		return compareInt(a.CurrentStreak, b.CurrentStreak)
	case sortGames:
		return compareInt(a.Games, b.Games)
	default:
		return compareInt(a.Rank, b.Rank)
	}
}

func compareOptional(a, b *float64) int {
	if a == nil || b == nil {
		return 0
	}
	switch {
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	default:
		return 0
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
