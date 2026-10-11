package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// A player correcting their own score.
//
// The login linked to a player may change that player's existing results
// and nobody else's — players.user_id is the grant, as docs/decisions.md
// says it would be. Every correction leaves a trail: the activity log
// records it with the score it replaced and the reason given, and the
// Puzzle page shows the group each one, so a 5 does not quietly become a 3.
//
// It is a form and a redirect, no script. The Puzzle page links here from
// the reader's own row.

// correctPage is the form, with the corrections already made to this result.
type correctPage struct {
	chrome

	PuzzleNo int
	Eyebrow  string
	Title    string
	Sub      string
	Action   string
	Back     string

	// Score is the select's value: "1" to "6", or "X" for a miss.
	Score    string
	Scores   []string
	HardMode bool
	Reason   string
	MaxLen   int

	Error string
	Trail []string
}

// correctTarget reads the puzzle from the path and the reader's player and
// result for it. Anything that is not theirs to correct is a 404, the same
// as a result that does not exist: a login with no player has nothing here.
func (s *Server) correctTarget(w http.ResponseWriter, r *http.Request) (store.User, store.Player, *store.Result, bool) {
	user, ok := authenticated(r)
	if !ok {
		s.renderError(w, r, http.StatusForbidden)
		return store.User{}, store.Player{}, nil, false
	}
	n, err := strconv.Atoi(r.PathValue("no"))
	if err != nil || n < 1 {
		s.renderError(w, r, http.StatusNotFound)
		return store.User{}, store.Player{}, nil, false
	}
	player, err := store.PlayerByUserID(r.Context(), s.db, user.ID)
	if errors.Is(err, store.ErrPlayerNotFound) {
		s.renderError(w, r, http.StatusNotFound)
		return store.User{}, store.Player{}, nil, false
	}
	if err != nil {
		s.logger.Error("read player for correction", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return store.User{}, store.Player{}, nil, false
	}
	result, err := store.ResultFor(r.Context(), s.db, n, player.ID)
	if errors.Is(err, store.ErrResultNotFound) {
		s.renderError(w, r, http.StatusNotFound)
		return store.User{}, store.Player{}, nil, false
	}
	if err != nil {
		s.logger.Error("read result for correction", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return store.User{}, store.Player{}, nil, false
	}
	return user, player, result, true
}

// handleCorrectForm renders GET /puzzle/{no}/correct.
func (s *Server) handleCorrectForm(w http.ResponseWriter, r *http.Request) {
	_, player, result, ok := s.correctTarget(w, r)
	if !ok {
		return
	}
	score := "X"
	if result.Solved && result.Guesses != nil {
		score = strconv.Itoa(*result.Guesses)
	}
	s.renderCorrect(w, r, http.StatusOK, player, result.PuzzleNo, score, result.HardMode, "", "")
}

// handleCorrectSubmit takes POST /puzzle/{no}/correct.
func (s *Server) handleCorrectSubmit(w http.ResponseWriter, r *http.Request) {
	user, player, result, ok := s.correctTarget(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest)
		return
	}
	n := result.PuzzleNo
	score := r.PostFormValue("score")
	hard := r.PostFormValue("hard_mode") != ""
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	fail := func(key string) {
		s.renderCorrect(w, r, http.StatusUnprocessableEntity, player, n, score, hard, reason, key)
	}

	if !s.checkCSRF(r) {
		fail("correct.error.expired")
		return
	}
	corrected := store.Result{PuzzleNo: n, HardMode: hard}
	if score != "X" {
		guesses, err := strconv.Atoi(score)
		if err != nil || guesses < 1 || guesses > wordle.MaxGuesses {
			fail("correct.error.score")
			return
		}
		corrected.Solved, corrected.Guesses = true, &guesses
	}
	if utf8.RuneCountInString(reason) > store.MaxCorrectionReason {
		fail("correct.error.reasonLong")
		return
	}

	_, err := store.CorrectOwnResult(r.Context(), s.db, user.ID, corrected, reason)
	switch {
	case errors.Is(err, store.ErrResultUnchanged):
		fail("correct.error.unchanged")
		return
	case errors.Is(err, store.ErrPlayerNotFound), errors.Is(err, store.ErrResultNotFound):
		// Unlinked or deleted since the form was drawn.
		s.renderError(w, r, http.StatusNotFound)
		return
	case err != nil:
		s.logger.Error("correct result", "error", err)
		fail("correct.error.failed")
		return
	}
	http.Redirect(w, r, puzzlePath("", n), http.StatusSeeOther)
}

func (s *Server) renderCorrect(w http.ResponseWriter, r *http.Request, status int,
	player store.Player, n int, score string, hard bool, reason, errKey string) {

	ch := s.newChrome(w, r, "", "", false)
	t := ch.T
	ch.Page = chromeOpt{Code: "puzzle", Label: t.T("puzzle.title"), On: true}

	date, _ := wordle.DateForPuzzle(n)
	page := correctPage{
		chrome:   ch,
		PuzzleNo: n,
		Eyebrow:  t.T("player.puzzle", t.Puzzle(n)) + " · " + longDate(t, date),
		Title:    t.T("correct.title"),
		Sub:      t.T("correct.sub", player.Name),
		Action:   puzzlePath("", n) + "/correct",
		Back:     puzzlePath("", n),
		Score:    score,
		Scores:   []string{"1", "2", "3", "4", "5", "6", "X"},
		HardMode: hard,
		Reason:   reason,
		MaxLen:   store.MaxCorrectionReason,
		Error:    errKey,
	}

	corrections, err := store.Corrections(r.Context(), s.db, n)
	if err != nil {
		s.logger.Error("read corrections", "error", err)
		s.renderError(w, r, http.StatusInternalServerError)
		return
	}
	page.Trail = correctionLines(t, corrections[player.ID])

	if !s.issueChromeToken(w, r, &page.chrome) {
		return
	}
	s.render(w, r, status, "correct.html", page)
}

// correctionLines renders a result's corrections as the group reads them:
// "4 → 3* · 1 October 2026 14:02 · “typed it wrong”".
func correctionLines(t translator, list []store.Correction) []string {
	lines := make([]string, 0, len(list))
	for _, c := range list {
		at := c.At.Local()
		line := t.T("correct.line", scoreLabel(c.From), scoreLabel(c.To), dayMonthYear(t, at)+" "+at.Format("15:04"))
		if c.Reason != "" {
			line += " · " + t.T("correct.reason", c.Reason)
		}
		lines = append(lines, line)
	}
	return lines
}

// scoreLabel is a score as a tile shows it: the guesses or X, starred for
// hard mode.
func scoreLabel(s store.Score) string {
	label := "X"
	if s.Solved {
		label = strconv.Itoa(s.Guesses)
	}
	if s.HardMode {
		label += "*"
	}
	return label
}
