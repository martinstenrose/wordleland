package web

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/martinstenrose/wordleland/internal/store"
)

// handleShare serves the read-only board behind a capability URL.
//
// The slug is read from the database on every request rather than cached: it
// is rotated by the CLI, a separate process, so an in-process cache could not
// be invalidated and a rotation would not take effect until a restart.
// Reading it is a single indexed lookup on a one-row table.
//
// Nothing authenticated is ever served here. This is read-only by
// construction rather than by convention.
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	current, err := store.ShareSlug(r.Context(), s.db)
	if err != nil {
		if !errors.Is(err, store.ErrNoSettings) {
			s.logger.Error("read share slug", "error", err)
		}
		s.renderError(w, r, http.StatusNotFound)
		return
	}

	// Constant time: the slug is a capability, so a comparison that returns
	// early would leak how much of a guess was correct.
	if subtle.ConstantTimeCompare([]byte(r.PathValue("slug")), []byte(current)) != 1 {
		s.renderError(w, r, http.StatusNotFound)
		return
	}

	prefix := "/share/" + current
	boardPath := prefix + "/"

	// "GET /share/{slug}/" is a subtree pattern, so it also matches every
	// path beneath it. Each shared page is matched explicitly and anything
	// else is refused: without this a mistyped link, or a player slug that
	// does not exist, would render the board with a 200.
	// Each view is told its own path, because that is what its controls
	// link back to. Passing one shared value would point every filter and
	// sort at whichever view happened to hold the bare prefix.
	switch rest := strings.TrimPrefix(r.URL.Path, boardPath); {
	case r.URL.Path == boardPath:
		// The front page, read-only, with links built under the share
		// prefix so an anonymous visitor is never sent into authenticated
		// routing.
		s.handleToday(w, r, prefix, viewPath(prefix, viewToday), true)
	case rest == "board":
		s.handleBoard(w, r, prefix, viewPath(prefix, viewBoard), true)
	// The nav points at the bare prefix for the front page now; this keeps
	// a link somebody already holds from dying.
	case rest == "today":
		s.handleToday(w, r, prefix, viewPath(prefix, viewToday), true)
	case rest == "months":
		s.handleMonths(w, r, prefix, viewPath(prefix, viewMonths), true)
	case rest == "grid":
		s.handleGrid(w, r, prefix, viewPath(prefix, viewGrid), true)
	case rest == "players":
		s.handlePlayers(w, r, prefix, viewPath(prefix, viewPlayers), true)
	case strings.HasPrefix(rest, "p/") && store.ValidSlug(strings.TrimPrefix(rest, "p/")):
		s.handlePlayer(w, r, strings.TrimPrefix(rest, "p/"), prefix, viewPath(prefix, viewPlayers), true)
	default:
		s.renderError(w, r, http.StatusNotFound)
	}
}
