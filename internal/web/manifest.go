package web

import (
	"encoding/json"
	"net/http"
)

// The Home Screen app. Added to a phone's Home Screen, Wordleland opens
// without the browser around it: the manifest says "standalone", and
// app.css draws the phone's app layout for that display mode — a tab bar
// at the foot, the account beside the page's title — since there is no
// browser toolbar left to keep the foot of the window clear for.
//
// There is one manifest, and the app starts at the root: Today with a
// session, the sign-in page without one. It is a member's app. Installed
// from a share link it starts at sign-in too — a share view is a link to
// read, not an app to keep, and a share app would end whenever the slug is
// rotated.

// webManifest is the subset of the Web App Manifest this app fills in.
type webManifest struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	Description     string         `json:"description,omitempty"`
	Lang            string         `json:"lang,omitempty"`
	StartURL        string         `json:"start_url"`
	Scope           string         `json:"scope"`
	Display         string         `json:"display"`
	BackgroundColor string         `json:"background_color"`
	ThemeColor      string         `json:"theme_color"`
	Icons           []manifestIcon `json:"icons"`
}

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
}

// canvasLight is the light theme's canvas, --color-canvas in app.css, as
// the hex a manifest and theme-color take. A splash screen and the status
// bar are drawn before any stylesheet, so they cannot read the token.
const (
	canvasLight = "#f8f4f1"
	canvasDark  = "#18130e"
)

// handleManifest serves the Home Screen app's manifest.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	t := s.translatorFor(w, r)
	const start = "/"
	m := webManifest{
		ID:              start,
		Name:            t.T("app.name"),
		ShortName:       t.T("app.name"),
		Description:     t.T("app.manifestDescription"),
		Lang:            t.locale,
		StartURL:        start,
		Scope:           start,
		Display:         "standalone",
		BackgroundColor: canvasLight,
		ThemeColor:      canvasLight,
		Icons: []manifestIcon{
			{Src: asset("/static/app-icon-192.png"), Sizes: "192x192", Type: "image/png"},
			{Src: asset("/static/app-icon-512.png"), Sizes: "512x512", Type: "image/png"},
			{Src: asset("/static/app-icon-maskable-512.png"), Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	// An hour: short enough that a new icon or name reaches a phone that
	// installs tomorrow, long enough not to be fetched on every page.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if err := json.NewEncoder(w).Encode(m); err != nil {
		s.logger.Warn("write manifest", "error", err)
	}
}
