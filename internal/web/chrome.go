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

// sidebarCookie remembers whether the rail is collapsed to its icons.
const sidebarCookie = "wordleland_sidebar"

// The two rail widths. Both are stored explicitly, for the reason the theme
// settings are: so that the attribute on <html> always names which one is in
// force rather than leaving the stylesheet to infer it from an absence.
const (
	sidebarWide   = "wide"
	sidebarNarrow = "narrow"
)

func validSidebar(v string) bool { return v == sidebarWide || v == sidebarNarrow }

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

	// TodayHref is the brand destination: Today for signed-in and shared
	// views, or sign-in for an anonymous visitor to the privacy page.
	TodayHref string

	// Shell is whether this page draws the application shell — the rail, the
	// top bar, the main column — around its content. An error page does not:
	// that is chrome for a stranger, and a full navigation wrapped around
	// "there is nothing at this address" offers the rest of the app to
	// somebody who has not got it. Every other page does, including the
	// signed-out ones, where the rail carries the wordmark and nothing else.
	Shell bool

	// Sidebar is "wide" or "narrow" and lands on <html> beside the theme,
	// which is what the stylesheet keys the rail's width off.
	Sidebar string

	// SidebarToggle is the collapse/expand control: a link back to this URL
	// with the other width, exactly as the two switchers are. Following it
	// works with no script at all.
	SidebarToggle chromeOpt

	// SidebarWideHref and SidebarNarrowHref are that same control's two
	// destinations. app.js flips the rail without a round trip and needs the
	// other one to point the link at afterwards; the server renders whichever
	// applies now into SidebarToggle.Href for a reader with no script.
	SidebarWideHref   string
	SidebarNarrowHref string

	// ThemeNext is the theme the single-button control moves to, for a bar
	// too narrow to carry all three.
	ThemeNext chromeOpt

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

	// AdminWarning is a problem worth an admin's attention, raised on the way
	// into the area rather than only on the page that computes it. A page
	// nobody opens is not a signal.
	AdminWarning adminWarning

	// SearchPath is where the topbar's search control points, and doubles
	// as whether it renders at all: set for a signed-in reader and for the
	// genuine read-only share view, empty everywhere else. That "everywhere
	// else" matters — renderError builds its chrome with readOnly:true for
	// any visitor, signed in or not, so gating on ReadOnly alone would put
	// a search box on a stranger's 404. Checking prefix rules that out: an
	// error page has none, and only the share view's readOnly is paired
	// with one.
	SearchPath string
}

// SignedIn reports whether the account menu should render.
func (c chrome) SignedIn() bool { return c.User != nil && !c.ReadOnly }

// IsAdmin reports whether the admin entries belong in the account menu.
func (c chrome) IsAdmin() bool { return c.SignedIn() && c.User.IsAdmin }

// SidebarRows is the rail's contents: the views, then the admin area for an
// admin. One row for the area rather than five: which screen inside it you
// are on is the strip at the top of that screen's job, and the rail is for
// where in the application you are.
//
// Built here rather than in newChrome because it depends on the session,
// which newChrome resolves after it builds Nav.
func (c chrome) SidebarRows() []chromeOpt {
	rows := make([]chromeOpt, 0, len(c.Nav)+1)
	rows = append(rows, c.Nav...)
	if c.IsAdmin() {
		rows = append(rows, chromeOpt{
			Code:  "admin",
			Label: c.T.T("nav.admin"),
			Href:  "/admin/players",
			On:    c.AdminTab != "",
		})
	}
	return rows
}

// AdminTabs feeds the pill-nav shared by every admin screen. The four
// destinations are fixed, unlike Nav's — there is no admin page that can be
// absent — so this builds them from AdminTab rather than the caller passing
// a slice each time.
func (c chrome) AdminTabs() []chromeOpt {
	return []chromeOpt{
		{Label: c.T.T("admin.players.title"), Href: "/admin/players", On: c.AdminTab == "players"},
		{Label: c.T.T("pending.title"), Href: "/admin/pending", On: c.AdminTab == "pending"},
		{Label: c.T.T("activity.title"), Href: "/admin/activity", On: c.AdminTab == "activity"},
		{Label: c.T.T("diag.title"), Href: "/admin/diagnostics", On: c.AdminTab == "diagnostics"},
	}
}

// newChrome resolves the locale and theme for this request and builds the
// switchers.
//
// Both switchers are links back to the current URL with one parameter
// changed, so they work with no JavaScript at all and a page reached through
// one keeps whatever the reader was already looking at.
func (s *Server) newChrome(w http.ResponseWriter, r *http.Request, prefix, view string, readOnly bool) chrome {
	t := s.translatorFor(w, r)
	c := chrome{
		TodayHref: viewPath(prefix, viewToday),
		T:         t,
		Lang:      t.locale,
		Theme:     s.themeFor(w, r),
		Sidebar:   s.sidebarFor(w, r),
		Shell:     true,
		ReadOnly:  readOnly,
	}

	// The views are built whatever page this is, with none marked current
	// when the page is not one of them. Settings and the admin area are
	// still inside the app, and dropping the views there left the chrome
	// looking like a different site. Pages reached before a session exists
	// clear them again in signedOutChrome.
	for _, v := range navViews {
		c.Nav = append(c.Nav, chromeOpt{
			Code:  v,
			Label: t.T("nav.view." + v),
			Href:  viewPath(prefix, v),
			On:    v == view,
		})
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
	for _, theme := range []string{themeLight, themeSystem, themeDark} {
		c.Themes = append(c.Themes, chromeOpt{
			Code:  theme,
			Label: t.T("theme." + theme),
			Href:  urlWith(r, "theme", theme),
			On:    theme == c.Theme,
		})
	}
	c.ThemeLabel = t.T("theme.label") + ": " + t.T("theme."+c.Theme)
	c.LangLabel = t.T("lang.label") + ": " + s.catalogues[c.Lang]["locale.name"]

	// A bar too narrow for three theme buttons gets one that moves to the
	// next setting, in the order the three are offered in.
	order := []string{themeLight, themeSystem, themeDark}
	for i, theme := range order {
		if theme != c.Theme {
			continue
		}
		next := order[(i+1)%len(order)]
		c.ThemeNext = chromeOpt{
			Code:  next,
			Label: t.T("theme.cycle", t.T("theme."+c.Theme), t.T("theme."+next)),
			Href:  urlWith(r, "theme", next),
		}
		break
	}

	// Collapsing the rail is a per-device preference like the theme, and it
	// travels the same way: a link back to this URL with the other width,
	// remembered in a cookie. A script flipping a class would save the round
	// trip and would also put script between a reader and a control that
	// already works without it.
	toggle := chromeOpt{Code: sidebarNarrow, Label: t.T("nav.collapse")}
	if c.Sidebar == sidebarNarrow {
		toggle = chromeOpt{Code: sidebarWide, Label: t.T("nav.expand")}
	}
	toggle.Href = urlWith(r, "sidebar", toggle.Code)
	c.SidebarToggle = toggle
	c.SidebarWideHref = urlWith(r, "sidebar", sidebarWide)
	c.SidebarNarrowHref = urlWith(r, "sidebar", sidebarNarrow)

	if user, ok := authenticated(r); ok && !readOnly {
		c.User = &user
		c.Initials = initialsFor(user.Email)
	}

	// See the field comment: a real session, or a real share prefix, gets
	// the search control; a bare readOnly (an error page) gets neither.
	if c.User != nil || (readOnly && prefix != "") {
		c.SearchPath = viewPath(prefix, "search")
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

// sidebarFor resolves the rail's width exactly as themeFor resolves the
// theme, and for the same reason: it is a property of the device in front of
// the reader rather than of the account, so it lives in a cookie and the
// control that sets it is an ordinary link.
func (s *Server) sidebarFor(w http.ResponseWriter, r *http.Request) string {
	if requested := r.URL.Query().Get("sidebar"); validSidebar(requested) {
		http.SetCookie(w, &http.Cookie{
			Name:     sidebarCookie,
			Value:    requested,
			Path:     "/",
			HttpOnly: true,
			Secure:   s.secureCookies,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   365 * 24 * 60 * 60,
		})
		return requested
	}
	if c, err := r.Cookie(sidebarCookie); err == nil && validSidebar(c.Value) {
		return c.Value
	}
	return sidebarWide
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
	// The rail itself stays — emptied to the wordmark, it is the one piece
	// of the shell that says which application this is.
	c.Nav = nil
	c.Subtitle = ""
	// The account menu has nothing to show yet, and on the two-factor step
	// there is a session that is deliberately not yet an identity.
	c.User = nil
	// newChrome may have set this from a session that is fully valid but
	// simply landed here (a bookmark to /forgot-password, say): clear it
	// alongside User rather than leaving a search box for a chrome that is
	// otherwise entirely signed-out.
	c.SearchPath = ""
	return c
}

// The views the nav offers.
const (
	viewToday   = "today"
	viewBoard   = "board"
	viewMonths  = "months"
	viewGrid    = "grid"
	viewPlayers = "players"
)

// Both widths use the same view list, including Today. The brand also
// links to Today, so it remains reachable through either control.
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

	// Losing the container-level "unhealthy" signal when the services merged
	// traded a warning that came to you for a page you have to open. This
	// closes that: the admin area opens on Players, so a problem is said once
	// on the way in. It used to repeat on every tab, which made it furniture
	// rather than a warning — and said it loudest on the two pages that exist
	// to show the same thing in full.
	if tab == "players" {
		if fresh, err := store.ReadFreshness(r.Context(), s.db); err == nil {
			c.AdminWarning = s.adminWarningFor(c.T, fresh, time.Now())
		} else {
			s.logger.Error("read freshness for the admin warning", "error", err)
		}
	}
	return c
}
