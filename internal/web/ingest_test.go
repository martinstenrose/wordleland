package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

const senderUUID = "11111111-2222-3333-4444-555555555555"

// ingestFixture returns a server, a valid token, and a player.
func ingestFixture(t *testing.T) (*Server, string, store.Player, int64) {
	t.Helper()

	srv := testServer(t)
	ctx := context.Background()

	admin, err := store.CreateUser(ctx, srv.db, store.SystemActor(), "admin@example.tld", "hash", true)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	actor := store.AdminActor(admin.ID)

	player, err := store.CreatePlayer(ctx, srv.db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	token, _, err := store.CreateAPIToken(ctx, srv.db, actor, "import-script", nil)
	if err != nil {
		t.Fatalf("CreateAPIToken() failed: %v", err)
	}
	return srv, token, player, admin.ID
}

// postIngest posts a body with the given bearer token.
func postIngest(t *testing.T, srv *Server, token string, body any) (*httptest.ResponseRecorder, ingestResponse) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(encoded))
	req.RemoteAddr = clientAddr(t)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var decoded ingestResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return rec, decoded
}

func senderBody(puzzle, guesses int) map[string]any {
	return map[string]any{
		"source": "signal", "external_id": senderUUID, "display_hint": "Someone",
		"puzzle_no": puzzle, "solved": true, "guesses": guesses,
	}
}

func TestIngestRejectsBadToken(t *testing.T) {
	srv, valid, _, _ := ingestFixture(t)

	for _, tc := range []struct{ name, token string }{
		{"missing", ""},
		{"unknown", "not-a-real-token"},
		{"empty bearer", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := postIngest(t, srv, tc.token, senderBody(1890, 4))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}

	// The valid one still works, so the test is not passing by accident.
	if rec, _ := postIngest(t, srv, valid, senderBody(1890, 4)); rec.Code == http.StatusUnauthorized {
		t.Error("a valid token was rejected")
	}
}

func TestIngestRejectsRevokedAndExpiredTokens(t *testing.T) {
	srv, _, _, adminID := ingestFixture(t)
	ctx := context.Background()
	actor := store.AdminActor(adminID)

	revoked, tokenRow, err := store.CreateAPIToken(ctx, srv.db, actor, "revoked", nil)
	if err != nil {
		t.Fatalf("CreateAPIToken() failed: %v", err)
	}
	if err := store.RevokeAPIToken(ctx, srv.db, actor, tokenRow.ID); err != nil {
		t.Fatalf("RevokeAPIToken() failed: %v", err)
	}
	if rec, _ := postIngest(t, srv, revoked, senderBody(1890, 4)); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	past := timeInPast()
	expired, _, err := store.CreateAPIToken(ctx, srv.db, actor, "expired", &past)
	if err != nil {
		t.Fatalf("CreateAPIToken() failed: %v", err)
	}
	if rec, _ := postIngest(t, srv, expired, senderBody(1890, 4)); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// The ingest response matrix, which exists so a caller need not infer the outcome.
func TestIngestResponseMatrix(t *testing.T) {
	srv, token, player, adminID := ingestFixture(t)
	ctx := context.Background()

	if _, err := store.LinkIdentity(ctx, srv.db, store.AdminActor(adminID), player.ID,
		"signal", senderUUID, store.ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	// 201 created.
	rec, body := postIngest(t, srv, token, senderBody(1890, 4))
	if rec.Code != http.StatusCreated || body.Status != "created" {
		t.Errorf("new row: status %d/%q, want 201/created", rec.Code, body.Status)
	}

	// 200 updated: a token may correct its own earlier write.
	rec, body = postIngest(t, srv, token, senderBody(1890, 3))
	if rec.Code != http.StatusOK || body.Status != "updated" {
		t.Errorf("overwrite: status %d/%q, want 200/updated", rec.Code, body.Status)
	}

	// 200 ignored: the precedence rule refuses a token over a human value.
	if _, _, err := store.UpsertResult(ctx, srv.db, store.Result{
		PuzzleNo: 1891, Date: mustDate(t, 1891), PlayerID: player.ID,
		Guesses: intPtr(2), Solved: true,
	}, &adminID); err != nil {
		t.Fatalf("human write failed: %v", err)
	}
	rec, body = postIngest(t, srv, token, senderBody(1891, 5))
	if rec.Code != http.StatusOK || body.Status != "ignored" {
		t.Errorf("refused: status %d/%q, want 200/ignored", rec.Code, body.Status)
	}
}

// The heart of the split: an unclaimed sender is 202, because the payload is
// held and becomes real once claimed. Reading it as an error would train
// whoever watches this endpoint to ignore it.
func TestIngestHoldsUnclaimedSender(t *testing.T) {
	srv, token, _, _ := ingestFixture(t)

	rec, body := postIngest(t, srv, token, map[string]any{
		"source": "signal", "external_id": senderUUID, "display_hint": "Someone",
		"puzzle_no": 1890, "solved": true, "guesses": 4, "hard_mode": true,
	})
	if rec.Code != http.StatusAccepted || body.Status != "pending" {
		t.Fatalf("status = %d/%q, want 202/pending", rec.Code, body.Status)
	}

	// The full payload is held, not merely a sighting.
	held, hint, err := store.PendingResultsFor(context.Background(), srv.db, "signal", senderUUID)
	if err != nil {
		t.Fatalf("read held results: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1", len(held))
	}
	if held[0].PuzzleNo != 1890 || !held[0].Solved || *held[0].Guesses != 4 || !held[0].HardMode {
		t.Errorf("held = %+v, want the whole payload", held[0])
	}
	if hint != "Someone" {
		t.Errorf("display hint = %q, want it recorded for the admin to recognise", hint)
	}
}

// A bad slug is a caller error, so it is 404 and stores nothing — the
// distinction the split exists to draw.
func TestIngestNamedPlayerMissIs404AndStoresNothing(t *testing.T) {
	srv, token, _, _ := ingestFixture(t)

	rec, body := postIngest(t, srv, token, map[string]any{
		"slug": "no-such-player", "puzzle_no": 1890, "solved": true, "guesses": 4,
	})
	if rec.Code != http.StatusNotFound || body.Status != "not_found" {
		t.Errorf("status = %d/%q, want 404/not_found", rec.Code, body.Status)
	}

	var held int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM pending_results`).Scan(&held); err != nil {
		t.Fatalf("count held: %v", err)
	}
	if held != 0 {
		t.Errorf("held = %d for a mistyped slug, want 0", held)
	}
}

func TestIngestByPlayerIDAndSlug(t *testing.T) {
	srv, token, player, _ := ingestFixture(t)

	rec, _ := postIngest(t, srv, token, map[string]any{
		"player_id": player.ID, "puzzle_no": 1890, "solved": true, "guesses": 4,
	})
	if rec.Code != http.StatusCreated {
		t.Errorf("player_id: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	rec, _ = postIngest(t, srv, token, map[string]any{
		"slug": "martin", "puzzle_no": 1891, "solved": false,
	})
	if rec.Code != http.StatusCreated {
		t.Errorf("slug: status = %d, want %d", rec.Code, http.StatusCreated)
	}
}

func TestIngestRejectsMalformedBodies(t *testing.T) {
	srv, token, player, _ := ingestFixture(t)

	tests := []struct {
		name string
		body map[string]any
	}{
		{"no identity", map[string]any{"puzzle_no": 1890, "solved": true, "guesses": 4}},
		{"two identities", map[string]any{
			"slug": "martin", "player_id": player.ID, "puzzle_no": 1890, "solved": true, "guesses": 4}},
		{"source without external_id", map[string]any{
			"source": "signal", "puzzle_no": 1890, "solved": true, "guesses": 4}},
		{"missing solved", map[string]any{"slug": "martin", "puzzle_no": 1890}},
		{"solved without guesses", map[string]any{"slug": "martin", "puzzle_no": 1890, "solved": true}},
		{"failed with guesses", map[string]any{
			"slug": "martin", "puzzle_no": 1890, "solved": false, "guesses": 3}},
		{"guesses too high", map[string]any{
			"slug": "martin", "puzzle_no": 1890, "solved": true, "guesses": 7}},
		{"guesses too low", map[string]any{
			"slug": "martin", "puzzle_no": 1890, "solved": true, "guesses": 0}},
		{"puzzle zero", map[string]any{"slug": "martin", "puzzle_no": 0, "solved": true, "guesses": 3}},
		{"negative puzzle", map[string]any{"slug": "martin", "puzzle_no": -5, "solved": true, "guesses": 3}},
		{"unknown field", map[string]any{
			"slug": "martin", "puzzle_no": 1890, "solved": true, "guesses": 3, "sourceNumber": "+00000000000"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, body := postIngest(t, srv, token, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if body.Error == "" {
				t.Error("no explanation of what was wrong")
			}
		})
	}
}

// Unknown fields are refused rather than ignored. The bridge must never
// send source or sourceNumber, and silently accepting them would let
// that slip in unnoticed.
func TestIngestRejectsPhoneNumberFields(t *testing.T) {
	srv, token, _, _ := ingestFixture(t)

	rec, _ := postIngest(t, srv, token, map[string]any{
		"source": "signal", "external_id": senderUUID, "sourceNumber": "+00000000000",
		"puzzle_no": 1890, "solved": true, "guesses": 4,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want a phone number field refused", rec.Code)
	}
}

// Posting again is evidence of return, but only from a live post.
func TestIngestReactivatesPlayerWhoPostsAgain(t *testing.T) {
	srv, token, player, adminID := ingestFixture(t)
	ctx := context.Background()
	actor := store.AdminActor(adminID)

	if _, err := store.LinkIdentity(ctx, srv.db, actor, player.ID,
		"signal", senderUUID, store.ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if _, err := store.UpdatePlayer(ctx, srv.db, actor, player.ID,
		store.PlayerUpdate{Active: boolPtr(false)}); err != nil {
		t.Fatalf("UpdatePlayer() failed: %v", err)
	}

	if rec, _ := postIngest(t, srv, token, senderBody(1890, 4)); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	reloaded, err := store.PlayerByID(ctx, srv.db, player.ID)
	if err != nil {
		t.Fatalf("PlayerByID() failed: %v", err)
	}
	if !reloaded.Active {
		t.Error("the player was not reactivated by posting again")
	}

	// Visible rather than silent.
	var count int
	if err := srv.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`,
		store.ActionPlayerReactivated).Scan(&count); err != nil {
		t.Fatalf("count audit entries: %v", err)
	}
	if count != 1 {
		t.Errorf("reactivation audit entries = %d, want 1", count)
	}
}

// Naming a player directly is an admin or a script, which says nothing about
// whether that person has rejoined the group.
func TestIngestByNamedPlayerDoesNotReactivate(t *testing.T) {
	srv, token, player, adminID := ingestFixture(t)
	ctx := context.Background()

	if _, err := store.UpdatePlayer(ctx, srv.db, store.AdminActor(adminID), player.ID,
		store.PlayerUpdate{Active: boolPtr(false)}); err != nil {
		t.Fatalf("UpdatePlayer() failed: %v", err)
	}

	if rec, _ := postIngest(t, srv, token, map[string]any{
		"slug": "martin", "puzzle_no": 1890, "solved": true, "guesses": 4,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	reloaded, err := store.PlayerByID(ctx, srv.db, player.ID)
	if err != nil {
		t.Fatalf("PlayerByID() failed: %v", err)
	}
	if reloaded.Active {
		t.Error("naming a player directly reactivated them")
	}
}

// The audit entry carries the previous value, which is what makes the log a
// correction trail rather than a list of events.
func TestIngestAuditsWithPreviousValue(t *testing.T) {
	srv, token, player, adminID := ingestFixture(t)
	ctx := context.Background()

	if _, err := store.LinkIdentity(ctx, srv.db, store.AdminActor(adminID), player.ID,
		"signal", senderUUID, store.ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}

	postIngest(t, srv, token, senderBody(1890, 5))
	postIngest(t, srv, token, senderBody(1890, 3))

	var detail string
	if err := srv.db.QueryRow(
		`SELECT detail FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`,
		store.ActionResultUpdated).Scan(&detail); err != nil {
		t.Fatalf("read audit detail: %v", err)
	}
	if !bytes.Contains([]byte(detail), []byte(`"previous"`)) {
		t.Errorf("the overwrite was logged without the value it replaced: %s", detail)
	}

	// The actor is the token, not a user.
	var kind string
	if err := srv.db.QueryRow(
		`SELECT actor_kind FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`,
		store.ActionResultUpdated).Scan(&kind); err != nil {
		t.Fatalf("read actor: %v", err)
	}
	if kind != store.ActorToken {
		t.Errorf("actor_kind = %q, want %q", kind, store.ActorToken)
	}
}

// A repost of the same puzzle overwrites what is held rather than
// accumulating a second row.
func TestIngestRepostOverwritesHeldResult(t *testing.T) {
	srv, token, _, _ := ingestFixture(t)

	postIngest(t, srv, token, senderBody(1890, 5))
	postIngest(t, srv, token, senderBody(1890, 3))

	held, _, err := store.PendingResultsFor(context.Background(), srv.db, "signal", senderUUID)
	if err != nil {
		t.Fatalf("read held results: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("held = %d, want 1", len(held))
	}
	if *held[0].Guesses != 3 {
		t.Errorf("guesses = %d, want the repost to have won", *held[0].Guesses)
	}
}

// The whole point of holding payloads: claiming recovers everything.
func TestIngestThenClaimReplaysHeldResults(t *testing.T) {
	srv, token, player, adminID := ingestFixture(t)
	ctx := context.Background()

	postIngest(t, srv, token, senderBody(1888, 4))
	postIngest(t, srv, token, senderBody(1889, 3))
	postIngest(t, srv, token, senderBody(1890, 5))

	summary, err := store.LinkIdentity(ctx, srv.db, store.AdminActor(adminID), player.ID,
		"signal", senderUUID, store.ActionIdentityClaimed, false)
	if err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if summary.Replayed != 3 {
		t.Errorf("replayed = %d, want 3", summary.Replayed)
	}

	for _, puzzle := range []int{1888, 1889, 1890} {
		stored, err := store.ResultFor(ctx, srv.db, puzzle, player.ID)
		if err != nil {
			t.Errorf("puzzle %d was not replayed: %v", puzzle, err)
			continue
		}
		if stored.EnteredBy != nil {
			t.Errorf("puzzle %d has entered_by set, want NULL", puzzle)
		}
	}

	// And a later post from the same sender now resolves normally.
	if rec, body := postIngest(t, srv, token, senderBody(1891, 2)); rec.Code != http.StatusCreated {
		t.Errorf("after claiming: status %d/%q, want 201/created", rec.Code, body.Status)
	}
}
