package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// signalCalls records what the client asked signal-cli-rest-api for.
type signalCalls struct {
	mu    sync.Mutex
	calls []string // "METHOD path body"
}

func (s *signalCalls) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		s.mu.Lock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path+" "+string(raw))
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *signalCalls) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

const testGroupRecipient = "group.YzJGdGNHeGxMV2R5YjNWd0xXbGtMWFpoYkhWbExXWnZjaTEwWlhOMGN3PT0="

// A reaction names the message by its author and its id, and the group
// the way /v2/send addresses it.
func TestSeenReactsOnTheQuestion(t *testing.T) {
	var s signalCalls
	c, err := NewClient(s.server(t).URL, testAccount, testGroupID)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	m := msg("who leads?")
	m.ID = 1787490859545
	if err := c.Seen(context.Background(), m); err != nil {
		t.Fatalf("Seen: %v", err)
	}
	calls := s.all()
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want one", calls)
	}
	for _, want := range []string{"POST /v1/reactions/" + testAccount, `"reaction":"👀"`,
		`"recipient":"` + testGroupRecipient + `"`, `"target_author":"` + testUUID + `"`, `"timestamp":1787490859545`} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("reaction call lacks %s:\n%s", want, calls[0])
		}
	}
}

func TestTypingStartsAndStops(t *testing.T) {
	var s signalCalls
	c, err := NewClient(s.server(t).URL, testAccount, testGroupID)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Typing(context.Background(), true); err != nil {
		t.Fatalf("Typing(on): %v", err)
	}
	if err := c.Typing(context.Background(), false); err != nil {
		t.Fatalf("Typing(off): %v", err)
	}
	calls := s.all()
	if len(calls) != 2 ||
		!strings.HasPrefix(calls[0], "PUT /v1/typing-indicator/"+testAccount+" ") ||
		!strings.HasPrefix(calls[1], "DELETE /v1/typing-indicator/"+testAccount+" ") ||
		!strings.Contains(calls[0], `"recipient":"`+testGroupRecipient+`"`) {
		t.Errorf("calls = %v", calls)
	}
}

// fakePresence records the order of what the filer showed.
type fakePresence struct {
	mu    sync.Mutex
	seq   []string
	fail  bool
	seenM Message
}

func (p *fakePresence) Seen(_ context.Context, m Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq = append(p.seq, "seen")
	p.seenM = m
	if p.fail {
		return errors.New("signal is down")
	}
	return nil
}

func (p *fakePresence) Typing(_ context.Context, on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on {
		p.seq = append(p.seq, "typing")
	} else {
		p.seq = append(p.seq, "stopped")
	}
	if p.fail {
		return errors.New("signal is down")
	}
	return nil
}

func (p *fakePresence) sequence() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seq...)
}

// While the answer is worked out the question is marked seen and the bot
// is typing; once the answer is out the typing stops.
func TestAQuestionIsSeenAndTypedAtUntilAnswered(t *testing.T) {
	f, _ := testFiler(t)
	p := &fakePresence{}
	f.presence = p
	var typingWhenAnswering bool
	f.respond = func(context.Context, Message) error {
		// Give the presence goroutine a moment; the sequence below is what
		// matters, this only makes the "while" observable.
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if seq := p.sequence(); len(seq) >= 2 && seq[1] == "typing" {
				typingWhenAnswering = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		return nil
	}

	q := question("who leads?")
	q.ID = 42
	f.handle(context.Background(), q)
	f.wait()

	if got := p.sequence(); strings.Join(got, ",") != "seen,typing,stopped" {
		t.Errorf("sequence = %v, want seen, typing, stopped", got)
	}
	if !typingWhenAnswering {
		t.Error("the typing indicator was not up while the answer was being worked out")
	}
	if p.seenM.ID != 42 {
		t.Errorf("Seen got message id %d, want the question's", p.seenM.ID)
	}
}

// The indicator is started again while the answer takes longer than a
// client keeps showing it.
func TestTypingIsRefreshedWhileTheAnswerTakesLong(t *testing.T) {
	f, _ := testFiler(t)
	p := &fakePresence{}
	f.presence = p
	f.typingRefresh = 10 * time.Millisecond
	f.respond = func(context.Context, Message) error {
		time.Sleep(60 * time.Millisecond)
		return nil
	}

	f.handle(context.Background(), question("who leads?"))
	f.wait()

	typing := 0
	for _, s := range p.sequence() {
		if s == "typing" {
			typing++
		}
	}
	if typing < 3 {
		t.Errorf("typing was started %d times over 60ms with a 10ms refresh, want several", typing)
	}
}

// Presence failing is a debug line; the answer still goes out.
func TestPresenceFailureDoesNotCostTheAnswer(t *testing.T) {
	f, cap := testFiler(t)
	f.presence = &fakePresence{fail: true}
	answered := false
	f.respond = func(context.Context, Message) error {
		answered = true
		return nil
	}

	f.handle(context.Background(), question("who leads?"))
	f.wait()

	if !answered {
		t.Error("the question was not answered")
	}
	if strings.Contains(cap.log(), "level=WARN") || strings.Contains(cap.log(), "level=ERROR") {
		t.Errorf("presence failing was logged above debug:\n%s", cap.log())
	}
}

func TestNilPresenceShowsNothing(t *testing.T) {
	f, _ := testFiler(t)
	answered := false
	f.respond = func(context.Context, Message) error {
		answered = true
		return nil
	}
	f.handle(context.Background(), question("who leads?"))
	f.wait()
	if !answered {
		t.Error("the question was not answered")
	}
}

func TestTheMessageIDIsTheSendersTimestamp(t *testing.T) {
	msg, ok := decodeEnvelope(t, dataEnvelope("Wordle 1 891 3/6*", testGroupID))
	if !ok {
		t.Fatal("dropped")
	}
	if msg.ID != 1787490859545 {
		t.Errorf("ID = %d, want the sender's timestamp", msg.ID)
	}
}
