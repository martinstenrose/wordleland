package web

import (
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/martinstenrose/wordleland/internal/store"
)

// themeCookie remembers a light/dark/system choice.
const themeCookie = "wordleland_theme"

// The three theme settings. "system" is a real stored value rather than the
// absence of one, so that choosing it explicitly is distinguishable from
// never having chosen — and so the attribute on <html> always says which of
// the three is in force.
const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

func validTheme(v string) bool {
	return v == themeSystem || v == themeLight || v == themeDark
}

// chromeOpt is one option in a switcher: where it points, and whether it is
// the current one.
type chromeOpt struct {
	Code  string
	Label string
	Href  string
	On    bool
}

// chrome is the furniture every page shares: the translator, the theme, the
// two switchers, and who is signed in.
//
// It is embedded in each page's data rather than passed alongside it, so a
// template reaches it the same way it reaches anything else and the shared
// header partial needs no special handling.
type chrome struct {
	T translator

	// Lang and Theme land on <html>, which is what the stylesheet keys off.
	Lang  string
	Theme string

	// Themes and Languages are the rows in their menus, each knowing
	// whether it is the one in force.
	Themes    []chromeOpt
	Languages []chromeOpt

	// ThemeLabel and LangLabel name the current setting for the button
	// that opens each menu.
	ThemeLabel string
	LangLabel  string

	// Nav is the view switcher. Only views that exist appear: a tab that
	// leads nowhere is worse than an absent one.
	Nav []chromeOpt

	// Tabs is the same list for the narrow layout, with Today restored.
	Tabs []chromeOpt

	// Subtitle sits under the wordmark where the page has something to put
	// there — the design's "N days". Blank elsewhere rather than costing a
	// query on every page that has no board data to hand.
	Subtitle string

	// User is nil when nobody is signed in, which is also how a read-only
	// page suppresses the account menu.
	User      *store.User
	Initials  string
	CSRFToken string

	// ReadOnly hides everything that implies an account.
	ReadOnly bool

	// AdminTab marks which admin page is open, for the strip they share.
	AdminTab string

	// AdminWarning is a problem worth an admin's attention, shown across
	// the area rather than only on the page that computes it. A page nobody
	// opens is not a signal.
	AdminWarning string
}

// SignedIn reports whether the account menu should render.
func (c chrome) SignedIn() bool { return c.User != nil && !c.ReadOnly }

// IsAdmin reports whether the admin entries belong in the account menu.
func (c chrome) IsAdmin() bool { return c.SignedIn() && c.User.IsAdmin }

// newChrome resolves the locale and theme for this request and builds the
// switchers.
//
// Both switchers are links back to the current URL with one parameter
// changed, so they work with no JavaScript at all and a page reached through
// one keeps whatever the reader was already looking at.
func (s *Server) newChrome(w http.ResponseWriter, r *http.Request, prefix, view string, readOnly bool) chrome {
	t := s.translatorFor(w, r)
	c := chrome{
		T:        t,
		Lang:     t.locale,
		Theme:    s.themeFor(w, r),
		ReadOnly: readOnly,
	}

	// The views are built whatever page this is, with none marked current
	// when the page is not one of them. Settings and the admin area are
	// still inside the app, and dropping the pills there left the top bar
	// looking like a different site. Pages reached before a session exists
	// clear them again in signedOutChrome.
	{
		for _, v := range navViews {
			c.Nav = append(c.Nav, chromeOpt{
				Code:  v,
				Label: t.T("nav.view." + v),
				Href:  viewPath(prefix, v),
				On:    v == view,
			})
		}
		// The narrow layout scrolls the same pills rather than offering a
		// different set, so there is one list to keep correct.
		c.Tabs = c.Nav
	}

	// The wordmark's subtitle. Built here rather than by each page: five
	// pages set it and the rest did not, so it vanished on Settings and in
	// the admin area. The top bar owns everything the top bar shows.
	if days, err := store.CountPlayedPuzzles(r.Context(), s.db); err != nil {
		// Not worth failing a page over. The subtitle is decoration.
		s.logger.Error("count played puzzles", "error", err)
	} else if days > 0 {
		c.Subtitle = t.TN("chrome.days", days)
	}

	for _, code := range s.localeCodes {
		c.Languages = append(c.Languages, chromeOpt{
			Code:  code,
			Label: s.catalogues[code]["locale.name"],
			Href:  urlWith(r, "lang", code),
			On:    code == c.Lang,
		})
	}
	for _, theme := range []string{themeLight, themeDark, themeSystem} {
		c.Themes = append(c.Themes, chromeOpt{
			Code:  theme,
			Label: t.T("theme." + theme),
			Href:  urlWith(r, "theme", theme),
			On:    theme == c.Theme,
		})
	}
	c.ThemeLabel = t.T("theme.label") + ": " + t.T("theme."+c.Theme)
	c.LangLabel = t.T("lang.label") + ": " + s.catalogues[c.Lang]["locale.name"]

	if user, ok := authenticated(r); ok && !readOnly {
		c.User = &user
		c.Initials = initialsFor(user.Email)
	}
	return c
}

// themeFor resolves the theme the same way the locale is resolved: an
// explicit parameter wins and is remembered, then the cookie, then the
// default. The default is "system", so an untouched install follows the
// reader's own setting rather than imposing one.
func (s *Server) themeFor(w http.ResponseWriter, r *http.Request) string {
	if requested := r.URL.Query().Get("theme"); validTheme(requested) {
		http.SetCookie(w, &http.Cookie{
			Name:     themeCookie,
			Value:    requested,
			Path:     "/",
			HttpOnly: true,
			Secure:   s.secureCookies,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   365 * 24 * 60 * 60,
		})
		return requested
	}
	if c, err := r.Cookie(themeCookie); err == nil && validTheme(c.Value) {
		return c.Value
	}
	return themeSystem
}

// urlWith returns the current URL with one query parameter set.
//
// The rest of the query is carried through, so switching language on a
// filtered board does not also reset the filter.
func urlWith(r *http.Request, key, value string) string {
	q := r.URL.Query()
	q.Set(key, value)

	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	return path + "?" + q.Encode()
}

// initialsFor derives up to two letters for the account avatar.
//
// It reads the local part of the address, since that is the only name we
// reliably have: players.name exists but a user need not be linked to one.
func initialsFor(email string) string {
	local, _, _ := strings.Cut(email, "@")

	var out []rune
	for _, part := range strings.FieldsFunc(local, func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == '+'
	}) {
		for _, r := range part {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// signedOutChrome is the furniture for pages reached before a session
// exists — sign-in, two-factor, password reset. They still switch language
// and theme, and their home is the login page rather than a board.
func (s *Server) signedOutChrome(w http.ResponseWriter, r *http.Request, token string) chrome {
	c := s.newChrome(w, r, "", "", false)
	c.CSRFToken = token
	// No views: every one of them needs a session, so offering them here
	// would be offering a round trip back to this page. And no subtitle:
	// how much history exists is not for a visitor who has not signed in.
	c.Nav, c.Tabs = nil, nil
	c.Subtitle = ""
	// The account menu has nothing to show yet, and on the two-factor step
	// there is a session that is deliberately not yet an identity.
	c.User = nil
	return c
}

// The views the nav offers. The design lists a player index too, which is
// not built; linking to it would be linking to nothing.
const (
	viewToday   = "today"
	viewBoard   = "board"
	viewMonths  = "months"
	viewGrid    = "grid"
	viewPlayers = "players"
)

// Today is one of the views rather than something the wordmark stands in
// for. The wordmark used to be the way home, which meant the front page was
// reachable by a control that looked nothing like the others and was absent
// on a narrow screen, where a Today tab appeared instead. One list, shown
// the same way at both widths.
var navViews = []string{viewToday, viewBoard, viewMonths, viewGrid, viewPlayers}

// landingPath is where a signed-in reader arrives, and what the bare share
// URL shows. The front page rather than the leaderboard: the first thing
// anybody wants is today.
const landingPath = "/today"

// viewPath maps a view to its URL under a prefix. Today holds the bare
// share path for the same reason it is the landing: it is the front page.
func viewPath(prefix, view string) string {
	if prefix == "" {
		switch view {
		case viewBoard:
			return "/leaderboard"
		default:
			return "/" + view
		}
	}
	if view == viewToday {
		return prefix + "/"
	}
	return prefix + "/" + view
}

// adminChrome is the furniture for an admin page, marking which of them is
// open so the strip they share can say so.
func (s *Server) adminChrome(w http.ResponseWriter, r *http.Request, tab string) chrome {
	c := s.newChrome(w, r, "", "", false)
	c.AdminTab = tab

	// Losing the container-level "unhealthy" signal when the services
	// merged traded a warning that came to you for a page you have to open.
	// This closes that: whatever is wrong follows the admin around the area.
	// Deliberately not on the diagnostics page itself, which already says it
	// in full.
	if tab != "diagnostics" {
		if fresh, err := store.ReadFreshness(r.Context(), s.db); err == nil {
			c.AdminWarning = s.diagnosticsWarning(c.T, fresh, time.Now())
		} else {
			s.logger.Error("read freshness for the admin warning", "error", err)
		}
	}
	return c
}
