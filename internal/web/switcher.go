package web

import (
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// switcher is a card's section bar, in which the heading is the control that
// changes it.
//
// It replaces a strip of tabs above a title. The strip and the title said the
// same word twice — one of them highlighted, the other as a heading — and the
// strip was the half that did not fit: five admin sections wrapped to a second
// row on a phone, and a roster of fourteen names scrolled sideways. A heading
// is one line at every width whatever is behind it.
//
// Nothing here needs a script. It renders as a <details> in the shared
// "menu-group", so it opens, closes on a second press, and closes when
// another menu opens, all from the markup — and every row is a link to the
// page it names.
type switcher struct {
	// Label is the current section, and the page's heading.
	Label string
	// Hint is the line under it: what this section is, or who this player
	// is. Blank where a section has nothing worth saying twice.
	Hint string
	// Badge is a count worth seeing before the menu is opened — senders
	// waiting to be claimed, so far.
	Badge string

	// The neighbours on either side, for the step arrows: one press to the
	// section or the player next door, without opening the list to find
	// them. Both wrap, so neither is ever absent while there is more than
	// one to step between — the ends of a list of five sections or fourteen
	// names are not a boundary anybody is trying to respect.
	//
	// Empty on a page with nothing selected, where there is no "next".
	PrevHref, PrevLabel string
	NextHref, NextLabel string

	Items []switcherItem
}

// switcherItem is one row of the open menu.
//
// The two kinds of row carry different things — a section has an icon and
// sometimes a count, a player has a rank and an average — so both sets of
// fields live here and the template draws whichever are filled. One row type
// with two shapes rather than two row types: they are the same control, and
// the alternative is two partials that drift.
type switcherItem struct {
	Label string
	Href  string
	On    bool

	// Section rows.
	Code  string
	Badge string

	// Player rows.
	Rank string
	Avg  string
}

// Open reports whether the menu should be rendered open.
//
// It is not: a section bar arrives closed, because the reader asked for the
// page rather than for the list of pages. The method exists so the template
// has one place to change if that is ever wrong.
func (s switcher) Open() bool { return false }

// adminSwitcher builds the admin area's section bar from the same list the
// strip was built from, so there is still one place that knows what the admin
// area contains.
func (c chrome) adminSwitcher() switcher {
	tabs := c.AdminTabs()
	out := switcher{Items: make([]switcherItem, 0, len(tabs))}
	for _, tab := range tabs {
		code := adminCodeFor(tab.Href)
		out.Items = append(out.Items, switcherItem{
			Label: tab.Label,
			Href:  tab.Href,
			On:    tab.On,
			Code:  code,
		})
		if tab.On {
			out.Label = tab.Label
			out.Hint = c.T.T("admin." + code + ".hint")
		}
	}
	out.step(c.T)
	return out
}

// step fills in the neighbours on either side of whichever item is current.
//
// It wraps rather than stopping at the ends, which is why neither arrow is
// ever the disabled control a first or last item would otherwise need. With
// nothing selected there is no next: the arrows are left off entirely.
func (s *switcher) step(t translator) {
	at := -1
	for i, item := range s.Items {
		if item.On {
			at = i
			break
		}
	}
	if at < 0 || len(s.Items) < 2 {
		return
	}
	prev := s.Items[(at-1+len(s.Items))%len(s.Items)]
	next := s.Items[(at+1)%len(s.Items)]
	s.PrevHref, s.PrevLabel = prev.Href, t.T("switcher.previous", prev.Label)
	s.NextHref, s.NextLabel = next.Href, t.T("switcher.next", next.Label)
}

// adminCodeFor names a section for the icon dispatch. The href is the one
// thing AdminTabs already carries that identifies a section, and deriving the
// code from it keeps the two lists from needing to agree about a second one.
func adminCodeFor(href string) string {
	switch href {
	case "/admin/settings":
		return "settings"
	case "/admin/players":
		return "players"
	case "/admin/pending":
		return "pending"
	case "/admin/activity":
		return "activity"
	default:
		return "diagnostics"
	}
}

// playerSwitcher builds the roster bar: the player you are reading, and every
// other one a press away.
//
// This is the harder of the two cases the design set out — fourteen names,
// no icons, and lengths that do not line up — and it is why the control
// carries a rank and an average on each row. A list of bare names in an
// arbitrary order is a list you have to read; with the board's own order and
// its figures beside them, it is the leaderboard in miniature, and you can
// aim at a row before reading it.
//
// on is the player being shown, or nil on the roster page where nobody has
// been chosen yet.
func (c chrome) playerSwitcher(board stats.Board, prefix string, on *stats.Player) switcher {
	out := switcher{Label: c.T.T("nav.view.players")}
	if on != nil {
		out.Label = on.Name
		if on.Ranked() {
			out.Hint = c.T.T("player.rank", on.Rank)
		} else {
			out.Hint = c.T.T("player.notRanked")
		}
		if on.LastPlayed != nil {
			out.Hint += " · " + c.T.T("player.lastPlayed") + " " + on.LastPlayed.Format(time.DateOnly)
		}
	}

	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			// Both figures are withheld below the ranking threshold, for
			// the same reason the board and the player page withhold them:
			// an average over three puzzles is a number, not a measurement,
			// and this menu would be the one place it slipped out.
			row := switcherItem{
				Label: p.Name,
				Href:  prefix + "/players/" + p.Slug,
				Rank:  "—",
				Avg:   "—",
			}
			if p.Ranked() {
				row.Rank, row.Avg = c.T.Integer(p.Rank), formatScore(c.T, p.Average)
			}
			if on != nil && p.ID == on.ID {
				row.On = true
			}
			out.Items = append(out.Items, row)
		}
	}
	out.step(c.T)
	return out
}
