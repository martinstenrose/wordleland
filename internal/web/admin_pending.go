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
	// SuggestionNote says why the suggestion is worth a look when the
	// player has gone quiet: "who hasn't posted since June".
	SuggestionNote string

	// Initials, the shortened sender id, and the latest held result — its
	// tile, and which puzzle and when — as the card's head draws them.
	Initials string
	ShortID  string
	Label    string
	Tone     int
	Latest   string
	// More is the rest of what is held, as a line, empty when there is
	// only the one.
	More string
	// NewName is the name a new player would take from this sender: the
	// first word of what they post under.
	NewName string
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
	"pending.error.newTaken": true,
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
		pendingHead(page.T, &row, held, now)

		// The display name the sender posts under is the only clue there
		// is. Offered as a suggestion, never applied: it is deliberate that
		// a wrong guess attributes one player's scores to another.
		if sender.DisplayHint != "" {
			if match, ok := suggestPlayer(sender.DisplayHint, players); ok {
				row.Suggestion = match.Name
				row.SuggestionSlug = match.Slug
				if !match.Active {
					row.SuggestionNote = page.T.T("pending.suggest.retired")
				}
			}
		}
		page.Rows = append(page.Rows, row)
	}

	// The counts this section is read for. The one worth seeing from every
	// admin screen — senders still waiting to be claimed — is on the pill;
	// adminChrome puts it there.
	page.Section.Sub = page.T.T("pending.nothing")
	if page.Open > 0 {
		page.Section.Sub = page.T.TN("pending.waiting", page.Open)
	}

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, http.StatusOK, "admin_pending.html", page)
}

// pendingHead fills in what the card's head shows: who, and the latest
// result held for them.
func pendingHead(t translator, row *pendingRow, held []store.PendingResult, now time.Time) {
	for _, w := range strings.Fields(row.DisplayHint) {
		for _, r := range w {
			row.Initials += strings.ToUpper(string(r))
			break
		}
		if len([]rune(row.Initials)) == 2 {
			break
		}
	}
	if row.Initials == "" {
		row.Initials = "?"
	}
	if fields := strings.Fields(row.DisplayHint); len(fields) > 0 {
		row.NewName = fields[0]
	}
	id := row.ExternalID
	if len(id) > 10 {
		id = id[:4] + "…" + id[len(id)-4:]
	}
	row.ShortID = row.Source + " · " + id

	if len(held) == 0 {
		return
	}
	sort.Slice(held, func(i, j int) bool { return held[i].PuzzleNo > held[j].PuzzleNo })
	latest := held[0]
	row.Label, row.Tone = "X", 7
	if latest.Solved && latest.Guesses != nil {
		row.Label, row.Tone = strconv.Itoa(*latest.Guesses), *latest.Guesses
	}
	if latest.HardMode {
		row.Label += "*"
	}
	row.Latest = t.T("player.puzzle", t.Puzzle(latest.PuzzleNo))
	if latest.PostedAt != nil {
		at := latest.PostedAt.In(time.Local)
		when := at.Format("15:04")
		if y, m, d := now.Date(); at.Year() != y || at.Month() != m || at.Day() != d {
			when = t.T("weekday.short."+strconv.Itoa(int(at.Weekday()))) + " " + when
		}
		row.Latest += " · " + when
	}
	if len(held) > 1 {
		row.More = pendingSnippet(t, held[1:])
	}
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
		parts = append(parts, t.T("pending.line", t.Puzzle(h.PuzzleNo), score))
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
	var player store.Player
	var summary store.ReplaySummary
	var err error
	if slug == "new" {
		// A sender nobody plays as yet: a player made from the name they
		// post under, and the sender claimed for them in one transaction,
		// so a claim that fails leaves no player behind.
		name := strings.TrimSpace(r.PostFormValue("new_name"))
		if name == "" {
			s.pendingRedirect(w, r, "", "pending.error.noPlayer")
			return
		}
		player, summary, err = store.CreatePlayerForSenders(r.Context(), s.db, store.AdminActor(admin.ID),
			name, "", []store.Sender{{Source: source, ExternalID: externalID}})
		if errors.Is(err, store.ErrSlugTaken) {
			s.pendingRedirect(w, r, "", "pending.error.newTaken")
			return
		}
	} else {
		player, err = store.PlayerBySlug(r.Context(), s.db, slug)
		if err != nil {
			s.pendingRedirect(w, r, "", "pending.error.noPlayer")
			return
		}
		summary, err = store.LinkIdentity(r.Context(), s.db, store.AdminActor(admin.ID),
			player.ID, source, externalID, store.ActionIdentityClaimed, false)
	}
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
