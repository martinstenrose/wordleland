package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/store"
)

// ingestRequest is the body of POST /api/ingest.
type ingestRequest struct {
	// Sender identification. external_id is the stable id — for Signal the
	// account UUID, never sourceName, which the sender can change.
	Source      string `json:"source"`
	ExternalID  string `json:"external_id"`
	DisplayHint string `json:"display_hint"`

	// Admin and curl convenience: name the player directly instead.
	PlayerID *int64 `json:"player_id"`
	Slug     string `json:"slug"`

	PuzzleNo int   `json:"puzzle_no"`
	Solved   *bool `json:"solved"`
	Guesses  *int  `json:"guesses"`
	HardMode bool  `json:"hard_mode"`
}

// ingestResponse carries the outcome in a machine-readable form, so a caller
// need not infer it from the status code alone.
type ingestResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// handleIngest accepts a result from any token holder.
//
// The rules live in internal/ingest, shared with the Signal bridge, which
// calls them directly rather than posting to this endpoint. This function is
// the HTTP shell: authenticate, decode, translate the outcome into a status
// code. Nothing about what a result means is decided here.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	token, err := s.authenticateToken(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, ingestResponse{Status: "unauthorized", Error: "invalid token"})
		return
	}

	var req ingestRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ingestResponse{Status: "invalid", Error: "malformed JSON body"})
		return
	}
	if req.Solved == nil {
		writeJSON(w, http.StatusBadRequest, ingestResponse{Status: "invalid", Error: "solved is required"})
		return
	}

	sub := ingest.Submission{
		Source:      req.Source,
		ExternalID:  req.ExternalID,
		PlayerID:    req.PlayerID,
		Slug:        req.Slug,
		DisplayHint: req.DisplayHint,
		PuzzleNo:    req.PuzzleNo,
		Solved:      *req.Solved,
		Guesses:     req.Guesses,
		HardMode:    req.HardMode,
	}

	// A token holder posting a sender is the live path, so it may reactivate
	// a player who has evidently come back. Naming a player directly does not.
	status, err := ingest.Apply(r.Context(), s.db, store.TokenActor(token.ID), sub, true)
	switch {
	case errors.Is(err, ingest.ErrNoSuchPlayer):
		writeJSON(w, http.StatusNotFound, ingestResponse{Status: "not_found", Error: "no such player"})
		return
	case err != nil && isCallerError(err):
		writeJSON(w, http.StatusBadRequest, ingestResponse{Status: "invalid", Error: err.Error()})
		return
	case err != nil:
		s.logger.Error("ingest", "error", err)
		writeJSON(w, http.StatusInternalServerError, ingestResponse{Status: "error"})
		return
	}

	switch status {
	case ingest.StatusPending:
		writeJSON(w, http.StatusAccepted, ingestResponse{Status: string(status)})
	case ingest.StatusCreated:
		writeJSON(w, http.StatusCreated, ingestResponse{Status: string(status)})
	default:
		writeJSON(w, http.StatusOK, ingestResponse{Status: string(status)})
	}
}

// isCallerError distinguishes a malformed submission from a broken server.
// The ingest package returns plain errors for the former, so that a caller
// gets told what it got wrong rather than a bare 500.
func isCallerError(err error) bool {
	var validation *ingest.ValidationError
	return errors.As(err, &validation)
}

// authenticateToken reads and checks the bearer token.
func (s *Server) authenticateToken(r *http.Request) (store.APIToken, error) {
	header := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || value == "" {
		return store.APIToken{}, errors.New("missing bearer token")
	}
	return store.AuthenticateToken(r.Context(), s.db, strings.TrimSpace(value))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
