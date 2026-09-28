package web

import (
	"fmt"
	"html/template"
)

// symbol draws one Material Symbols Outlined glyph as inline SVG, size
// pixels square.
//
// These are the design system's icons, and it pulls them as a font from
// Google Fonts. They are drawn from path data here instead, for the reason
// Manrope is served from this app: a page that reaches a third party to
// finish rendering is one this app does not control, and one more party
// watching whoever reads the board. A glyph also costs nothing until a page
// uses it, where the font is megabytes for the handful of these this app
// draws. The licence is Apache 2.0; its text travels in
// static/material-symbols-LICENSE.txt.
//
// A glyph with a filled form carries both paths, and app.css shows the
// filled one on whatever is current — a page in the bar, a section in the
// pill row — the way the design fills the icon of the page you are on. The
// template does not have to know which state it is drawing.
//
// An unknown name is a programming error the template tests would miss, so
// it draws nothing rather than a broken box; TestEverySymbolExists holds the
// names the templates ask for against the map.
func symbol(name string, size int) template.HTML {
	paths, ok := symbolPaths[name]
	if !ok {
		return ""
	}
	out := fmt.Sprintf(`<svg class="sym" width="%d" height="%d" viewBox="0 -960 960 960" aria-hidden="true">`, size, size)
	if paths[1] == "" {
		out += fmt.Sprintf(`<path d="%s"/>`, paths[0])
	} else {
		out += fmt.Sprintf(`<path class="sym-line" d="%s"/><path class="sym-fill" d="%s"/>`, paths[0], paths[1])
	}
	return template.HTML(out + `</svg>`)
}

// The glyph for each place a code rather than a glyph name is what the
// template has to hand: a view, a page, a kind of search hit, a theme. One
// table, so the icon a view has in the bar is the one it has in the search
// results. The admin area's sections are drawn from sectionSymbols instead:
// their codes overlap these ("settings", "players") and mean something else.
var symbolFor = map[string]string{
	// Views, and the admin area beside them.
	"today":   "today",
	"board":   "leaderboard",
	"months":  "calendar_month",
	"grid":    "grid_view",
	"players": "group",
	"admin":   "admin_panel_settings",

	// Pages in the shell that are not views.
	"settings": "settings",
	"search":   "search",
	"privacy":  "lock",
	"puzzle":   "tag",

	// Search hits.
	"player": "person",
	"page":   "description",

	// Themes.
	"light":  "light_mode",
	"system": "brightness_auto",
	"dark":   "dark_mode",
}

// sectionSymbols is the admin area's own, keyed by adminCodeFor.
var sectionSymbols = map[string]string{
	"settings":    "tune",
	"players":     "group",
	"pending":     "inbox",
	"activity":    "history",
	"diagnostics": "monitor_heart",
}

// calloutSymbols is the glyph on each kind of callout on Today, keyed by
// stats.Callout's Kind.
var calloutSymbols = map[string]string{
	"unbroken":    "local_fire_department",
	"oneAndDone":  "target",
	"onForm":      "trending_up",
	"offForm":     "trending_down",
	"missing":     "person_off",
	"quickSolves": "bolt",
	"closeShaves": "warning",
	"stumped":     "sentiment_stressed",
	"hardMode":    "fitness_center",
}

// codeSymbol draws the glyph for a view, a page, a hit or a theme.
func codeSymbol(code string, size int) template.HTML {
	return symbol(symbolFor[code], size)
}
