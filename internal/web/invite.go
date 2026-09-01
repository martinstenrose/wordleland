package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
)

// invitePage is the claim form somebody reaches from their invitation.
type invitePage struct {
	chrome

	Token      string
	PlayerName string
	Email      string

	// Games and Average tell the reader the scores are already there, which
	// is the whole pitch: the invitation claims a player, it does not make
	// one.
	Games   int
	Average string

	Invalid bool
	Error   string
}

// handleInviteForm shows the claim form for a token.
func (s *Server) handleInviteForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		s.renderInvite(w, r, invitePage{Invalid: true}, http.StatusBadRequest)
		return
	}
	page, ok := s.invitePageFor(r, token, "")
	if !ok {
		s.renderInvite(w, r, invitePage{Invalid: true}, http.StatusBadRequest)
		return
	}
	s.renderInvite(w, r, page, http.StatusOK)
}

// handleInviteSubmit accepts an invitation and signs the new account in.
func (s *Server) handleInviteSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	token := r.PostFormValue("token")

	page, ok := s.invitePageFor(r, token, "")
	if !ok {
		s.renderInvite(w, r, invitePage{Invalid: true}, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		page.Error = "invite.error.expired"
		s.renderInvite(w, r, page, http.StatusForbidden)
		return
	}

	password := r.PostFormValue("password")
	if password != r.PostFormValue("password_confirm") {
		page.Error = "invite.error.mismatch"
		s.renderInvite(w, r, page, http.StatusUnprocessableEntity)
		return
	}
	if len([]rune(password)) < auth.MinPasswordLength {
		page.Error = "invite.error.short"
		s.renderInvite(w, r, page, http.StatusUnprocessableEntity)
		return
	}

	// Hashing is bounded like every other argon2 call here: an open
	// endpoint that hashes on demand is a memory-exhaustion lever
	// regardless of whether any account is ever created.
	var hash string
	if err := s.limiter.WithHashSlot(r.Context(), func() error {
		var err error
		hash, err = auth.HashPassword(password)
		return err
	}); err != nil {
		s.logger.Error("hash password", "error", err)
		page.Error = "invite.error.failed"
		s.renderInvite(w, r, page, http.StatusInternalServerError)
		return
	}

	user, err := store.AcceptInvitation(r.Context(), s.db, token, hash)
	switch {
	case errors.Is(err, store.ErrInvitationInvalid), errors.Is(err, store.ErrPlayerAlreadyLinked):
		s.renderInvite(w, r, invitePage{Invalid: true}, http.StatusBadRequest)
		return
	case errors.Is(err, store.ErrEmailTaken):
		page.Error = "invite.error.taken"
		s.renderInvite(w, r, page, http.StatusUnprocessableEntity)
		return
	case err != nil:
		s.logger.Error("accept invitation", "error", err)
		page.Error = "invite.error.failed"
		s.renderInvite(w, r, page, http.StatusInternalServerError)
		return
	}

	// Signed straight in: they have just proved the address and chosen the
	// password, so asking them to type it again achieves nothing.
	session, err := store.CreateSession(r.Context(), s.db, user.ID, false)
	if err != nil {
		s.logger.Error("create session", "error", err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w, session)
	http.Redirect(w, r, landingPath, http.StatusSeeOther)
}

// invitePageFor builds the page for a token, reporting whether it is live.
func (s *Server) invitePageFor(r *http.Request, token, errKey string) (invitePage, bool) {
	inv, player, err := store.InvitationByToken(r.Context(), s.db, token)
	if err != nil {
		if !errors.Is(err, store.ErrInvitationInvalid) && !errors.Is(err, store.ErrPlayerNotFound) {
			s.logger.Error("read invitation", "error", err)
		}
		return invitePage{}, false
	}

	page := invitePage{
		Token: token, PlayerName: player.Name, Email: inv.Email,
		Average: "—", Error: errKey,
	}
	t := s.translatorFor(nil, r)

	// The figures come from the same computation the board uses, so what an
	// invitation promises matches what they will find inside.
	if board, _, _, _, err := s.boardData(r); err == nil {
		for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
			for _, p := range group {
				if p.ID == player.ID {
					page.Games = p.Games
					page.Average = formatScore(t, p.Average)
				}
			}
		}
	}
	return page, true
}

func (s *Server) renderInvite(w http.ResponseWriter, r *http.Request, page invitePage, status int) {
	token, err := s.issueCSRFToken(w, r)
	if err != nil {
		s.logger.Error("issue csrf token", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	page.chrome = s.signedOutChrome(w, r, token)
	s.render(w, r, status, "invite.html", page)
}

// handleAdminInvite sends an invitation for a player.
func (s *Server) handleAdminInvite(w http.ResponseWriter, r *http.Request) {
	player, err := store.PlayerBySlug(r.Context(), s.db, r.PathValue("slug"))
	if err != nil {
		if !errors.Is(err, store.ErrPlayerNotFound) {
			s.logger.Error("read player", "error", err)
		}
		s.renderError(w, r, http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(r) {
		s.renderAdminPlayer(w, r, player, "admin.error.expired", formFor(player))
		return
	}
	if !s.mailer.Configured() {
		s.renderAdminPlayer(w, r, player, "admin.error.noMail", formFor(player))
		return
	}

	admin, _ := authenticated(r)
	email := strings.TrimSpace(r.PostFormValue("invite_email"))

	// The language the invitation is written in, and the one the account it
	// creates starts in. Defaulted rather than inherited from the admin:
	// the message is for the recipient, and an admin reading in Swedish
	// does not make the person they are inviting a Swedish speaker.
	locale := r.PostFormValue("locale")
	if !s.KnownLocale(locale) {
		locale = defaultLocale
	}

	token, err := store.CreateInvitation(r.Context(), s.db,
		store.AdminActor(admin.ID), player.ID, email, locale)
	switch {
	case errors.Is(err, store.ErrInvalidEmail):
		s.renderAdminPlayer(w, r, player, "admin.error.badEmail", formFor(player))
		return
	case errors.Is(err, store.ErrEmailTaken):
		s.renderAdminPlayer(w, r, player, "admin.error.emailHasAccount", formFor(player))
		return
	case errors.Is(err, store.ErrPlayerAlreadyLinked):
		s.renderAdminPlayer(w, r, player, "admin.error.alreadyLinked", formFor(player))
		return
	case err != nil:
		s.logger.Error("create invitation", "error", err)
		s.renderAdminPlayer(w, r, player, "admin.error.failed", formFor(player))
		return
	}

	if err := s.sendInviteEmail(r, player, admin, email, token, locale); err != nil {
		s.logger.Error("send invitation", "error", err)
		s.renderAdminPlayer(w, r, player, "admin.error.failed", formFor(player))
		return
	}
	http.Redirect(w, r, "/admin/players/"+player.Slug+"?notice=invited", http.StatusSeeOther)
}

// sendInviteEmail writes the invitation.
func (s *Server) sendInviteEmail(r *http.Request, player store.Player, admin store.User, to, token, locale string) error {
	link := fmt.Sprintf("%s/invite?token=%s", s.cfg.AppURL, token)
	t := s.translatorIn(locale)

	page := emailPage{
		Lang:    t.locale,
		AppName: t.T("app.name"),
		Subject: t.T("email.invite.subject", t.T("app.name")),
		Preview: t.T("email.invite.preview"),
		Heading: t.T("email.invite.heading"),
		Intro:   t.T("email.invite.intro"),

		ActionURL:   link,
		ActionLabel: t.T("email.invite.action"),
		ActionNote:  t.T("email.invite.note", int(store.InvitationLifetime.Hours()/24)),

		Aside: &emailAside{
			Title: t.T("email.invite.aside.title"),
			Body:  t.T("email.invite.aside.body"),
		},
		Footer: t.T("email.footer.invited", t.T("app.name"), admin.Email),
	}

	// The player's own figures, so the invitation says what is being
	// claimed rather than just asking for a password.
	if board, _, _, _, err := s.boardData(r); err == nil {
		for _, group := range [][]stats.Player{board.Ranked, board.Unranked} {
			for _, p := range group {
				if p.ID == player.ID {
					page.Panel = &emailPanel{
						Label: t.T("email.invite.panel"),
						Title: player.Name,
						Detail: t.TN("player.games", p.Games) + " · " +
							t.T("board.column.average") + " " + formatScore(t, p.Average),
					}
				}
			}
		}
	}
	if page.Panel == nil {
		page.Panel = &emailPanel{Label: t.T("email.invite.panel"), Title: player.Name}
	}
	return s.sendEmail(to, page)
}
