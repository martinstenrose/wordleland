package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/skip2/go-qrcode"

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
	// ConfirmDisable is the second step of turning two-factor off: the
	// first press only changes what the page shows, so the risk is read
	// before anything is typed into the field that commits it.
	ConfirmDisable bool
	// Confirm is which of the two-step card's questions is open: "codes",
	// "rotate" or "totp". Each is asked in the card before anything is done.
	Confirm string

	// RecoveryLeft is how many unused codes remain, so somebody running
	// low finds out before it is the thing locking them out.
	RecoveryLeft int

	// Notice and Error report the outcome of the last change, carried in the
	// query string so a reload cannot repeat a write.
	Notice string
	Error  string

	// Form holds what was submitted when something was rejected.
	Form settingsForm

	// Setup is an authenticator being set up in the two-step card: the
	// secret as a QR code and as text, and the form that proves it was
	// scanned. Nil unless the reader asked for it.
	Setup *totpSetup

	// Codes are a fresh set of recovery codes, shown once in the two-step
	// card; Enrolling says they finish a setup rather than replace a set.
	Codes     []string
	Enrolling bool
	// CodesText and CodesFile are the same codes for the copy button and
	// the download link.
	CodesText string
	CodesFile template.URL
}

// NoticeText is what the toast says for the outcome in ?notice=.
func (p settingsPage) NoticeText() string {
	switch p.Notice {
	case "name":
		return p.T.T("settings.saved.name")
	case "email":
		return p.T.T("settings.saved.email")
	case "totp-off":
		return p.T.T("settings.totp.disabled")
	case "totp-on":
		return p.T.T("settings.totp.enabled")
	case "codes":
		return p.T.T("settings.recovery.stored")
	}
	return p.T.T("admin.saved")
}

// totpSetup is an authenticator being set up inline on the settings page.
type totpSetup struct {
	QRCode template.URL
	// Secret is grouped in fours for typing by hand.
	Secret string
	// Replacing asks for the password too: see handleEnrolTOTPSubmit.
	Replacing bool
	Error     string
}

type settingsForm struct {
	Name  string
	Email string
}

// handleSettings shows the signed-in reader's own account: one page of three
// cards, as the design has it. /settings/account and /settings/security are
// the addresses the tabs it replaced had, and show the same page.
//
// ?setup=totp opens an authenticator's setup in the two-step card, issuing a
// fresh pending secret each time it is asked for — the enrolment page's own
// rule, which is what makes a mis-scanned code recoverable by asking again.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	user, _ := authenticated(r)
	if r.URL.Query().Get("setup") == "totp" {
		setup, err := s.newTOTPSetup(r, user)
		if err != nil {
			s.logger.Error("start authenticator setup", "error", err)
			s.renderError(w, r, http.StatusInternalServerError)
			return
		}
		s.renderSettingsStatus(w, r, user, "", "", settingsForm{}, 0, func(p *settingsPage) { p.Setup = setup })
		return
	}
	s.renderSettings(w, r, user, r.URL.Query().Get("notice"), "", settingsForm{})
}

// newTOTPSetup issues a pending secret and draws it for the two-step card.
func (s *Server) newTOTPSetup(r *http.Request, user store.User) (*totpSetup, error) {
	secret, uri, err := auth.GenerateTOTPSecret(user.Email)
	if err != nil {
		return nil, err
	}
	sealed, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		return nil, err
	}
	if err := store.SetPendingTOTPSecret(r.Context(), s.db, user.ID, sealed); err != nil {
		return nil, err
	}
	return drawTOTPSetup(secret, uri, user.HasTOTP)
}

// pendingTOTPSetup draws the secret already pending, for a setup sent back
// with an error: the reader keeps the code they have just scanned.
func (s *Server) pendingTOTPSetup(r *http.Request, user store.User) (*totpSetup, error) {
	sealed, err := store.PendingTOTPSecret(r.Context(), s.db, user.ID)
	if err != nil {
		return nil, err
	}
	secret, err := s.cipher.Decrypt(sealed)
	if err != nil {
		return nil, err
	}
	_, uri, err := auth.RebuildTOTPURI(string(secret), user.Email)
	if err != nil {
		return nil, err
	}
	return drawTOTPSetup(string(secret), uri, user.HasTOTP)
}

func drawTOTPSetup(secret, uri string, replacing bool) (*totpSetup, error) {
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		return nil, err
	}
	var grouped []string
	for i := 0; i < len(secret); i += 4 {
		grouped = append(grouped, secret[i:min(i+4, len(secret))])
	}
	return &totpSetup{QRCode: qrDataURI(png), Secret: strings.Join(grouped, " "), Replacing: replacing}, nil
}

// fromSettings reports whether an enrolment form was posted from the
// settings page's two-step card rather than the standalone enrolment page,
// so its answer goes back to where it was asked.
func fromSettings(r *http.Request) bool { return r.PostFormValue("from") == "settings" }

// renderSettingsSetup sends a setup back to the settings page with an
// error, keeping the pending secret.
func (s *Server) renderSettingsSetup(w http.ResponseWriter, r *http.Request, status int, message string) {
	user, _ := userFrom(r)
	setup, err := s.pendingTOTPSetup(r, user)
	if err != nil {
		http.Redirect(w, r, "/settings?setup=totp#two-step", http.StatusSeeOther)
		return
	}
	setup.Error = message
	s.renderSettingsStatus(w, r, user, "", "", settingsForm{}, status, func(p *settingsPage) { p.Setup = setup })
}

// renderSettingsCodes shows a fresh set of recovery codes in the two-step
// card, issuing them.
func (s *Server) renderSettingsCodes(w http.ResponseWriter, r *http.Request, user store.User, enrolling bool) {
	codes, err := store.ReplaceRecoveryCodes(r.Context(), s.db, store.PlayerActor(user.ID), user.ID)
	if err != nil {
		s.logger.Error("issue recovery codes", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	// Read again: enrolment has just switched two-step on.
	if fresh, err := store.UserByID(r.Context(), s.db, user.ID); err == nil {
		user = fresh
	}
	text := strings.Join(codes, "\n") + "\n"
	s.renderSettingsStatus(w, r, user, "", "", settingsForm{}, http.StatusOK, func(p *settingsPage) {
		p.Codes, p.Enrolling = codes, enrolling
		p.CodesText = text
		p.CodesFile = template.URL("data:text/plain;charset=utf-8," + url.PathEscape(text))
	})
}

// handleSettingsTOTPDisable turns two-factor off at the account's own request.
//
// Admins cannot: two-factor is what the admin area is gated on, and a screen
// that let one switch it off would make "required for admins" a suggestion.
// The check is here and not only in the template — the template decides what
// is offered, and this decides what is allowed.
//
// The password is required for the reason a replacement requires it, only
// more so: this takes the second factor off the account and cancels the
// recovery codes with it, and a session on a borrowed screen has already
// cleared both factors. The password is the one thing it does not carry.
func (s *Server) handleSettingsTOTPDisable(w http.ResponseWriter, r *http.Request) {
	user, ok := s.settingsSubmit(w, r)
	if !ok {
		return
	}
	if user.IsAdmin || !user.HasTOTP {
		s.renderError(w, r, http.StatusForbidden)
		return
	}

	// Before the hash rather than after, and on the same key the password
	// form uses: it is the same password being guessed at from the same
	// account, and verifying costs 64 MiB either way.
	clientIP := auth.ClientIP(r, s.cfg.TrustedProxies)
	if !s.limiter.Allow("settings-password:user:"+strconv.FormatInt(user.ID, 10),
		"settings-password:ip:"+clientIP) {
		s.logger.Warn("two-factor disable rate limited", "ip", clientIP)
		s.renderSettingsStatus(w, r, user, "", "settings.error.tooMany",
			settingsForm{}, http.StatusTooManyRequests)
		return
	}

	var verifyErr error
	if err := s.limiter.WithHashSlot(r.Context(), func() error {
		verifyErr = auth.VerifyPassword(user.PasswordHash, r.PostFormValue("password"))
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

	if err := store.DisableTOTP(r.Context(), s.db, store.PlayerActor(user.ID), user.ID); err != nil {
		s.logger.Error("disable totp", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{})
		return
	}
	s.limiter.Reset("settings-password:user:" + strconv.FormatInt(user.ID, 10))

	http.Redirect(w, r, "/settings?notice=totp-off#two-step", http.StatusSeeOther)
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, user store.User, notice, errKey string, form settingsForm) {
	s.renderSettingsStatus(w, r, user, notice, errKey, form, 0)
}

// settingsChrome is the chrome every settings screen shares, naming the page
// for the phone's bar, which shows where you are.
func (s *Server) settingsChrome(w http.ResponseWriter, r *http.Request) chrome {
	c := s.newChrome(w, r, "", "", false)
	c.Page = chromeOpt{Code: "settings", Label: c.T.T("nav.settings"), Href: "/settings", On: true}
	return c
}

// renderSettingsStatus is renderSettings with the response code chosen by
// the caller, and a status of 0 meaning "the usual one for this errKey".
//
// Only the rate limiter needs it. Being throttled is a 429 because the
// answer is to wait, not the 422 that every other rejected settings form
// gets, which means the form itself is wrong.
func (s *Server) renderSettingsStatus(w http.ResponseWriter, r *http.Request, user store.User, notice, errKey string, form settingsForm, status int, extra ...func(*settingsPage)) {
	page := settingsPage{
		chrome:       s.settingsChrome(w, r),
		Email:        user.Email,
		Verified:     user.EmailVerifiedAt != nil,
		HasTOTP:      user.HasTOTP,
		TOTPRequired: user.IsAdmin,
		Notice:       notice,
		Error:        errKey,
		Form:         form,
	}
	page.ConfirmDisable = user.HasTOTP && !user.IsAdmin && r.URL.Query().Get("confirm") == "totp"
	if user.HasTOTP {
		switch c := r.URL.Query().Get("confirm"); c {
		case "codes", "rotate", "totp":
			page.Confirm = c
		}
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

	for _, f := range extra {
		f(&page)
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

	// The actor is the player themselves, which the activity log distinguishes
	// from an admin doing it for them.
	if _, err := store.UpdatePlayer(r.Context(), s.db, store.PlayerActor(user.ID), player.ID,
		store.PlayerUpdate{Name: &name}); err != nil {
		s.logger.Error("rename player", "error", err)
		s.renderSettings(w, r, user, "", "settings.error.failed", settingsForm{Name: name})
		return
	}
	http.Redirect(w, r, "/settings?notice=name#profile", http.StatusSeeOther)
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
	http.Redirect(w, r, "/settings?notice=email#sign-in", http.StatusSeeOther)
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
