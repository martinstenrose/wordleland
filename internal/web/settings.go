package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

type settingsPage struct {
	chrome

	// Player is the player this login reports for, nil when none is linked.
	Player *store.Player

	Email        string
	PendingEmail string
	Verified     bool
	Role         string

	HasTOTP bool
	// TOTPRequired marks an admin, for whom two-factor is not optional.
	TOTPRequired bool

	// RecoveryLeft is how many unused codes remain, so somebody running
	// low finds out before it is the thing locking them out.
	RecoveryLeft int

	// Notice and Error report the outcome of the last change, carried in the
	// query string so a reload cannot repeat a write.
	Notice string
	Error  string

	// Form holds what was submitted when something was rejected.
	Form settingsForm
}

type settingsForm struct {
	Name  string
	Email string
}

// handleSettings shows the signed-in reader's own account.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	user, _ := authenticated(r)
	s.renderSettings(w, r, user, r.URL.Query().Get("notice"), "", settingsForm{})
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, user store.User, notice, errKey string, form settingsForm) {
	s.renderSettingsStatus(w, r, user, notice, errKey, form, 0)
}

// renderSettingsStatus is renderSettings with the response code chosen by
// the caller, and a status of 0 meaning "the usual one for this errKey".
//
// Only the rate limiter needs it. Being throttled is a 429 because the
// answer is to wait, not the 422 that every other rejected settings form
// gets, which means the form itself is wrong.
func (s *Server) renderSettingsStatus(w http.ResponseWriter, r *http.Request, user store.User, notice, errKey string, form settingsForm, status int) {
	page := settingsPage{
		chrome:       s.newChrome(w, r, "", "", false),
		Email:        user.Email,
		Verified:     user.EmailVerifiedAt != nil,
		HasTOTP:      user.HasTOTP,
		TOTPRequired: user.IsAdmin,
		Notice:       notice,
		Error:        errKey,
		Form:         form,
	}
	if user.PendingEmail != nil {
		page.PendingEmail = *user.PendingEmail
	}

	if user.HasTOTP {
		left, err := store.CountRecoveryCodes(r.Context(), s.db, user.ID)
		if err != nil {
			// Not fatal: the rest of the page is still worth showing, and
			// the count is information rather than a control.
			s.logger.Error("count recovery codes", "error", err)
		}
		page.RecoveryLeft = left
	}

	page.Role = page.T.T("settings.role.player")
	if user.IsAdmin {
		page.Role = page.T.T("settings.role.admin")
	}

	// The display name on the board belongs to the player, not the login:
	// a login with no player linked has no name to show anywhere, and
	// inventing a second one would put two names on one person.
	if player, err := store.PlayerByUserID(r.Context(), s.db, user.ID); err == nil {
		page.Player = &player
		if page.Form.Name == "" {
			page.Form.Name = player.Name
		}
	} else if !errors.Is(err, store.ErrPlayerNotFound) {
		s.logger.Error("read linked player", "error", err)
	}
	if page.Form.Email == "" {
		page.Form.Email = user.Email
	}

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}

	if status == 0 {
		status = http.StatusOK
		if errKey != "" {
			status = http.StatusUnprocessableEntity
		}
	}
	s.render(w, r, status, "settings.html", page)
}

// handleSettingsName renames the player this login reports for.
func (s *Server) handleSettingsName(w http.ResponseWriter, r *http.Request) {
	user, ok := s.settingsSubmit(w, r)
	if !ok {
		return
	}

	player, err := store.PlayerByUserID(r.Context(), s.db, user.ID)
	if err != nil {
		s.renderSettings(w, r, user, "", "settings.error.noPlayer", settingsForm{})
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		s.renderSettings(w, r, user, "", "settings.error.nameRequired", settingsForm{Name: name})
		return
	}

	// The actor is the player themselves, which the audit log distinguishes
	// from an admin doing it for them.
	if _, err := store.UpdatePlayer(r.Context(), s.db, store.PlayerActor(user.ID), player.ID,
		store.PlayerUpdate{Name: &name}); err != nil {
		s.logger.Error("rename player", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{Name: name})
		return
	}
	http.Redirect(w, r, "/settings?notice=name", http.StatusSeeOther)
}

// handleSettingsEmail starts a change of address.
func (s *Server) handleSettingsEmail(w http.ResponseWriter, r *http.Request) {
	user, ok := s.settingsSubmit(w, r)
	if !ok {
		return
	}

	if !s.mailer.Configured() {
		s.renderSettings(w, r, user, "", "settings.error.noMail", settingsForm{})
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	err := store.SetPendingEmail(r.Context(), s.db, store.PlayerActor(user.ID), user.ID, email)
	switch {
	case errors.Is(err, store.ErrInvalidEmail):
		s.renderSettings(w, r, user, "", "settings.error.badEmail", settingsForm{Email: email})
		return
	case errors.Is(err, store.ErrEmailUnchanged):
		s.renderSettings(w, r, user, "", "settings.error.sameEmail", settingsForm{Email: email})
		return
	case errors.Is(err, store.ErrEmailTaken):
		// Deliberately the same message as an invalid address: saying "that
		// one is taken" tells whoever asks who else is on the board.
		s.renderSettings(w, r, user, "", "settings.error.badEmail", settingsForm{Email: email})
		return
	case err != nil:
		s.logger.Error("set pending email", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{Email: email})
		return
	}

	if err := s.sendVerifyEmail(r, user, email); err != nil {
		s.logger.Error("send verification", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{Email: email})
		return
	}
	http.Redirect(w, r, "/settings?notice=email", http.StatusSeeOther)
}

// handleSettingsPassword changes the password, current one first.
func (s *Server) handleSettingsPassword(w http.ResponseWriter, r *http.Request) {
	user, ok := s.settingsSubmit(w, r)
	if !ok {
		return
	}

	current := r.PostFormValue("current")
	next := r.PostFormValue("password")

	// The current password is required even though the session is already
	// authenticated: a borrowed screen should not be enough to take the
	// account.
	//
	// Which makes this a second place to guess a password at, so it is
	// limited like the first one, and before the hash rather than after:
	// verifying costs 64 MiB and the CPU time to go with it, so an
	// unthrottled endpoint is a way to spend the box's memory as well as a
	// way to find the password. Its own key, not login's — throttling the
	// two together would let a signed-in tab lock its owner out of the
	// sign-in form.
	clientIP := auth.ClientIP(r, s.cfg.TrustedProxies)
	if !s.limiter.Allow("settings-password:user:"+strconv.FormatInt(user.ID, 10),
		"settings-password:ip:"+clientIP) {
		s.logger.Warn("settings password change rate limited", "ip", clientIP)
		s.renderSettingsStatus(w, r, user, "", "settings.error.tooMany",
			settingsForm{}, http.StatusTooManyRequests)
		return
	}

	var verifyErr error
	if err := s.limiter.WithHashSlot(r.Context(), func() error {
		verifyErr = auth.VerifyPassword(user.PasswordHash, current)
		return nil
	}); err != nil {
		s.logger.Error("verify password", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{})
		return
	}
	if verifyErr != nil {
		s.renderSettings(w, r, user, "", "settings.error.wrongPassword", settingsForm{})
		return
	}
	if len([]rune(next)) < auth.MinPasswordLength {
		s.renderSettings(w, r, user, "", "settings.error.shortPassword", settingsForm{})
		return
	}

	var hash string
	err := s.limiter.WithHashSlot(r.Context(), func() error {
		var err error
		hash, err = s.hashPassword(next)
		return err
	})
	if err != nil {
		s.logger.Error("hash password", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{})
		return
	}
	if err := store.SetUserPassword(r.Context(), s.db, store.PlayerActor(user.ID), user.ID, hash); err != nil {
		s.logger.Error("set password", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{})
		return
	}
	// Cleared on success, as sign-in does, so earlier typos are not still
	// being counted against whoever gets this right.
	s.limiter.Reset("settings-password:user:" + strconv.FormatInt(user.ID, 10))

	// Changing the password ends every session, this one included, so the
	// reader lands back at sign-in rather than on a page they can no longer
	// act on.
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/?notice=password", http.StatusSeeOther)
}

// settingsSubmit does the checks every settings write shares.
func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	user, ok := authenticated(r)
	if !ok {
		s.renderError(w, r, http.StatusForbidden)
		return store.User{}, false
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return store.User{}, false
	}
	if !s.checkCSRF(r) {
		s.renderSettings(w, r, user, "", "settings.error.expired", settingsForm{})
		return store.User{}, false
	}
	return user, true
}
