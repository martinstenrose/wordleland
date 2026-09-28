package web

import (
	"context"
	"errors"
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

// The three arrangements a page can be drawn in. See chrome.Frame.
const (
	frameApp  = "app"
	frameAuth = "auth"
	frameBare = "bare"
)

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

	// PrivacyHref is where the footer's and the About panel's privacy link
	// goes: the share view's own copy under a share prefix, /privacy
	// everywhere else. A share reader sent to /privacy would have left the
	// share view for the signed-out one, whose wordmark leads to sign-in.
	PrivacyHref string

	// Frame is which of the three arrangements this page is drawn in.
	//
	// frameApp is the application shell: the glass bar floating over the
	// page, and the footer under it. frameAuth is the sign-in family — a card centred on the canvas,
	// with the wordmark in one corner and the pickers in the other and no
	// navigation at all, because there is nothing yet to navigate. frameBare
	// is an error page: chrome for a stranger, where a full navigation
	// wrapped around "there is nothing at this address" would be offering
	// the rest of the application to somebody who has not got it.
	Frame string

	// Subtitle is the design's "N days": how long the group has been at it,
	// said beside the wordmark in the footer and in the account menu.
	Subtitle string

	// Page names a page in the shell that is not one of the views —
	// Settings, Search, Privacy — for the phone's bar, which shows where you
	// are rather than every page at once. Set by that page's handler; a view
	// or an admin screen needs nothing, since Nav and AdminTab already say.
	Page chromeOpt

	// Tiles is the field of score tiles behind the sign-in card: one tone
	// per tile, 2–6 as the score ramp draws them and 0 for an empty square.
	// Set only in the sign-in family. See authTiles.
	Tiles []int

	// DisplayName heads the account menu: the name of the player this
	// account is linked to, or its address where it is linked to nobody.
	DisplayName string

	// User is nil when nobody is signed in, which is also how a read-only
	// page suppresses the account menu.
	User      *store.User
	Initials  string
	CSRFToken string

	// ReadOnly hides everything that implies an account.
	ReadOnly bool

	// AdminTab marks which admin page is open, for the pill row they share.
	AdminTab string

	// Section is that row, with the head it sits under: the section's name
	// and the pill for every other one. Empty outside the area. A page with
	// counts worth saying overwrites its Hint; the Pending pill's count is
	// set for every screen by adminChrome.
	Section switcher

	// AdminWarning is a problem worth an admin's attention, raised on the way
	// into the area rather than only on the page that computes it. A page
	// nobody opens is not a signal.
	AdminWarning adminWarning

	// Live is set on a page that redraws itself when a result lands — Today
	// and the board — and nil everywhere else. See live.go.
	Live *liveView

	// SearchPath is where the topbar's search control points, and doubles
	// as whether it renders at all: set for a signed-in reader and for the
	// genuine read-only share view, empty everywhere else. That "everywhere
	// else" matters — renderError builds its chrome with readOnly:true for
	// any visitor, signed in or not, so gating on ReadOnly alone would put
	// a search box on a stranger's 404. Checking prefix rules that out: an
	// error page has none, and only the share view's readOnly is paired
	// with one.
	SearchPath string

	// PendingCount is how many senders wait in Pending results, counted for
	// an admin only; 0 for everyone else.
	PendingCount int
}

// SignedIn reports whether the account menu should render.
func (c chrome) SignedIn() bool { return c.User != nil && !c.ReadOnly }

// IsAdmin reports whether the admin entries belong in the account menu.
func (c chrome) IsAdmin() bool { return c.SignedIn() && c.User.IsAdmin }

// AdminHref is where the account menu's Admin area row goes on a wide
// window: straight to Pending while senders wait there, to Settings
// otherwise. A phone goes to the list of sections at /admin instead.
func (c chrome) AdminHref() string {
	if c.PendingCount > 0 {
		return "/admin/pending"
	}
	return "/admin/settings"
}

// Here is the page the phone's bar names: its capsule holds where you are
// and opens the rest, where a desktop's shows every view at once. A view, the
// admin area, or the page a handler named in Page; the wordmark itself where
// none of them applies, which is an error page or the share view's search.
func (c chrome) Here() chromeOpt {
	for _, v := range c.Nav {
		if v.On {
			return v
		}
	}
	if c.AdminTab != "" {
		return chromeOpt{Code: "admin", Label: c.T.T("nav.admin"), Href: "/admin/settings", On: true}
	}
	if c.Page.Label != "" {
		return c.Page
	}
	return chromeOpt{Label: c.T.T("app.name"), Href: c.TodayHref}
}

// AdminTabs feeds the pill row shared by every admin screen. The five
// destinations are fixed, unlike Nav's — there is no admin page that can be
// absent — so this builds them from AdminTab rather than the caller passing
// a slice each time.
//
// Settings leads, and is where the account menu's Admin area row lands: it is the screen
// that answers "what is this installation", which is the question someone
// opening the area for the first time has.
func (c chrome) AdminTabs() []chromeOpt {
	return []chromeOpt{
		{Label: c.T.T("admin.settings.title"), Href: "/admin/settings", On: c.AdminTab == "settings"},
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
		TodayHref:   viewPath(prefix, viewToday),
		PrivacyHref: viewPath(prefix, "privacy"),
		T:           t,
		Lang:        t.locale,
		Theme:       s.themeFor(w, r),
		Frame:       frameApp,
		ReadOnly:    readOnly,
	}

	// Every page in the shell renders one form whoever built the page did
	// not think about: sign out, in the account menu. Issuing the token here
	// rather than per handler is what keeps that working — Diagnostics and
	// the activity log had no token, so their sign-out posted an empty one
	// and came back to the page it was on, having done nothing. A handler
	// that needs a token for a form of its own may still ask for one; the
	// same token comes back.
	//
	// A failure here is not worth a 500: everything on the page still reads,
	// and the one control that needs the token will be refused by checkCSRF
	// rather than acting without it.
	if token, err := s.issueCSRFToken(w, r); err == nil {
		c.CSRFToken = token
	} else {
		s.logger.Error("issue csrf token", "error", err)
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

	// "N days", for the footer and the account menu. Built here rather than
	// by each page: five pages set it and the rest did not, so it vanished on
	// Settings and in the admin area.
	if days, err := s.playedPuzzles(r.Context()); err != nil {
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

	if user, ok := authenticated(r); ok && !readOnly {
		c.User = &user
		c.Initials = initialsFor(user.Email)
		// Senders waiting to be claimed, for an admin: a count on the
		// avatar from anywhere, since a result held for want of a click is
		// a result missing from the board. One aggregate query.
		if user.IsAdmin {
			if senders, err := store.ListPendingSenders(r.Context(), s.db); err == nil {
				c.PendingCount = len(senders)
			} else {
				s.logger.Error("count pending senders", "error", err)
			}
		}
		c.DisplayName = user.Email
		// The player this account plays as, where there is one: the account
		// menu is headed by who you are, and an address is only that where
		// there is nothing better. Not worth failing a page over either way.
		if p, err := store.PlayerByUserID(r.Context(), s.db, user.ID); err == nil {
			c.DisplayName = p.Name
		} else if !errors.Is(err, store.ErrPlayerNotFound) {
			s.logger.Error("find the player linked to the account", "error", err)
		}
	}

	// See the field comment: a real session, or a real share prefix, gets
	// the search control; a bare readOnly (an error page) gets neither.
	if c.User != nil || (readOnly && prefix != "") {
		c.SearchPath = viewPath(prefix, "search")
	}
	return c
}

// playedPuzzlesHold is how long the count behind the wordmark's subtitle
// is kept before it is read again.
const playedPuzzlesHold = time.Minute

// playedPuzzles is the count behind the wordmark's subtitle, held for a
// minute at a time.
//
// The chrome is built for every page, and this was a query on every one of
// them — for a number that changes once a day, when the first result for a
// new puzzle lands. A minute's staleness on a day counter is invisible.
// Invalidating on ingest instead is not worth what it costs: results arrive
// by three routes — the HTTP endpoint, the bridge, and the CLI in another
// process — and a notifier through all of them is more machinery than a
// clock. An error is the caller's to handle and is not held: a count that
// could not be read is read again next time.
func (s *Server) playedPuzzles(ctx context.Context) (int, error) {
	s.played.Lock()
	defer s.played.Unlock()
	if !s.played.at.IsZero() && time.Since(s.played.at) < playedPuzzlesHold {
		return s.played.n, nil
	}
	n, err := store.CountPlayedPuzzles(ctx, s.db)
	if err != nil {
		return 0, err
	}
	s.played.n, s.played.at = n, time.Now()
	return n, nil
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
	// Never this one. "partial=1" asks a handler for a fragment instead of a
	// page — it is how a request was made, not part of what is being looked
	// at — and a link built while serving one would hand a reader a bare
	// card with no page around it the moment they followed it without a
	// script to catch the press.
	q.Del("partial")

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
	// Its own frame rather than the application shell. A bar emptied down
	// to the wordmark is a navigation with nothing in it, which reads as an
	// app that has lost its menu rather than as a door: the design puts the
	// card on a field of tiles with no bar at all, and the language in the
	// footer. Nav is cleared all the same: every view needs a session, so
	// offering one here would be offering a round trip back to this page.
	//
	// The subtitle stays. It used to go, on the reasoning that how much
	// history exists is not for a visitor who has not signed in — but the
	// card's own panel says "398 puzzles logged over 45 days" in figures,
	// which settles that question in the other direction, and on a phone,
	// where that panel does not fit, this is the only place it is said.
	c.Frame = frameAuth
	c.Tiles = authTiles
	c.Nav = nil
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

// authTiles is the field behind the sign-in card: enough tiles to cover a
// wide window at the size app.css draws them, in the proportions a group's
// scores actually fall in — mostly 4s and 3s, a few 5s, the odd 2 and 6, and
// the odd day nobody played. Fixed rather than random, so the door looks the
// same every time and a page is the same bytes on every request; the order
// comes from a small linear congruential sequence so nobody had to type out
// four hundred numbers.
var authTiles = func() []int {
	out := make([]int, 22*18)
	x := uint32(1925)
	for i := range out {
		x = x*1664525 + 1013904223
		r := float64(x>>8) / (1 << 24)
		switch {
		case r < .05:
			out[i] = 2
		case r < .25:
			out[i] = 3
		case r < .6:
			out[i] = 4
		case r < .82:
			out[i] = 5
		case r < .93:
			out[i] = 6
		}
	}
	return out
}()

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
	c.Section = c.adminSwitcher()

	// Senders waiting to be claimed, counted on the Pending pill from every
	// admin screen: a count only the pending screen shows is a count you
	// have to go there to see. One aggregate query, and not worth failing a
	// page over.
	if s.bridge != nil {
		if alive, _ := s.bridge.Alive(); alive && s.bridge.Status().Connected {
			for i := range c.Section.Items {
				if c.Section.Items[i].Href == "/admin/diagnostics" {
					c.Section.Items[i].Mark = c.T.T("diag.bridgeConnected")
				}
			}
		}
	}
	if c.PendingCount > 0 {
		for i := range c.Section.Items {
			if c.Section.Items[i].Href == "/admin/pending" {
				c.Section.Items[i].Badge = c.T.Integer(c.PendingCount)
			}
		}
	}

	// Losing the container-level "unhealthy" signal when the services merged
	// traded a warning that came to you for a page you have to open. This
	// closes that, on Players: results held for an unclaimed sender are
	// claimed by naming the player they belong to, so that is the screen the
	// warning is actionable on. It used to repeat on every tab, which made it
	// furniture rather than a warning — and said it loudest on the two pages
	// that exist to show the same thing in full.
	if tab == "players" {
		if fresh, err := store.ReadFreshness(r.Context(), s.db); err == nil {
			c.AdminWarning = s.adminWarningFor(c.T, fresh, time.Now())
		} else {
			s.logger.Error("read freshness for the admin warning", "error", err)
		}
	}
	return c
}
