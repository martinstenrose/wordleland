package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
)

// requireAdmin wraps a handler so only administrators reach it.
//
// A signed-in non-admin gets 404 rather than 403: the admin area is not a
// thing they are being told they cannot have, and enumerating it serves no
// purpose. requireAuth still runs first, so an anonymous visitor is sent to
// the login page as everywhere else.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		user, ok := authenticated(r)
		if !ok || !user.IsAdmin {
			s.renderError(w, r, http.StatusNotFound)
			return
		}
		next(w, r)
	})
}

// adminPlayerRow is one line of the roster list.
type adminPlayerRow struct {
	store.Player
	// LinkedEmail is empty when the player has no login attached.
	LinkedEmail string
	Games       int
	Average     string
	Trait       string
	Why         string
	Status      string
	Href        string
	Selected    bool
}

type adminPlayersPage struct {
	chrome

	Rows []adminPlayerRow

	// Counts sit under the heading, as the design has them.
	Count    int
	Linked   int
	Unlinked int

	// Selected is the player the panel is editing, nil when none is chosen.
	// The list and the panel share one page, so choosing a player is a link
	// rather than something that needs script.
	Selected *adminPlayerPanel

	// Notice reports the outcome of the previous action, carried in the
	// query string so a reload cannot repeat the write.
	Notice string
	Error  string

	// InviteLocale is the language pre-selected on the invitation form.
	// English rather than the admin's own: the message is for somebody
	// else, and their language is not the sender's to assume.
	InviteLocale string
}

// adminPlayerPanel is the editing panel beside the list.
type adminPlayerPanel struct {
	store.Player

	Games    int
	Average  string
	LastSeen string
	SlugBase string
	Users    []store.User
	Form     adminPlayerForm

	// Pending is an invitation waiting to be accepted.
	Pending      *store.Invitation
	PendingUntil string
	CanInvite    bool

	// Linked describes the attached login, nil when there is none.
	Linked      *store.User
	LinkedSince string
	Initials    string
	Role        string
}

// adminPlayerForm is the editable surface, mirroring `player update` and
// `player link`. There is deliberately no delete: retirement is
// active = false.
type adminPlayerForm struct {
	Name   string
	Slug   string
	Active bool
	UserID string
}

// handleAdminPlayers renders the roster and, when one is chosen, the panel
// that edits it.
//
// One page rather than two: the design puts the list and the editor side by
// side, and choosing a player is a link, so nothing here needs script.
func (s *Server) handleAdminPlayers(w http.ResponseWriter, r *http.Request) {
	board, players, results, _, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	games := make(map[int64]int, len(players))
	for _, res := range results {
		games[res.PlayerID]++
	}

	page := adminPlayersPage{
		chrome: s.adminChrome(w, r, "players"),

		Notice: r.URL.Query().Get("notice"),
		Count:  len(players),

		InviteLocale: defaultLocale,
	}

	// The chosen player comes from the path when one was followed, and from
	// the query when a row in the list was clicked.
	chosen := r.PathValue("slug")
	if chosen == "" {
		chosen = r.URL.Query().Get("player")
	}

	traits := stats.NewTraiter(board)
	figures := make(map[int64]stats.Player, len(players))
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			figures[p.ID] = p
		}
	}

	for _, p := range players {
		row := adminPlayerRow{
			Player:   p,
			Games:    games[p.ID],
			Average:  "—",
			Href:     "/admin/players/" + p.Slug,
			Selected: p.Slug == chosen,
			Status:   page.T.T("admin.active"),
		}
		if !p.Active {
			row.Status = page.T.T("admin.retired")
		}
		if f, ok := figures[p.ID]; ok {
			row.Average = formatScore(page.T, f.Average)
			if key := traits.For(f); key != "" {
				row.Trait = page.T.T("trait." + key)
				row.Why = page.T.T("trait." + key + ".why")
			}
		}
		if p.UserID != nil {
			page.Linked++
			if user, err := store.UserByID(r.Context(), s.db, *p.UserID); err != nil {
				s.logger.Error("read linked user", "player", p.Slug, "error", err)
			} else {
				row.LinkedEmail = user.Email
			}
		} else {
			page.Unlinked++
		}
		page.Rows = append(page.Rows, row)
	}

	if chosen != "" {
		panel, err := s.adminPanel(r, chosen, games, figures)
		if err != nil {
			if !errors.Is(err, store.ErrPlayerNotFound) {
				s.logger.Error("build admin panel", "error", err)
			}
			s.renderError(w, r, http.StatusNotFound)
			return
		}
		page.Selected = panel
	}

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, http.StatusOK, "admin_players.html", page)
}

// adminPanel gathers everything the editing panel shows for one player.
func (s *Server) adminPanel(r *http.Request, slug string, games map[int64]int, figures map[int64]stats.Player) (*adminPlayerPanel, error) {
	player, err := store.PlayerBySlug(r.Context(), s.db, slug)
	if err != nil {
		return nil, err
	}

	users, err := store.ListUsers(r.Context(), s.db)
	if err != nil {
		return nil, err
	}

	t := s.translatorFor(nil, r)
	panel := &adminPlayerPanel{
		Player:   player,
		Games:    games[player.ID],
		Average:  "—",
		LastSeen: "—",
		// Shown beside the slug field so an admin can see the address they
		// are about to change. Falls back to a bare path when APP_URL is
		// unset, which is the local-run case.
		SlugBase: strings.TrimPrefix(s.cfg.AppURL, "https://") + "/p/",
		Users:    users,
		Form:     formFor(player),
	}
	if f, ok := figures[player.ID]; ok {
		panel.Average = formatScore(t, f.Average)
		if f.LastPlayed != nil {
			panel.LastSeen = f.LastPlayed.Format(time.DateOnly)
		}
	}

	panel.CanInvite = s.mailer.Configured()
	if player.UserID == nil {
		if inv, err := store.PendingInvitation(r.Context(), s.db, player.ID); err == nil {
			panel.Pending = &inv
			panel.PendingUntil = inv.ExpiresAt.Format(time.DateOnly)
		}
	}

	if player.UserID != nil {
		user, err := store.UserByID(r.Context(), s.db, *player.UserID)
		if err != nil {
			return nil, err
		}
		panel.Linked = &user
		panel.Initials = initialsFor(user.Email)
		panel.Role = t.T("settings.role.player")
		if user.IsAdmin {
			panel.Role = t.T("settings.role.admin")
		}
		if user.EmailVerifiedAt != nil {
			panel.LinkedSince = user.EmailVerifiedAt.Format(time.DateOnly)
		}
	}
	return panel, nil
}

// handleAdminPlayerSubmit applies an edit.
func (s *Server) handleAdminPlayerSubmit(w http.ResponseWriter, r *http.Request) {
	player, err := store.PlayerBySlug(r.Context(), s.db, r.PathValue("slug"))
	if err != nil {
		if !errors.Is(err, store.ErrPlayerNotFound) {
			s.logger.Error("read player", "error", err)
		}
		s.renderError(w, r, http.StatusNotFound)
		return
	}

	// Checked after the lookup so a bad token on an unknown player still
	// answers 404, telling a caller without a token nothing about the
	// roster.
	if !s.checkCSRF(r) {
		// Re-rendered with a fresh token rather than erroring: the usual
		// cause is a form left open until the cookie expired.
		s.renderAdminPlayer(w, r, player, "admin.error.expired", formFor(player))
		return
	}

	user, _ := authenticated(r)
	actor := store.AdminActor(user.ID)

	form := adminPlayerForm{
		Name: strings.TrimSpace(r.PostFormValue("name")),
		Slug: strings.TrimSpace(r.PostFormValue("slug")),
		// An unchecked box submits nothing, so presence is the value. That
		// is safe here in a way it is not for the CLI's flags: this form
		// always carries every field, so absence really does mean false
		// rather than "not mentioned".
		Active: r.PostFormValue("active") != "",
		UserID: r.PostFormValue("user_id"),
	}

	if form.Name == "" {
		s.renderAdminPlayer(w, r, player, "admin.error.nameRequired", form)
		return
	}
	if !store.ValidSlug(form.Slug) {
		s.renderAdminPlayer(w, r, player, "admin.error.badSlug", form)
		return
	}

	updated, err := store.UpdatePlayer(r.Context(), s.db, actor, player.ID, store.PlayerUpdate{
		Name: &form.Name, Slug: &form.Slug, Active: &form.Active,
	})
	switch {
	case errors.Is(err, store.ErrSlugTaken):
		s.renderAdminPlayer(w, r, player, "admin.error.slugTaken", form)
		return
	case errors.Is(err, store.ErrInvalidSlug):
		s.renderAdminPlayer(w, r, player, "admin.error.badSlug", form)
		return
	case err != nil:
		s.logger.Error("update player", "player", player.Slug, "error", err)
		s.renderAdminPlayer(w, r, player, "admin.error.failed", form)
		return
	}

	// Presence, not value. The link control is hidden until there is a
	// user-management screen to pick from, and an absent field read as an
	// empty selection would unlink every player somebody merely renamed.
	_, linkOffered := r.PostForm["user_id"]
	if err := s.applyLink(r, actor, updated, form.UserID, linkOffered); err != nil {
		key := "admin.error.failed"
		if errors.Is(err, store.ErrUserLinkedElsewhere) {
			key = "admin.error.userLinked"
		} else {
			s.logger.Error("link player", "player", updated.Slug, "error", err)
		}
		// The name and slug are already saved; report the link failure
		// against the player as it now stands rather than pretending the
		// whole edit was rejected.
		s.renderAdminPlayer(w, r, updated, key, form)
		return
	}

	// Redirect after the write, so a reload cannot repeat it.
	http.Redirect(w, r, "/admin/players?notice=saved", http.StatusSeeOther)
}

// applyLink attaches or detaches a login, doing nothing when the selection
// already matches what is stored, or when the form did not carry the field
// at all.
func (s *Server) applyLink(r *http.Request, actor store.Actor, player store.Player,
	raw string, offered bool) error {

	if !offered {
		return nil
	}

	var want *int64
	if raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		want = &id
	}

	switch {
	case want == nil && player.UserID == nil:
		return nil
	case want != nil && player.UserID != nil && *want == *player.UserID:
		return nil
	}

	_, err := store.LinkPlayer(r.Context(), s.db, actor, player.ID, want)
	return err
}

func formFor(p store.Player) adminPlayerForm {
	form := adminPlayerForm{Name: p.Name, Slug: p.Slug, Active: p.Active}
	if p.UserID != nil {
		form.UserID = strconv.FormatInt(*p.UserID, 10)
	}
	return form
}

// renderAdminPlayer re-renders the page with the panel open and whatever was
// submitted still in the fields, so a rejected edit is not thrown away.
// renderAdminPlayer re-renders the page with the panel open and whatever was
// submitted still in the fields, so a rejected edit is not thrown away.
func (s *Server) renderAdminPlayer(w http.ResponseWriter, r *http.Request, player store.Player, errKey string, form adminPlayerForm) {
	board, players, results, _, err := s.boardData(r)
	if err != nil {
		s.logger.Error("build board", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	games := make(map[int64]int, len(players))
	for _, res := range results {
		games[res.PlayerID]++
	}
	figures := make(map[int64]stats.Player, len(players))
	for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			figures[p.ID] = p
		}
	}

	page := adminPlayersPage{
		chrome: s.adminChrome(w, r, "players"),

		Error: errKey,
		Count: len(players),

		InviteLocale: defaultLocale,
	}

	traits := stats.NewTraiter(board)
	for _, p := range players {
		row := adminPlayerRow{
			Player: p, Games: games[p.ID], Average: "—",
			Href:     "/admin/players/" + p.Slug,
			Selected: p.ID == player.ID,
			Status:   page.T.T("admin.active"),
		}
		if !p.Active {
			row.Status = page.T.T("admin.retired")
		}
		if f, ok := figures[p.ID]; ok {
			row.Average = formatScore(page.T, f.Average)
			if key := traits.For(f); key != "" {
				row.Trait = page.T.T("trait." + key)
				row.Why = page.T.T("trait." + key + ".why")
			}
		}
		if p.UserID != nil {
			page.Linked++
			if user, err := store.UserByID(r.Context(), s.db, *p.UserID); err == nil {
				row.LinkedEmail = user.Email
			}
		} else {
			page.Unlinked++
		}
		page.Rows = append(page.Rows, row)
	}

	panel, err := s.adminPanel(r, player.Slug, games, figures)
	if err != nil {
		s.logger.Error("build admin panel", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	panel.Form = form
	page.Selected = panel

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}

	status := http.StatusOK
	if errKey != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, status, "admin_players.html", page)
}

// issueChromeToken attaches a CSRF token, reporting whether the caller
// should carry on. It writes the error response itself when issuing fails.
func (s *Server) issueChromeToken(w http.ResponseWriter, r *http.Request, c *chrome) bool {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return false
	}
	c.CSRFToken = token
	return true
}
