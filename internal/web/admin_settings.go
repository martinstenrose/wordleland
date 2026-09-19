package web

import (
	"errors"
	"net/http"

	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/store"
)

// settingRow is one line of the environment table: the variable's name, what
// it came to, and how to draw it.
//
// Value is already worded for the reader — "Not set", "Enabled" — because the
// words are translated and config.Setting deliberately carries a kind instead
// of a phrase.
type settingRow struct {
	Name  string
	Value string
	Mono  bool

	// Muted greys the value, and marks one thing only: that there is no
	// value here. It used to grey three states — unset, off, and a secret
	// — which put "Configured" in the same visual class as "Not set" while
	// meaning the opposite of it, and made the column unsweepable for the
	// one question it is worth sweeping for.
	Muted bool

	// Redacted marks a value that exists and is deliberately not shown. It
	// draws as a run of dots at full strength: something is here, and it is
	// not for this screen. Value then carries the sentence that says so,
	// for a reader who cannot see the dots.
	Redacted bool
}

type adminSettingsPage struct {
	chrome

	// ShareURL is the read-only link to the board, absolute when APP_URL is
	// set and a bare path when it is not — the path is still what someone
	// needs, just without the origin.
	ShareURL string
	// Slug is the same link's last segment, drawn apart from the rest so the
	// part that changes is the part that stands out.
	Slug   string
	Origin string

	// Confirming is the rotate form's second step. Rotating breaks every
	// link the group already has, so it is asked once before it happens
	// rather than undone afterwards — it cannot be undone.
	Confirming bool

	Env []settingRow

	Notice string
	Error  string
}

// handleAdminSettings shows what this installation is configured to do.
//
// Everything on it but the share link is read-only, and deliberately: these
// are environment variables, set where the process is started, and a screen
// that let an admin type over one would be writing somewhere the next restart
// does not read. Showing them is still worth a page — the question "what is
// this deployment actually doing" previously had no answer short of shelling
// into the container.
func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	page := adminSettingsPage{chrome: s.adminChrome(w, r, "settings")}
	q := r.URL.Query()
	page.Confirming = q.Get("confirm") == "slug"
	page.Notice = noticeText(page.T, "admin.settings.notice.", q.Get("notice"))
	page.Error = noticeText(page.T, "admin.settings.error.", q.Get("error"))

	slug, err := store.ShareSlug(r.Context(), s.db)
	switch {
	case errors.Is(err, store.ErrNoSettings):
		// Before the first serve there is no slug to show. Not a fault, and
		// not worth a 500: the rest of the page is still the answer.
	case err != nil:
		s.logger.Error("read share slug", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	default:
		page.Slug = slug
		page.Origin = s.cfg.AppURL + "/share/"
		page.ShareURL = page.Origin + slug + "/"
	}

	page.Env = envRows(page.T, s.cfg.Settings(s.bridgeCfg))

	s.render(w, r, http.StatusOK, "admin_settings.html", page)
}

// envRows words each setting for this reader.
func envRows(t translator, settings []config.Setting) []settingRow {
	rows := make([]settingRow, 0, len(settings))
	for _, set := range settings {
		row := settingRow{Name: set.Name, Mono: set.Mono}
		switch set.Kind {
		case config.SettingValue:
			row.Value = set.Value
		case config.SettingSecret:
			row.Value, row.Redacted = t.T("admin.settings.redacted"), true
		case config.SettingOn:
			row.Value = t.T("admin.settings.enabled")
		case config.SettingOff:
			// Off is a state, not an absence: a switch that is off is as
			// much an answer as one that is on, and DEMO_MODE off is an
			// answer somebody is specifically here to check.
			row.Value = t.T("admin.settings.disabled")
		default:
			row.Value, row.Muted = t.T("admin.settings.notSet"), true
		}
		rows = append(rows, row)
	}
	return rows
}

// handleAdminSlugRotate replaces the share link.
//
// Two steps, both without JavaScript: the button links to the same page with
// the confirmation showing, and the confirmation posts. A dialog that only
// appears once a script has run is a dialog that sometimes does not, and this
// is the one action here that cannot be taken back.
func (s *Server) handleAdminSlugRotate(w http.ResponseWriter, r *http.Request) {
	admin, _ := authenticated(r)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.settingsRedirect(w, r, "", "expired")
		return
	}

	if _, err := store.RotateShareSlug(r.Context(), s.db, store.AdminActor(admin.ID)); err != nil {
		s.logger.Error("rotate share slug", "error", err)
		s.settingsRedirect(w, r, "", "failed")
		return
	}
	s.settingsRedirect(w, r, "rotated", "")
}

func (s *Server) settingsRedirect(w http.ResponseWriter, r *http.Request, notice, errCode string) {
	to := "/admin/settings"
	switch {
	case notice != "":
		to += "?notice=" + notice
	case errCode != "":
		to += "?error=" + errCode
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// noticeText turns a code from the query string into a sentence, and an
// unknown code into nothing. The codes are in the URL, where anyone can type
// one; looking them up in the catalogue rather than printing them is what
// keeps that from putting chosen text on the page.
func noticeText(t translator, prefix, code string) string {
	if code == "" {
		return ""
	}
	key := prefix + code
	if msg := t.T(key); msg != key {
		return msg
	}
	return ""
}
