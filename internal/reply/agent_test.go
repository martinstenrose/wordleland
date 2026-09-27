package reply

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
)

func TestGroundedAcceptsOnlyNumbersFromTheSources(t *testing.T) {
	t.Parallel()
	sources := []string{"Bo: 12 days in a row now, 12 at best.", "Alma leads with 3,45 on average."}
	tests := []struct {
		answer string
		want   bool
	}{
		{"Bo has solved 12 days in a row.", true},
		{"Alma leads, averaging 3.45.", true},
		{"Alma leads, averaging 3,45.", true},
		{"Nobody has a streak worth mentioning.", true},
		{"Bo has solved 13 days in a row.", false},
		{"Alma leads, averaging 3.40.", false},
		{"Bo's streak is 121 days.", false},
	}
	for _, tc := range tests {
		if got := grounded(tc.answer, sources...); got != tc.want {
			t.Errorf("grounded(%q) = %v, want %v", tc.answer, got, tc.want)
		}
	}
}

// lookupFixture is the reply fixture's players and history, as a lookup
// is handed them.
func lookupFixture(t *testing.T) (i18n.Translator, *store.Player, []store.Player, []store.BoardResult) {
	t.Helper()
	db := replyDB(t)
	ctx := context.Background()
	players, err := store.ListPlayers(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.ResultsForBoard(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	bo, _, err := store.ResolveIdentity(ctx, db, "signal", senderUUID)
	if err != nil {
		t.Fatal(err)
	}
	cats, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	return i18n.NewTranslator(cats, "en"), &bo, players, results
}

// A lookup of a Kind is the answer a placed question of that Kind gets.
func TestALookupIsTheBotsOwnAnswer(t *testing.T) {
	t.Parallel()
	tr, bo, players, results := lookupFixture(t)
	now := time.Now()

	got, err := lookup(tr, "streak", json.RawMessage(`{"player":"Bo"}`), bo, players, results, now)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	want := answer(tr, Request{Kind: KindStreak, Span: SpanMonth, Player: "Bo"}, bo, players, results, now)
	if got != want {
		t.Errorf("lookup = %q, want the streak answer %q", got, want)
	}

	if _, err := lookup(tr, "weather", nil, bo, players, results, now); err == nil {
		t.Error("an unknown tool was looked up")
	}
}

func TestTheResultsLookupListsDays(t *testing.T) {
	t.Parallel()
	tr, bo, players, results := lookupFixture(t)
	now := time.Now()

	got, err := lookup(tr, toolResults, json.RawMessage(`{}`), bo, players, results, now)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	lines := strings.Split(got, "\n")
	// The asker, the last seven days by default, each a 4 in the fixture.
	if lines[0] != "Bo:" || len(lines) != 8 {
		t.Fatalf("got %q", got)
	}
	if want := now.Format(DateLayout) + " " + now.Weekday().String() + ": 4"; lines[7] != want {
		t.Errorf("last line = %q, want %q", lines[7], want)
	}

	if _, err := lookup(tr, toolResults, json.RawMessage(`{"from":"last week"}`), bo, players, results, now); err == nil {
		t.Error("a date that is not a date was accepted")
	}
}

func testAgent(t *testing.T, f *fakeOllama) *Agent {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	a := NewAgent(srv.URL, "qwen2.5:7b")
	a.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return a
}

// toolCallReply is the model asking for one lookup.
func toolCallReply(name string, args map[string]any) map[string]any {
	return map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{
		{"function": map[string]any{"name": name, "arguments": args}},
	}}
}

func agentAnswerer(t *testing.T, db *sql.DB, a *Agent) (func(context.Context, string, string, string, []string) error, *collector) {
	t.Helper()
	cats, err := i18n.Load()
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	var c collector
	return New(db, cats, "en", canned{req: Request{Kind: KindUnknown}}, a, c.send,
		slog.New(slog.NewTextHandler(io.Discard, nil))), &c
}

// The model looks the streak up, is handed the bot's answer, and phrases
// its own from it — which, grounded, is what the group gets.
func TestTheAgentAnswersFromWhatItLookedUp(t *testing.T) {
	t.Parallel()
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{toolCallReply("streak", map[string]any{"player": "Bo"})},
		content: "Bo is on 12 days in a row, his best yet."}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "is my streak my best ever?", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); got != "Bo is on 12 days in a row, his best yet." {
		t.Errorf("got %q", got)
	}

	if len(f.chats) != 2 {
		t.Fatalf("%d chat calls, want 2", len(f.chats))
	}
	if _, ok := f.chats[0]["tools"].([]any); !ok {
		t.Errorf("no tools offered: %v", f.chats[0]["tools"])
	}
	msgs := f.chats[1]["messages"].([]any)
	if system := msgs[0].(map[string]any)["content"].(string); !strings.Contains(system, "Never tease anyone for not playing") {
		t.Errorf("the system prompt lacks the persona's limits:\n%s", system)
	}
	tool := msgs[len(msgs)-1].(map[string]any)
	if tool["role"] != "tool" || tool["content"] != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("the lookup was not handed back: %v", tool)
	}
}

// A number the lookups did not give is the model's own, and the group gets
// the lookups instead.
func TestAnUngroundedAnswerIsReplacedByTheLookups(t *testing.T) {
	t.Parallel()
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{toolCallReply("streak", map[string]any{"player": "Bo"})},
		content: "Bo is on 40 days in a row."}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "how long is my streak?", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); got != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("got %q, want the lookup", got)
	}
}

// Without a lookup the model knows nothing about the group, so what it
// says may go out only when it is about something else: no player, no
// number. Anything else gets the unknown line, and the question is kept
// either way.
func TestAnAnswerFromNothingIsPostedOnlyOffTopic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		said string
		want string
	}{
		{"Stockholm, obviously.", "Stockholm, obviously."},
		{"Bo is leading, naturally.", "No idea what that was."},
		{"bo is leading, naturally.", "No idea what that was."},
		{"The leader averages 3.4.", "No idea what that was."},
		{"Gustav Vasa was born in 1496.", "No idea what that was."},
		{"", "No idea what that was."},
	}
	for _, tc := range tests {
		db := replyDB(t)
		f := &fakeOllama{models: []string{"qwen2.5:7b"}, content: tc.said}
		answer, c := agentAnswerer(t, db, testAgent(t, f))

		if err := answer(context.Background(), senderUUID, "a question", "", nil); err != nil {
			t.Fatalf("%q: answer: %v", tc.said, err)
		}
		if got := c.last(t); !strings.HasPrefix(got, tc.want) {
			t.Errorf("model said %q; posted %q, want %q", tc.said, got, tc.want)
		}
		var kept int
		if err := db.QueryRow(`SELECT COUNT(*) FROM unanswered_questions`).Scan(&kept); err != nil {
			t.Fatalf("count kept: %v", err)
		}
		if kept != 1 {
			t.Errorf("%q: %d questions kept, want 1", tc.said, kept)
		}
	}
}
