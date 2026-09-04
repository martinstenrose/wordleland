package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

// pendingRow is one unmatched sender awaiting assignment.
type pendingRow struct {
	Source      string
	ExternalID  string
	DisplayHint string

	// Snippet is the held results as plain text — the line the message
	// carried, not a grid of squares.
	Snippet string
	Seen    string
	Count   int

	// Suggestion is a player worth offering in one click, from the display
	// name the sender posts under. Empty when nothing matches well enough.
	Suggestion     string
	SuggestionSlug string
}

type pendingPage struct {
	chrome

	Rows    []pendingRow
	Players []store.Player

	// Open is the senders still waiting, Held the results they carry.
	Open   int
	Count  int
	Notice string
	Error  string
}

// pendingProblems is every problem code pendingRedirect issues.
//
// The template renders Error as {{.T.T .Error}}, so whatever reaches it is
// used as a translation-catalogue key — and it arrives in a query string,
// which means a link somebody else wrote chooses which entry of the
// catalogue is shown on an admin page. The template already matches Notice
// against fixed values before translating it; Error cannot be checked the
// same way there, because there are five of it, so it is checked here.
var pendingProblems = map[string]bool{
	"pending.error.noPlayer": true,
	"pending.error.taken":    true,
	"pending.error.gone":     true,
	"pending.error.expired":  true,
	"pending.error.failed":   true,
}

// pendingProblem passes through a problem code this handler issues, and
// drops anything else.
func pendingProblem(problem string) string {
	if pendingProblems[problem] {
		return problem
	}
	return ""
}

// handleAdminPending lists senders whose results are held for want of a
// player to attach them to.
func (s *Server) handleAdminPending(w http.ResponseWriter, r *http.Request) {
	senders, err := store.ListPendingSenders(r.Context(), s.db)
	if err != nil {
		s.logger.Error("list pending senders", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	players, err := store.ListPlayers(r.Context(), s.db)
	if err != nil {
		s.logger.Error("list players", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}

	page := pendingPage{
		chrome:  s.adminChrome(w, r, "pending"),
		Players: players,
		Open:    len(senders),
		Notice:  r.URL.Query().Get("notice"),
		Error:   pendingProblem(r.URL.Query().Get("problem")),
	}

	for _, sender := range senders {
		page.Count += sender.Count
	}

	now := time.Now()
	for _, sender := range senders {
		row := pendingRow{
			Source:      sender.Source,
			ExternalID:  sender.ExternalID,
			DisplayHint: sender.DisplayHint,
			Count:       sender.Count,
			Seen:        sinceText(page.T, sender.LastSeen, now),
		}

		held, _, err := store.PendingResultsFor(r.Context(), s.db, sender.Source, sender.ExternalID)
		if err != nil {
			s.logger.Error("read held results", "error", err)
		}
		row.Snippet = pendingSnippet(page.T, held)

		// The display name the sender posts under is the only clue there
		// is. Offered as a suggestion, never applied: it is deliberate that
		// a wrong guess attributes one player's scores to another.
		if sender.DisplayHint != "" {
			if match, ok := suggestPlayer(sender.DisplayHint, players); ok {
				row.Suggestion = match.Name
				row.SuggestionSlug = match.Slug
			}
		}
		page.Rows = append(page.Rows, row)
	}

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, http.StatusOK, "admin_pending.html", page)
}

// pendingSnippet renders the held results as a line of text.
func pendingSnippet(t translator, held []store.PendingResult) string {
	if len(held) == 0 {
		return ""
	}
	sort.Slice(held, func(i, j int) bool { return held[i].PuzzleNo > held[j].PuzzleNo })

	// The newest few, in the share text's own convention. A sender with
	// months of history would otherwise fill the page.
	const shown = 3
	var parts []string
	for i, h := range held {
		if i == shown {
			break
		}
		score := "X"
		if h.Solved && h.Guesses != nil {
			score = strconv.Itoa(*h.Guesses)
		}
		if h.HardMode {
			score += "*"
		}
		parts = append(parts, t.T("pending.line", h.PuzzleNo, score))
	}
	line := strings.Join(parts, " · ")
	if len(held) > shown {
		line += " · " + t.TN("pending.more", len(held)-shown)
	}
	return line
}

// suggestPlayer matches a display name to a player by name or slug.
//
// Exact, case-insensitive matches only. Anything looser guesses, and a
// wrong guess here writes somebody else's scores under your name.
func suggestPlayer(hint string, players []store.Player) (store.Player, bool) {
	want := strings.ToLower(strings.TrimSpace(hint))
	if want == "" {
		return store.Player{}, false
	}
	for _, p := range players {
		if strings.ToLower(p.Name) == want || p.Slug == want {
			return p, true
		}
	}
	return store.Player{}, false
}

// handleAdminPendingAssign attaches a sender to a player and replays what
// was held for them.
func (s *Server) handleAdminPendingAssign(w http.ResponseWriter, r *http.Request) {
	admin, source, externalID, ok := s.pendingSubmit(w, r)
	if !ok {
		return
	}

	slug := strings.TrimSpace(r.PostFormValue("player"))
	player, err := store.PlayerBySlug(r.Context(), s.db, slug)
	if err != nil {
		s.pendingRedirect(w, r, "", "pending.error.noPlayer")
		return
	}

	summary, err := store.LinkIdentity(r.Context(), s.db, store.AdminActor(admin.ID),
		player.ID, source, externalID, store.ActionIdentityClaimed, false)
	switch {
	case errors.Is(err, store.ErrIdentityTaken):
		s.pendingRedirect(w, r, "", "pending.error.taken")
		return
	case err != nil:
		s.logger.Error("claim identity", "error", err)
		s.pendingRedirect(w, r, "", "pending.error.failed")
		return
	}

	s.logger.Info("pending sender claimed",
		"player", player.Slug, "replayed", summary.Replayed, "skipped", summary.Skipped)
	s.pendingRedirect(w, r, "assigned", "")
}

// handleAdminPendingDiscard drops a sender's held results.
func (s *Server) handleAdminPendingDiscard(w http.ResponseWriter, r *http.Request) {
	admin, source, externalID, ok := s.pendingSubmit(w, r)
	if !ok {
		return
	}

	_, err := store.DiscardPendingResults(r.Context(), s.db, store.AdminActor(admin.ID), source, externalID)
	switch {
	case errors.Is(err, store.ErrNoPendingResults):
		s.pendingRedirect(w, r, "", "pending.error.gone")
		return
	case err != nil:
		s.logger.Error("discard pending", "error", err)
		s.pendingRedirect(w, r, "", "pending.error.failed")
		return
	}
	s.pendingRedirect(w, r, "discarded", "")
}

// pendingSubmit does the checks both actions share.
func (s *Server) pendingSubmit(w http.ResponseWriter, r *http.Request) (store.User, string, string, bool) {
	admin, _ := authenticated(r)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return store.User{}, "", "", false
	}
	if !s.checkCSRF(r) {
		s.pendingRedirect(w, r, "", "pending.error.expired")
		return store.User{}, "", "", false
	}

	source := strings.TrimSpace(r.PostFormValue("source"))
	externalID := strings.TrimSpace(r.PostFormValue("external_id"))
	if source == "" || externalID == "" {
		s.renderError(w, r, http.StatusBadRequest)
		return store.User{}, "", "", false
	}
	return admin, source, externalID, true
}

func (s *Server) pendingRedirect(w http.ResponseWriter, r *http.Request, notice, problem string) {
	target := "/admin/pending"
	switch {
	case notice != "":
		target += "?notice=" + notice
	case problem != "":
		target += "?problem=" + problem
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
