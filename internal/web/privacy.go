package web

import "net/http"

// privacyPage is the data for privacy.html.
type privacyPage struct {
	chrome
}

// handlePrivacyPage serves the notice at /privacy. It is linked from the
// footer on every page, so it has to work reached signed in or signed out —
// readOnly follows which one the visitor actually is, so the topbar offers
// the sign-in button for a stranger and the account menu for a member, the
// same as every other page.
func (s *Server) handlePrivacyPage(w http.ResponseWriter, r *http.Request) {
	_, signedIn := authenticated(r)
	s.handlePrivacy(w, r, "", !signedIn)
}

// handlePrivacy serves the plain-language notice about what this
// installation stores and why, under a prefix: empty for the application,
// the share path for the read-only view. The share view has its own copy
// so a reader who came in by the link keeps its bar — the views, search,
// and a wordmark that leads back to the shared Today rather than to a
// sign-in page they have no account for.
func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request, prefix string, readOnly bool) {
	ch := s.newChrome(w, r, prefix, "", readOnly)
	ch.Page = chromeOpt{Code: "privacy", Label: ch.T.T("footer.privacy"), On: true}
	if readOnly && prefix == "" {
		// newChrome always builds the views; with no prefix they point at
		// "/today" and friends, which redirect a stranger straight to login.
		// signedOutChrome drops them for the same reason.
		ch.Nav = nil
		// The brand stays clickable without a detour through requireAuth.
		ch.TodayHref = "/"
	}
	s.render(w, r, http.StatusOK, "privacy.html", privacyPage{chrome: ch})
}
