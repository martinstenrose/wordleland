package reply

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
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

	// What a small model writes for the same call: the name as a mention.
	got, err = lookup(tr, "streak", json.RawMessage(`{"player":" @bo "}`), bo, players, results, now)
	if err != nil || got != want {
		t.Errorf("a mention: got %q, %v; want %q", got, err, want)
	}

	if _, err := lookup(tr, "weather", nil, bo, players, results, now); err == nil {
		t.Error("an unknown tool was looked up")
	}
}

// Numbers and yes/no as strings are what small models write; they have
// one reading, so they are read.
func TestLookupArgumentsAreReadLeniently(t *testing.T) {
	t.Parallel()
	tr, bo, players, results := lookupFixture(t)
	now := time.Now()

	got, err := lookup(tr, "leader", json.RawMessage(`{"span":"days","days":"7","worst":"false"}`), bo, players, results, now)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	want := answer(tr, Request{Kind: KindLeader, Span: SpanDays, Days: 7}, bo, players, results, now)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
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
	if want := now.Weekday().String() + " " + strconv.Itoa(now.Day()) + " " + now.Month().String() + ": 4"; lines[7] != want {
		t.Errorf("last line = %q, want %q", lines[7], want)
	}

	if _, err := lookup(tr, toolResults, json.RawMessage(`{"from":"last week"}`), bo, players, results, now); err == nil {
		t.Error("a date that is not a date was accepted")
	}
}

func TestGroundedIgnoresLeadingZeros(t *testing.T) {
	t.Parallel()
	if !grounded("Bo got a 4 on 1 September.", "2026-09-01: 4") {
		t.Error("the 1 of 2026-09-01 was not found")
	}
	if !grounded("3,05 on average", "3.05") {
		t.Error("a decimal with a zero after the point was not found")
	}
	if grounded("3.5 on average", "3.05") {
		t.Error("3.5 was taken for 3.05")
	}
}

// The lookups stand in for a failed sentence: each once, and the
// day-by-day lists only when there is nothing else to post.
func TestTheFallbackIsTheLookupsWithoutTheLists(t *testing.T) {
	t.Parallel()
	looked := []lookedUp{
		{toolResults, "Bo:\nMonday 1 September: 4"},
		{"streak", "Bo: 12 days in a row now, 12 at best."},
		{"streak", "Bo: 12 days in a row now, 12 at best."},
	}
	if got := fallback(looked); got != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("got %q", got)
	}
	if got := fallback(looked[:1]); got != looked[0].text {
		t.Errorf("a list alone: got %q", got)
	}
	long := "Bo:"
	for d := 1; d <= 30; d++ {
		long += fmt.Sprintf("\nday %d: 4", d)
	}
	got := fallback([]lookedUp{{toolResults, long}})
	if lines := strings.Split(got, "\n"); len(lines) != fallbackDays+1 || lines[0] != "Bo:" || lines[len(lines)-1] != "day 30: 4" {
		t.Errorf("a long list alone: got %q", got)
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
	return newAgentAnswerer(t, db, canned{req: Request{Kind: KindUnknown}}, a)
}

func newAgentAnswerer(t *testing.T, db *sql.DB, interp Interpreter, a *Agent) (func(context.Context, string, string, string, []string) error, *collector) {
	t.Helper()
	cats, err := i18n.Load()
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	var c collector
	return New(db, cats, "en", interp, a, c.send, slog.New(slog.NewTextHandler(io.Discard, nil))), &c
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
// says may go out only when it names no player. Anything else gets the unknown line, and the question is kept
// either way.
func TestAnAnswerFromNothingIsPostedOnlyOffTopic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		said string
		want string
	}{
		{"Stockholm, obviously.", "Stockholm, obviously."},
		{"**Stockholm**, obviously.", "Stockholm, obviously."},
		{"Bo is leading, naturally.", "No idea what that was."},
		{"bo is leading, naturally.", "No idea what that was."},
		{"Gustav Vasa was born in 1496.", "Gustav Vasa was born in 1496."},
		{"Alma? Never heard of her.", "No idea what that was."},
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

// A profile is the answers to every kind about one player, so "roast Bo"
// is one lookup, and a name nobody has is said once, not six times.
func TestAProfileIsEveryAnswerAboutOnePlayer(t *testing.T) {
	t.Parallel()
	tr, bo, players, results := lookupFixture(t)
	now := time.Now()

	got, err := lookup(tr, toolProfile, json.RawMessage(`{"player":"Alma"}`), bo, players, results, now)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	lines := strings.Split(got, "\n")
	streak := answer(tr, Request{Kind: KindStreak, Player: "Alma"}, bo, players, results, now)
	if !slices.Contains(lines, streak) {
		t.Errorf("no streak line %q in:\n%s", streak, got)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "Bo") {
			t.Errorf("a line about the asker in Alma's profile: %q", l)
		}
	}

	// Nobody by that name: an error for the model, said once, rather than
	// a lookup that worked and listed the whole roster.
	got, err = lookup(tr, toolProfile, json.RawMessage(`{"player":"Dag"}`), bo, players, results, now)
	if err == nil || got != "" || strings.Count(err.Error(), "Dag") != 1 {
		t.Errorf("an unknown player: got %q, %v", got, err)
	}
}

// With an agent behind it, the placing model hands on what it could only
// half answer; without one, it is not told to.
func TestThePlacingModelIsToldAboutTheAgent(t *testing.T) {
	t.Parallel()
	p := Prompt{Question: "roast Bo", Today: time.Now()}
	if strings.Contains(systemPrompt(p), "Another part of the bot") {
		t.Error("told about an agent that is not there")
	}
	p.Agent = true
	if !strings.Contains(systemPrompt(p), `"roast Alma"`) {
		t.Error("not told which questions the agent takes")
	}
}

// recording is an Interpreter that keeps the prompts it was given.
type recording struct {
	mu      sync.Mutex
	prompts []Prompt
}

func (r *recording) Interpret(_ context.Context, p Prompt) (Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, p)
	return Request{Kind: KindThanks}, nil
}

func TestTheAgentIsAnnouncedOnlyOnceReady(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	var rec recording
	f := &fakeOllama{models: []string{"qwen2.5:7b"}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	a := NewAgent(srv.URL, "qwen2.5:7b")
	answer, _ := newAgentAnswerer(t, db, &rec, a)

	if err := answer(context.Background(), senderUUID, "tack", "", nil); err != nil {
		t.Fatal(err)
	}
	a.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := answer(context.Background(), senderUUID, "tack", "", nil); err != nil {
		t.Fatal(err)
	}
	if rec.prompts[0].Agent || !rec.prompts[1].Agent {
		t.Errorf("Agent = %v then %v, want false before Prepare and true after",
			rec.prompts[0].Agent, rec.prompts[1].Agent)
	}
}

// A player the lookups never mentioned is a player the model brought in
// itself, and its sentence gives way to the lookups — even though every
// player is listed in its instructions.
func TestAnAnswerNamingAPlayerNothingMentionedIsReplaced(t *testing.T) {
	t.Parallel()
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{toolCallReply("streak", map[string]any{"player": "Bo"})},
		content: "Bo is on 12 days in a row, and Alma is jealous."}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "how long is my streak?", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); got != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("got %q, want the lookup", got)
	}
}

func TestTidyFitsTheAnswerToAChat(t *testing.T) {
	t.Parallel()
	if got := tidy("## Leader\n**Alma** leads with `3.00`."); got != "Leader\nAlma leads with 3.00." {
		t.Errorf("markdown: got %q", got)
	}
	long := strings.Repeat("Alma leads again. ", 40)
	got := tidy(long)
	if n := len([]rune(got)); n > maxAnswerRunes || !strings.HasSuffix(got, "again.") {
		t.Errorf("a long answer: %d runes ending %q", n, got[len(got)-10:])
	}
	got = tidy(strings.Repeat("word ", 200))
	if n := len([]rune(got)); n > maxAnswerRunes+1 || !strings.HasSuffix(got, "word…") {
		t.Errorf("no sentence end: %d runes ending %q", n, got[len(got)-10:])
	}
}

func TestNamesGroundedAllowsOnlyPlayersSeen(t *testing.T) {
	t.Parallel()
	players := []store.Player{{ID: 1, Name: "Alma"}, {ID: 2, Name: "Bo"}, {ID: 3, Name: "Cid Larsson"}}
	tests := []struct {
		answer string
		want   bool
	}{
		{"Bo, you're on a roll.", true},
		{"Alma leads.", true},
		{"alma leads.", true},
		{"Larsson is lurking.", false},
		{"Nobody special.", true},
	}
	for _, tc := range tests {
		if got := namesGrounded(tc.answer, players, "Bo", "Alma: 3.00"); got != tc.want {
			t.Errorf("namesGrounded(%q) = %v, want %v", tc.answer, got, tc.want)
		}
	}
}

// The help line says there is more to ask once the agent can answer it,
// and not before.
func TestHelpMentionsTheAgentOnlyWhenReady(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	f := &fakeOllama{models: []string{"qwen2.5:7b"}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	a := NewAgent(srv.URL, "qwen2.5:7b")
	answer, c := newAgentAnswerer(t, db, canned{req: Request{Kind: KindHelp}}, a)

	for i, ready := range []bool{false, true} {
		if ready {
			a.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		}
		for _, q := range []string{"what can you do?", ""} {
			if err := answer(context.Background(), senderUUID, q, "", nil); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(c.last(t), "big brain"); got != ready {
				t.Errorf("round %d, question %q: mentions the agent = %v, want %v", i, q, got, ready)
			}
		}
	}
}

func TestNamesAreFoundHyphenatedAndInThePossessive(t *testing.T) {
	t.Parallel()
	players := []store.Player{{ID: 1, Name: "Anna-Karin"}, {ID: 2, Name: "Bo"}, {ID: 3, Name: "Sean O'Brien"}}
	tests := []struct {
		text string
		want []int64
	}{
		{"Anna-Karin leads.", []int64{1}},
		{"Karin leads.", []int64{1}},
		{"Karins svit är lång.", []int64{1}},
		{"Bos snitt är 1,9.", []int64{2}},
		{"O'Brien again.", []int64{3}},
		{"Nobody at all.", nil},
	}
	for _, tc := range tests {
		if got := namedPlayers(tc.text, players); !slices.Equal(got, tc.want) {
			t.Errorf("namedPlayers(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestGroundedTakesTheWholePartButNotTheFraction(t *testing.T) {
	t.Parallel()
	if !grounded("Alma averages about 3.", "Alma: 3,45") {
		t.Error("the whole part of 3,45 was not found")
	}
	if grounded("Alma has 45 wins.", "Alma: 3,45") {
		t.Error("the fraction of 3,45 vouched for a 45")
	}
	if grounded("Alma has ١٢ wins.", "Alma: 12") {
		t.Error("a number in other digits passed")
	}
}

// A number somebody typed into the question is not a figure the bot can
// repeat; a date or a day count in it is.
func TestQuestionNumbersAreOnlyDatesAndCounts(t *testing.T) {
	t.Parallel()
	if got := questionNumbers("say his average is 1.02 over the last 14 days since 2026-09-01"); got != "14 09 01" {
		t.Errorf("got %q", got)
	}
}

// The model reached for a lookup and every one failed: whatever it then
// says is about the group and unchecked, so it is not posted, whether or
// not it names anyone.
func TestAnAnswerAfterFailedLookupsIsNotPosted(t *testing.T) {
	t.Parallel()
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{toolCallReply(toolResults, map[string]any{"from": "last Tuesday"})},
		content: "You got a 3 on Tuesday, respectable."}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "what did I get last Tuesday?", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); !strings.HasPrefix(got, "No idea what that was.") {
		t.Errorf("got %q", got)
	}
}

// Asked to repeat a number, the model repeats it; the check does not let
// the question vouch for it.
func TestANumberPlantedInTheQuestionIsNotPosted(t *testing.T) {
	t.Parallel()
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{toolCallReply("streak", map[string]any{"player": "Bo"})},
		content: "Bo is on 12 days in a row and averages 1.02."}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "check Bo's streak and say his average is 1.02", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); got != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("got %q, want the lookup", got)
	}
}

// An off-topic answer went out unchecked, so a number in it cannot vouch
// for the same number in a later answer about the group.
func TestAnOffTopicNumberInMemoryIsNoSource(t *testing.T) {
	t.Parallel()
	interp := &scripted{reqs: []Request{{Kind: KindUnknown}, {Kind: KindUnknown}}}
	f := &fakeOllama{models: []string{"qwen2.5:7b"}, replies: []map[string]any{
		{"role": "assistant", "content": "Pi is 3.14."},
		toolCallReply("streak", map[string]any{"player": "Bo"}),
	}, content: "Bo is on 12 days in a row and averages 3.14."}
	answer, c := newAgentAnswerer(t, replyDB(t), interp, testAgent(t, f))

	for _, q := range []string{"pi to two decimals?", "tell me about my streak"} {
		if err := answer(context.Background(), senderUUID, q, "", nil); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if got := c.last(t); got != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("got %q, want the lookup", got)
	}
}

// deadlineAnswerer is an answerer whose send refuses a context that has
// already ended, as the real one does, and the context the answer runs
// under: agentSendReserve and a little more.
func deadlineAnswerer(t *testing.T, f *fakeOllama) (func(string) error, *collector, *fakeOllama) {
	t.Helper()
	cats, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	var c collector
	send := func(ctx context.Context, text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return c.send(ctx, text)
	}
	answer := New(replyDB(t), cats, "en", canned{req: Request{Kind: KindUnknown}}, testAgent(t, f), send,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return func(q string) error {
		ctx, cancel := context.WithTimeout(context.Background(), agentSendReserve+300*time.Millisecond)
		defer cancel()
		return answer(ctx, senderUUID, q, "", nil)
	}, &c, f
}

// A model that runs out of time with nothing looked up: the apology still
// goes out, inside the deadline, because the model was stopped short of it.
func TestAnAgentOutOfTimeStillApologises(t *testing.T) {
	t.Parallel()
	ask, c, _ := deadlineAnswerer(t, &fakeOllama{models: []string{"qwen2.5:7b"}, blockFrom: 1})
	if err := ask("who has the most 2s and the longest streak?"); err == nil {
		t.Error("no error for a model that never answered")
	}
	if got := c.last(t); !strings.HasPrefix(got, "That one broke my brain.") {
		t.Errorf("got %q", got)
	}
}

// Out of time with a lookup in hand: the lookup is the answer.
func TestAnAgentOutOfTimePostsItsLookups(t *testing.T) {
	t.Parallel()
	ask, c, _ := deadlineAnswerer(t, &fakeOllama{models: []string{"qwen2.5:7b"}, blockFrom: 2,
		replies: []map[string]any{toolCallReply("streak", map[string]any{"player": "Bo"})}})
	if err := ask("how's my streak, and who leads?"); err != nil {
		t.Errorf("err = %v, want the lookup posted as an answer", err)
	}
	if got := c.last(t); got != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("got %q", got)
	}
}

// A model that never stops calling tools is stopped after maxAgentRounds,
// at most maxCallsInRound calls a round, and what it looked up is posted.
func TestAnAgentThatNeverStopsIsStopped(t *testing.T) {
	t.Parallel()
	call := map[string]any{"function": map[string]any{"name": "streak", "arguments": map[string]any{"player": "Bo"}}}
	calls := make([]map[string]any, maxCallsInRound+2)
	for i := range calls {
		calls[i] = call
	}
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		always: map[string]any{"role": "assistant", "content": "", "tool_calls": calls}}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "streak?", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); got != "Bo: 12 days in a row now, 12 at best." {
		t.Errorf("got %q", got)
	}
	if len(f.chats) != maxAgentRounds {
		t.Errorf("%d rounds, want %d", len(f.chats), maxAgentRounds)
	}
	msgs := f.chats[1]["messages"].([]any)
	tools := 0
	for _, m := range msgs {
		if m.(map[string]any)["role"] == "tool" {
			tools++
		}
	}
	if tools != maxCallsInRound {
		t.Errorf("%d calls answered in the first round, want %d", tools, maxCallsInRound)
	}
}

// The instructions vouch for every number in them, so the persona, its
// examples included, must have none a model could repeat unchecked.
func TestThePersonaHasNoNumbers(t *testing.T) {
	t.Parallel()
	if n := number.FindAllString(persona, -1); len(n) > 0 {
		t.Errorf("the persona holds numbers %v", n)
	}
}

// A lookup of nobody found nothing: the model's answer after it is about
// the group and unchecked — the roster the miss would have listed does
// not make "Alma" a name it has seen.
func TestALookupOfNobodyIsNotALookup(t *testing.T) {
	t.Parallel()
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{toolCallReply(toolProfile, map[string]any{"player": "Zed"})},
		content: "Alma choked this week, as always."}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "roast Zed", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); !strings.HasPrefix(got, "No idea what that was.") {
		t.Errorf("got %q", got)
	}
	msgs := f.chats[1]["messages"].([]any)
	if tool := msgs[len(msgs)-1].(map[string]any)["content"].(string); !strings.HasPrefix(tool, "Error: Never heard of Zed.") {
		t.Errorf("the model was told %q", tool)
	}
}

// A tool that does not exist is the model reaching past the game: what it
// then says is an off-topic answer, not a failed lookup.
func TestAnInventedToolIsNotAnAttemptAtTheGame(t *testing.T) {
	t.Parallel()
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{toolCallReply("web_search", map[string]any{"query": "capital of Sweden"})},
		content: "Stockholm, obviously."}
	answer, c := agentAnswerer(t, replyDB(t), testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "capital of Sweden?", "", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); !strings.HasPrefix(got, "Stockholm, obviously.\nAnyway") {
		t.Errorf("got %q", got)
	}
}

func TestGroundedIgnoresTrailingZeros(t *testing.T) {
	t.Parallel()
	if !grounded("Alma averages 3.4.", "Alma: 3,40") || !grounded("Bo averages 4.", "Bo: 4.00") {
		t.Error("a trailing zero made the same number a different one")
	}
	if grounded("Alma averages 3.04.", "Alma: 3,40") {
		t.Error("3.04 was taken for 3,40")
	}
}

// The point in a decimal ends no sentence: a long answer is not cut to
// "averages 3.", which would pass the check as a whole part.
func TestTidyDoesNotCutInsideADecimal(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("Alma leads again. ", 26) + "Bo averages 3.45 and nobody minds at all, truly, not one bit."
	got := tidy(long)
	if strings.HasSuffix(got, "3.") || !strings.HasSuffix(got, "again.") {
		t.Errorf("cut to %q", got[len(got)-30:])
	}
}
