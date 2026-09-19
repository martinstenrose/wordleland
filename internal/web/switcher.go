package web

import (
	"strings"
	"time"
	"unicode"

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

	// Code picks the glyph beside the label, and Initials the avatar in its
	// place. A section has an icon, a player has initials, and neither has
	// both.
	Code     string
	Initials string

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
			out.Label, out.Code = tab.Label, code
			out.Hint = c.T.T("admin." + code + ".hint")
		}
	}
	return out
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
	out := switcher{Label: c.T.T("nav.view.players"), Code: "players"}
	if on != nil {
		out.Label, out.Initials = on.Name, initialsOf(on.Name)
		// The avatar replaces the section glyph: a player is a who, not a
		// what, and two of them side by side would say neither.
		out.Code = ""
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
				Href:  prefix + "/p/" + p.Slug,
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
	if on == nil {
		out.Hint = c.T.T("players.count", len(out.Items))
	}
	return out
}

// initialsOf is the avatar's text: one letter, or two when the name has a
// second word to take one from.
//
// Names here are whatever the group typed — "Lars", "Anna-Karin", "Jo" — so
// this counts runes rather than bytes and gives up quietly on anything it
// cannot read, leaving an empty avatar rather than a broken one.
func initialsOf(name string) string {
	out := make([]rune, 0, 2)
	for _, word := range strings.Fields(name) {
		for _, r := range word {
			out = append(out, unicode.ToUpper(r))
			break
		}
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}
