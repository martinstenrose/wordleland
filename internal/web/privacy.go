package web

import "net/http"

// privacyPage is the data for privacy.html.
type privacyPage struct {
	chrome
}

// handlePrivacy serves the plain-language notice about what this
// installation stores and why. It is linked from the footer on every page,
// so it has to work reached signed in or signed out — readOnly follows
// which one the visitor actually is, so the topbar offers the sign-in
// button for a stranger and the account menu for a member, the same as
// every other page.
func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	_, signedIn := authenticated(r)
	ch := s.newChrome(w, r, "", "", !signedIn)
	if !signedIn {
		// newChrome always builds the view pills; with no prefix they point
		// at "/today" and friends, which redirect a stranger straight to
		// login. signedOutChrome drops them for the same reason.
		ch.Nav, ch.Tabs = nil, nil
	}
	s.render(w, r, http.StatusOK, "privacy.html", privacyPage{chrome: ch})
}
