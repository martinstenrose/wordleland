package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/martinstenrose/wordleland/internal/store"
)

// The editor's smaller actions, each a form of its own posting here: take a
// Signal sender off a player, withdraw an invitation, send a login's owner a
// password reset, detach a login, switch a login off, and attach a login
// nobody plays as to a player. Every one redirects back to the player with
// the outcome in the query string, so a reload cannot repeat it.

// playerAction resolves the player named in the path and checks the form,
// reporting whether the caller should carry on.
func (s *Server) playerAction(w http.ResponseWriter, r *http.Request) (store.Player, store.Actor, bool) {
	player, err := store.PlayerBySlug(r.Context(), s.db, r.PathValue("slug"))
	if err != nil {
		if !errors.Is(err, store.ErrPlayerNotFound) {
			s.logger.Error("read player", "error", err)
		}
		s.renderError(w, r, http.StatusNotFound)
		return store.Player{}, store.Actor{}, false
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return store.Player{}, store.Actor{}, false
	}
	if !s.checkCSRF(r) {
		s.renderAdminPlayer(w, r, player, "admin.error.expired", formFor(player))
		return store.Player{}, store.Actor{}, false
	}
	admin, _ := authenticated(r)
	return player, store.AdminActor(admin.ID), true
}

func (s *Server) playerRedirect(w http.ResponseWriter, r *http.Request, player store.Player, notice string) {
	http.Redirect(w, r, "/admin/players/"+player.Slug+"?notice="+notice, http.StatusSeeOther)
}

// handleAdminUnlinkSender takes a Signal sender off a player. Its next
// result waits in Pending results to be assigned again.
func (s *Server) handleAdminUnlinkSender(w http.ResponseWriter, r *http.Request) {
	player, actor, ok := s.playerAction(w, r)
	if !ok {
		return
	}
	err := store.UnlinkIdentity(r.Context(), s.db, actor, player.ID, r.PostFormValue("source"), r.PostFormValue("external_id"))
	if err != nil && !errors.Is(err, store.ErrIdentityNotFound) {
		s.logger.Error("unlink sender", "error", err)
		s.renderAdminPlayer(w, r, player, "admin.error.failed", formFor(player))
		return
	}
	s.playerRedirect(w, r, player, "senderUnlinked")
}

// handleAdminCancelInvite withdraws a player's invitation.
func (s *Server) handleAdminCancelInvite(w http.ResponseWriter, r *http.Request) {
	player, actor, ok := s.playerAction(w, r)
	if !ok {
		return
	}
	if err := store.CancelInvitation(r.Context(), s.db, actor, player.ID); err != nil && !errors.Is(err, store.ErrInvitationInvalid) {
		s.logger.Error("cancel invitation", "error", err)
		s.renderAdminPlayer(w, r, player, "admin.error.failed", formFor(player))
		return
	}
	s.playerRedirect(w, r, player, "inviteCancelled")
}

// handleAdminResetPassword mails the linked login's owner a reset link: the
// forgotten-password flow, started for them. Nothing about their password
// changes until they follow it.
func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	player, _, ok := s.playerAction(w, r)
	if !ok {
		return
	}
	if player.UserID == nil {
		s.playerRedirect(w, r, player, "saved")
		return
	}
	if !s.mailer.Configured() {
		s.renderAdminPlayer(w, r, player, "admin.error.noMail", formFor(player))
		return
	}
	user, err := store.UserByID(r.Context(), s.db, *player.UserID)
	if err == nil {
		err = s.issueResetLink(r, user.Email)
	}
	if err != nil {
		s.logger.Error("send reset link", "error", err)
		s.renderAdminPlayer(w, r, player, "admin.error.failed", formFor(player))
		return
	}
	s.playerRedirect(w, r, player, "resetSent")
}

// handleAdminUnlinkLogin detaches the login from the player. The login still
// exists, and shows under the sign-ins without a player.
func (s *Server) handleAdminUnlinkLogin(w http.ResponseWriter, r *http.Request) {
	player, actor, ok := s.playerAction(w, r)
	if !ok {
		return
	}
	if player.UserID != nil {
		if _, err := store.LinkPlayer(r.Context(), s.db, actor, player.ID, nil); err != nil {
			s.logger.Error("unlink login", "error", err)
			s.renderAdminPlayer(w, r, player, "admin.error.failed", formFor(player))
			return
		}
	}
	s.playerRedirect(w, r, player, "loginUnlinked")
}

// handleAdminDisableLogin switches the player's login off and detaches it.
//
// The design calls this deleting the user. A login cannot be deleted here:
// the activity log names whoever did each thing, and a row the log points
// at is kept for as long as the log is. Switching it off is what deleting
// would have been for — it can no longer sign in, and every session it held
// ends — and the player stays on the board with every score. An admin's own
// login is not offered it.
func (s *Server) handleAdminDisableLogin(w http.ResponseWriter, r *http.Request) {
	player, actor, ok := s.playerAction(w, r)
	if !ok {
		return
	}
	if player.UserID == nil {
		s.playerRedirect(w, r, player, "saved")
		return
	}
	user, err := store.UserByID(r.Context(), s.db, *player.UserID)
	if err != nil || user.IsAdmin {
		s.renderError(w, r, http.StatusForbidden)
		return
	}
	if err := store.SetUserDisabled(r.Context(), s.db, actor, user.ID, true); err != nil {
		s.logger.Error("disable login", "error", err)
		s.renderAdminPlayer(w, r, player, "admin.error.failed", formFor(player))
		return
	}
	if _, err := store.LinkPlayer(r.Context(), s.db, actor, player.ID, nil); err != nil {
		s.logger.Error("unlink disabled login", "error", err)
	}
	s.playerRedirect(w, r, player, "loginDisabled")
}

// handleAdminAttachLogin links a login that has no player to this one.
func (s *Server) handleAdminAttachLogin(w http.ResponseWriter, r *http.Request) {
	player, actor, ok := s.playerAction(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("user_id"), 10, 64)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	if _, err := store.LinkPlayer(r.Context(), s.db, actor, player.ID, &id); err != nil {
		key := "admin.error.failed"
		switch {
		case errors.Is(err, store.ErrUserLinkedElsewhere):
			key = "admin.error.userLinked"
		default:
			s.logger.Error("attach login", "error", err)
		}
		s.renderAdminPlayer(w, r, player, key, formFor(player))
		return
	}
	s.playerRedirect(w, r, player, "loginAttached")
}
