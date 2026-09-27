package reply

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheOffTopicRunIsCountedSinceTheLastWordleAnswer(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	var c conversation
	add := func(ago time.Duration, tp topic) { c.add(turn{at: now.Add(-ago), topic: tp}) }

	add(20*time.Minute, topicOff) // aged out
	add(10*time.Minute, topicOff)
	add(9*time.Minute, topicWordle)
	add(8*time.Minute, topicOff)
	add(7*time.Minute, topicNeutral) // thanks: neither ends nor extends
	add(6*time.Minute, topicOff)
	if got := c.offTopicRun(now); got != 2 {
		t.Errorf("run = %d, want 2", got)
	}
	// A quarter of an hour later the conversation is a new one.
	if got := c.offTopicRun(now.Add(memoryWindow)); got != 0 {
		t.Errorf("run after the window = %d, want 0", got)
	}
}

func TestTheConversationRemembersTheLastFewTurns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	var c conversation
	for i := range 6 {
		c.add(turn{at: now.Add(time.Duration(i-6) * time.Minute), question: string(rune('a' + i))})
	}
	c.add(turn{at: now.Add(-time.Hour), question: "old"}) // out of order, and aged out
	got := c.recent(now)
	if len(got) != maxTurns || got[0].question != "c" || got[len(got)-1].question != "f" {
		t.Errorf("recent = %+v, want the last %d, oldest first", got, maxTurns)
	}
	if got := c.recent(now.Add(memoryWindow + 5*time.Minute)); len(got) != 0 {
		t.Errorf("remembered %d turns past the window", len(got))
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

// Off-topic is answered, with a line back to the game, until the group has
// drifted maxOffTopicInARow times; then it is turned away, still with the
// line back. A question about the game ends the run.
func TestOffTopicIsAnsweredTwiceThenTurnedAway(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	unknown := Request{Kind: KindUnknown}
	interp := &scripted{reqs: []Request{unknown, unknown, unknown, {Kind: KindStreak, Player: "Bo"}, unknown}}
	f := &fakeOllama{models: []string{"qwen2.5:7b"}, content: "Stockholm, obviously."}
	answer, c := newAgentAnswerer(t, db, interp, testAgent(t, f))

	ask := func(q string) string {
		t.Helper()
		if err := answer(context.Background(), senderUUID, q, "", nil); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return c.last(t)
	}
	for i, q := range []string{"hej!", "capital of Sweden?"} {
		got := ask(q)
		first, steer, ok := strings.Cut(got, "\n")
		if !ok || first != "Stockholm, obviously." || !strings.HasPrefix(steer, "Anyway") {
			t.Errorf("off-topic %d: got %q, want the answer and a line back", i+1, got)
		}
	}
	if got := ask("capital of Norway?"); !strings.HasPrefix(got, "That's enough small talk.") ||
		!strings.Contains(got, "\nAnyway") {
		t.Errorf("third off-topic: got %q, want it turned away with a line back", got)
	}
	if got := ask("how's my streak?"); !strings.HasPrefix(got, "Bo: 12 days in a row") {
		t.Errorf("the game: got %q", got)
	}
	if got := ask("capital of Denmark?"); !strings.HasPrefix(got, "Stockholm, obviously.") {
		t.Errorf("after the game: got %q, want an answer again", got)
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
	if len(f.chats) != 1 {
		t.Fatalf("%d chat calls, want 1", len(f.chats))
	}
	msgs := f.chats[0]["messages"].([]any)
	var said []string
	for _, m := range msgs[1:] {
		said = append(said, m.(map[string]any)["content"].(string))
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
