package reply

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheConversationRemembersTheLastFewTurns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	var c conversation
	for i := range 6 {
		c.add(turn{at: now.Add(time.Duration(i-6) * time.Minute), question: string(rune('a' + i))})
	}
	got := c.recent(now)
	if len(got) != maxTurns || got[0].question != "c" || got[len(got)-1].question != "f" {
		t.Errorf("recent = %+v, want the last %d, oldest first", got, maxTurns)
	}
	if got := c.recent(now.Add(memoryWindow + time.Minute)); len(got) != 0 {
		t.Errorf("remembered %d turns after a quarter of an hour of quiet", len(got))
	}
}

// A conversation that keeps going is one conversation: turns a few
// minutes apart are remembered together even when the first is more than
// a window old.
func TestTheConversationLastsWhileItGoesOn(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, time.September, 27, 19, 0, 0, 0, time.UTC)
	var c conversation
	c.add(turn{at: start, question: "first"})
	c.add(turn{at: start.Add(14 * time.Minute), question: "second"})
	if got := c.recent(start.Add(16 * time.Minute)); len(got) != 2 {
		t.Errorf("remembered %d turns, want 2", len(got))
	}
}

// scripted is an Interpreter that answers with its requests in turn.
type scripted struct {
	mu   sync.Mutex
	reqs []Request
}

func (s *scripted) Interpret(context.Context, Prompt) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.reqs[0]
	s.reqs = s.reqs[1:]
	return r, nil
}

// Off-topic is answered every time, however many in a row, each with a
// line back to the game; and a question about the game is answered as it
// always is.
func TestOffTopicIsAlwaysAnsweredAndSteeredBack(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	unknown := Request{Kind: KindUnknown}
	interp := &scripted{reqs: []Request{unknown, unknown, unknown, unknown, {Kind: KindStreak, Player: "Bo"}}}
	stockholm := map[string]any{"role": "assistant", "content": "Stockholm, obviously."}
	f := &fakeOllama{models: []string{"qwen2.5:7b"},
		replies: []map[string]any{stockholm, stockholm, stockholm, stockholm},
		content: "Bo is on 12 days in a row. Somebody stop him."}
	answer, c := newAgentAnswerer(t, db, interp, testAgent(t, f))

	ask := func(q string) string {
		t.Helper()
		if err := answer(context.Background(), senderUUID, q, "", nil); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return c.last(t)
	}
	for i, q := range []string{"hej!", "capital of Sweden?", "capital of Norway?", "capital of Denmark?"} {
		got := ask(q)
		first, steer, ok := strings.Cut(got, "\n")
		if !ok || first != "Stockholm, obviously." || !strings.HasPrefix(steer, "Anyway") {
			t.Errorf("off-topic %d: got %q, want the answer and a line back", i+1, got)
		}
	}
	if got := ask("how's my streak?"); got != "Bo is on 12 days in a row. Somebody stop him." {
		t.Errorf("the game: got %q", got)
	}
}

// A follow-up reaches the agent with what came before it, each question
// with who asked it.
func TestTheAgentIsShownTheRecentConversation(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	interp := &scripted{reqs: []Request{{Kind: KindLeader, Span: SpanMonth}, {Kind: KindUnknown}}}
	f := &fakeOllama{models: []string{"qwen2.5:7b"}, content: "Sure."}
	answer, c := newAgentAnswerer(t, db, interp, testAgent(t, f))

	if err := answer(context.Background(), senderUUID, "who leads?", "", nil); err != nil {
		t.Fatal(err)
	}
	first := c.last(t)
	if err := answer(context.Background(), senderUUID, "and last week?", "", nil); err != nil {
		t.Fatal(err)
	}
	// Both questions went to the agent: the first placed, the second not.
	// The first answer, "Sure.", states none of its lookup's figures, so it
	// is sent back once before the lookup itself is posted.
	if len(f.chats) != 3 {
		t.Fatalf("%d chat calls, want 3", len(f.chats))
	}
	msgs := f.chats[2]["messages"].([]any)
	var said []string
	for _, m := range msgs[1:] {
		said = append(said, m.(map[string]any)["content"].(string))
	}
	// The question's own message starts with notes about it; the question
	// is its last line, as the earlier one was said.
	if n := len(said) - 1; n >= 0 {
		said[n] = said[n][strings.LastIndex(said[n], "\n")+1:]
	}
	want := []string{"Bo: who leads?", first, "Bo: and last week?"}
	if strings.Join(said, "|") != strings.Join(want, "|") {
		t.Errorf("messages = %q, want %q", said, want)
	}
}

func TestTheSegueCarriesARealFigure(t *testing.T) {
	t.Parallel()
	tr, _, players, results := lookupFixture(t)
	now := time.Now()
	// The fixture: Alma 3s and Bo 4s on each of the last twelve days, so
	// Alma leads alone, the streak is a tie, and both have played today.
	lines := map[string]bool{}
	for n := range 3 {
		lines[segue(tr, n, players, results, now)] = true
	}
	for _, want := range []string{
		"Anyway, back to what matters: Alma leads",
		"Anyway, back to what matters: 2 of 2 have played today's Wordle.",
	} {
		found := false
		for l := range lines {
			found = found || strings.HasPrefix(l, want)
		}
		if !found {
			t.Errorf("no segue starts %q among %v", want, lines)
		}
	}
	for l := range lines {
		if strings.Contains(l, "in a row") {
			t.Errorf("a tied streak made a segue: %q", l)
		}
	}
}

// What has aged out is gone from memory, not only from what is read, and
// the timer forgets the conversation on its own once it has gone quiet.
func TestTheConversationForgets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	var c conversation
	c.add(turn{at: now.Add(-2 * time.Hour), question: "older"})
	c.add(turn{at: now.Add(-time.Hour), question: "old"})
	c.recent(now)
	c.mu.Lock()
	n := len(c.turns)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("%d turns held after a read past the window", n)
	}

	quick := &conversation{quiet: 20 * time.Millisecond}
	quick.add(turn{at: time.Now(), question: "new"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		quick.mu.Lock()
		n = len(quick.turns)
		quick.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the timer never forgot the conversation")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Without the agent nothing reads the conversation, so nothing is held.
func TestNothingIsRememberedWithoutTheAgent(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	var rec recording
	answer, _ := newAgentAnswerer(t, db, &rec, nil)
	for range 2 {
		if err := answer(context.Background(), senderUUID, "tack", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if h := rec.prompts[1].History; len(h) != 0 {
		t.Errorf("remembered %v without an agent", h)
	}
}

// A question read just before the window closes keeps the conversation:
// the timer that fires while it is being answered finds it active and
// waits, rather than clearing what the answer is about to add to.
func TestAQuestionBeingAnsweredKeepsTheConversation(t *testing.T) {
	t.Parallel()
	c := &conversation{quiet: 100 * time.Millisecond}
	c.add(turn{at: time.Now(), question: "first"})
	time.Sleep(70 * time.Millisecond)
	asked := time.Now() // the next question arrives, and is read
	c.recent(asked)
	time.Sleep(70 * time.Millisecond)
	// The answer is done past the first window: the conversation is still
	// held for it.
	c.mu.Lock()
	n := len(c.turns)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("%d turns held mid-answer, want 1", n)
	}
}
