package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/martinstenrose/wordleland/internal/store"
)

// newPlayerForm is the design's New player card, in the editor's place
// beside the roster: a name, the address made from it, and any senders
// waiting in Pending to take onto the board as this player.
type newPlayerForm struct {
	Name, Slug string
	Senders    []newPlayerSender
	// SlugError says what is wrong with the address, "" when nothing is;
	// a clash names the player who has it, as the design does.
	SlugError string
}

// newPlayerSender is one waiting sender, as a choice.
type newPlayerSender struct {
	store.PendingSender
	Checked bool
	Sub     string
}

// newPlayerFor lists the waiting senders, checking the ones in chosen.
func (s *Server) newPlayerFor(r *http.Request, t translator, form newPlayerForm, chosen map[string]bool) (*newPlayerForm, error) {
	senders, err := store.ListPendingSenders(r.Context(), s.db)
	if err != nil {
		return nil, err
	}
	for _, sender := range senders {
		form.Senders = append(form.Senders, newPlayerSender{
			PendingSender: sender,
			Checked:       chosen[senderKey(sender.Source, sender.ExternalID)],
			Sub:           t.TN("admin.new.waiting", sender.Count),
		})
	}
	return &form, nil
}

// senderKey is how a sender travels in the form: "signal:<id>".
func senderKey(source, externalID string) string { return source + ":" + externalID }

// handleAdminPlayerCreate makes a player from the New player card, claiming
// any senders chosen with them in the same transaction.
func (s *Server) handleAdminPlayerCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	form := newPlayerForm{
		Name: strings.TrimSpace(r.PostFormValue("name")),
		Slug: strings.ToLower(strings.TrimSpace(r.PostFormValue("slug"))),
	}
	chosen := map[string]bool{}
	var senders []store.Sender
	for _, key := range r.PostForm["sender"] {
		source, externalID, ok := strings.Cut(key, ":")
		if !ok || source == "" || externalID == "" {
			continue
		}
		chosen[key] = true
		senders = append(senders, store.Sender{Source: source, ExternalID: externalID})
	}

	errKey := ""
	switch {
	case !s.checkCSRF(r):
		errKey = "admin.error.expired"
	case form.Name == "":
		errKey = "admin.error.nameRequired"
	case form.Slug != "" && !store.ValidSlug(form.Slug):
		form.SlugError = s.translatorFor(w, r).T("admin.new.slugInvalid")
	}
	if errKey == "" && form.SlugError == "" {
		user, _ := authenticated(r)
		player, summary, err := store.CreatePlayerForSenders(r.Context(), s.db, store.AdminActor(user.ID), form.Name, form.Slug, senders)
		switch {
		case err == nil:
			q := url.Values{"notice": {"created"}}
			if summary.Replayed > 0 {
				q.Set("moved", strconv.Itoa(summary.Replayed))
			}
			http.Redirect(w, r, "/admin/players/"+player.Slug+"?"+q.Encode(), http.StatusSeeOther)
			return
		case errors.Is(err, store.ErrSlugTaken):
			t := s.translatorFor(w, r)
			form.SlugError = t.T("admin.error.slugTaken")
			if other, err := store.PlayerBySlug(r.Context(), s.db, form.Slug); err == nil {
				form.SlugError = t.T("admin.new.slugTaken", other.Name)
			}
		case errors.Is(err, store.ErrInvalidSlug):
			form.SlugError = s.translatorFor(w, r).T("admin.new.slugInvalid")
		case errors.Is(err, store.ErrIdentityTaken):
			errKey = "pending.error.taken"
		default:
			s.logger.Error("create player", "error", err)
			errKey = "admin.error.failed"
		}
	}

	page, err := s.adminPlayersFor(w, r, "")
	if err != nil {
		s.logger.Error("build admin players", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	if page.New, err = s.newPlayerFor(r, page.T, form, chosen); err != nil {
		s.logger.Error("list pending senders", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	page.Error = errKey
	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, http.StatusUnprocessableEntity, "admin_players.html", page)
}
