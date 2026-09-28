package web

import (
	"net/http"
	"strings"

	"github.com/martinstenrose/wordleland/internal/store"
)

// adminSection is one row of the admin area's list: a glyph, the section, a
// line saying how it stands, and a count or an all's-well where it has one.
type adminSection struct {
	Href, Icon, Label, Sub string
	Badge                  int
	OK                     bool
}

type adminHomePage struct {
	chrome
	Sections []adminSection
}

// handleAdminHome is the admin area as a list of its sections, which is how
// a phone opens it: five tabs do not fit a phone's width, and a list says
// more about each — who is waiting, when the last change was — than a tab
// has room for. A wide window goes straight into a section instead; see
// chrome.AdminHref.
func (s *Server) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	c := s.newChrome(w, r, "", "", false)
	t := c.T
	c.Page = chromeOpt{Code: "admin", Label: t.T("nav.admin"), Href: "/admin", On: true}
	page := adminHomePage{chrome: c}

	pending := adminSection{Href: "/admin/pending", Icon: "inbox", Label: t.T("pending.title"), Sub: t.T("pending.nothing"), Badge: c.PendingCount}
	if senders, err := store.ListPendingSenders(r.Context(), s.db); err == nil && len(senders) > 0 {
		var names []string
		for _, sd := range senders {
			if f := strings.Fields(sd.DisplayHint); len(f) > 0 {
				names = append(names, f[0])
			}
		}
		if len(names) > 0 {
			pending.Sub = t.T("admin.home.waiting", joinList(t, names))
		}
	}

	players := adminSection{Href: "/admin/players", Icon: "group", Label: t.T("admin.players.title")}
	if list, err := store.ListPlayers(r.Context(), s.db); err == nil {
		logins := 0
		for _, p := range list {
			if p.UserID != nil {
				logins++
			}
		}
		players.Sub = t.T("admin.home.players", t.TN("player.count", len(list)), logins)
	}

	activity := adminSection{Href: "/admin/activity", Icon: "history", Label: t.T("activity.title"), Sub: t.T("admin.activity.hint")}
	if events, _, err := store.ListActivity(r.Context(), s.db, "", 1); err == nil && len(events) > 0 {
		row := s.activityRowFor(events[0], t)
		activity.Sub = t.T("admin.home.last", row.Text, row.Clock)
	}

	diagnostics := adminSection{Href: "/admin/diagnostics", Icon: "monitor_heart", Label: t.T("diag.title"), Sub: t.T("diag.status.none")}
	if s.bridge != nil {
		alive, _ := s.bridge.Alive()
		st := s.bridge.Status()
		switch {
		case !alive:
			diagnostics.Sub = t.T("diag.status.down")
		case !st.Connected:
			diagnostics.Sub = t.T("diag.status.reconnecting")
		default:
			diagnostics.OK = true
			diagnostics.Sub = t.T("admin.home.bridge")
			if !st.LastMessage.IsZero() {
				diagnostics.Sub = t.T("admin.home.bridgeSeen", st.LastMessage.Local().Format("15:04"))
			}
		}
	}

	settings := adminSection{Href: "/admin/settings", Icon: "tune", Label: t.T("admin.settings.title"), Sub: t.T("admin.settings.hint")}

	page.Sections = []adminSection{pending, players, activity, diagnostics, settings}
	s.render(w, r, http.StatusOK, "admin_home.html", page)
}
