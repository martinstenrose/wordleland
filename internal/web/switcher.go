package web

import (
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
)

// switcher is the head of a page with siblings — a player among the roster,
// a section among the admin area's — and the row of pills that moves between
// them: every sibling always in view, one press to any of them.
//
// It replaces a heading that opened a menu, which replaced a strip of tabs
// before that. The strip wrapped to a second row on a phone and the menu hid
// what could just be shown; a row that scrolls sideways does neither. Five
// sections and fourteen names both fit in one row, and on a phone the row
// swipes.
//
// Nothing here needs a script: every pill and both step arrows are links to
// the page they name. app.js only scrolls the row so the current pill is in
// view when the page arrives, which a row starting at its left end would
// otherwise leave to the reader.
type switcher struct {
	// NavLabel names the row for assistive tech: the players, the admin
	// area.
	NavLabel string
	// Eyebrow is the line over the title: where this player stands, or the
	// area a section belongs to.
	Eyebrow string
	// Label is the current sibling, and the page's heading.
	Label string
	// Sub is the line under it: what this page is, or what a section holds.
	Sub string
	// Stats are the figures a player's head carries beside the name.
	Stats []playerStat
	// Trait and Why are a player's trait chip and what earned it: beside
	// the title rather than inside it, since the chip is a <details>.
	Trait, Why string

	// The neighbours on either side, for the step arrows beside the row on
	// a wide screen: one press to the section or the player next door. Both
	// wrap, so neither is ever absent while there is more than one to step
	// between — the ends of a list of five sections or fourteen names are
	// not a boundary anybody is trying to respect.
	//
	// Empty on a page with nothing selected, where there is no "next".
	PrevHref, PrevLabel string
	NextHref, NextLabel string

	Items []switcherItem
}

// switcherItem is one pill.
//
// It can lead with a rank or a glyph and end with a score tile or a count:
// a player's pill carries their place on the board and their latest score, a
// section's its icon and, where there is one, how many things wait in it.
// One pill with optional ends rather than two kinds of pill: they are the
// same control, and two would drift.
type switcherItem struct {
	Label string
	Href  string
	On    bool

	// Section pills. Mark is a quiet all's-well beside a section's name —
	// the bridge connected, on Diagnostics — naming what it says.
	Icon  string
	Badge string
	Mark  string

	// Player pills. Tile is the latest result's label and Tone its colour,
	// empty when nothing was played in the last five days.
	Rank string
	Tile string
	Tone int
	// Top marks the leader, whose rank is drawn on the accent.
	Top bool
}

// adminSwitcher builds the admin area's pill row from the same list the
// strip was built from, so there is still one place that knows what the admin
// area contains.
func (c chrome) adminSwitcher() switcher {
	tabs := c.AdminTabs()
	out := switcher{NavLabel: c.T.T("nav.admin"), Eyebrow: c.T.T("nav.admin"), Items: make([]switcherItem, 0, len(tabs))}
	for _, tab := range tabs {
		code := adminCodeFor(tab.Href)
		out.Items = append(out.Items, switcherItem{
			Label: tab.Label,
			Href:  tab.Href,
			On:    tab.On,
			Icon:  sectionSymbols[code],
		})
		if tab.On {
			out.Label = tab.Label
			out.Sub = c.T.T("admin." + code + ".hint")
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

// adminCodeFor names a section for its glyph. The href is the one
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

// playerSwitcher builds the roster's row: the player you are reading, and
// every other one a press away.
//
// This is the harder of the two cases the design set out — fourteen names,
// no icons, and lengths that do not line up — and it is why each pill carries
// a rank and the latest score. A row of bare names in an arbitrary order is a
// row you have to read; in the board's own order, with each place and the
// last tile beside it, it is the leaderboard in miniature, and you can aim at
// a pill before reading it.
//
// on is the player being shown, or nil on the roster page where nobody has
// been chosen yet.
func (c chrome) playerSwitcher(board stats.Board, results []store.BoardResult, prefix string, on *stats.Player) switcher {
	out := switcher{NavLabel: c.T.T("nav.view.players"), Label: c.T.T("nav.view.players")}
	if on != nil {
		out.Label = on.Name
		out.Sub = c.T.T("player.sub")
		if on.Ranked() {
			out.Eyebrow = c.T.T("player.rank", on.Rank)
		} else {
			out.Eyebrow = c.T.T("player.notRanked")
		}
		if on.LastPlayed != nil {
			out.Eyebrow += " · " + lastPlayed(c.T, *on.LastPlayed, time.Now())
		}
	}

	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			// The rank is withheld below the ranking threshold, for the same
			// reason the board and the player page withhold it: a place
			// earned over three puzzles is a number, not a measurement, and
			// this row would be the one place it slipped out.
			row := switcherItem{
				Label: p.Name,
				Href:  prefix + "/players/" + p.Slug,
				Rank:  "—",
			}
			if p.Ranked() {
				row.Rank = c.T.Integer(p.Rank)
				row.Top = p.Rank == 1
			}
			// The latest result in the last five: today's once it is in, and
			// until then the one before it, so the row is not blank every
			// morning. Nothing for a player who has not played in five days —
			// a tile from last month would read as recent.
			cells := recentCells(p, results, board.CurrentPuzzle, c.T)
			for i := len(cells) - 1; i >= 0 && i >= len(cells)-5; i-- {
				if cells[i].Played {
					row.Tile, row.Tone = cells[i].Label, cells[i].Tone
					break
				}
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

// lastPlayed says when a player last played the way the design does — today,
// yesterday, or the day and month — so the eyebrow stays one line on a
// phone. The date is a puzzle's calendar day, compared as one.
func lastPlayed(t translator, date, now time.Time) string {
	day := func(at time.Time) time.Time { return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC) }
	now = now.In(time.Local)
	switch int(day(now).Sub(day(date)).Hours() / 24) {
	case 0:
		return t.T("player.lastPlayedToday")
	case 1:
		return t.T("player.lastPlayedYesterday")
	}
	when := strconv.Itoa(date.Day()) + " " + shortMonthName(t, date.Month())
	if date.Year() != now.Year() {
		when += " " + strconv.Itoa(date.Year())
	}
	return t.T("player.lastPlayedOn", when)
}
