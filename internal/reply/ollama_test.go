package reply

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOllama is the three endpoints the client uses, with what each was
// asked recorded.
type fakeOllama struct {
	mu      sync.Mutex
	models  []string
	pulled  []string
	chats   []map[string]any
	content string
	// blockFrom, when not zero, is the chat call (counting from 1) from
	// which the server hangs until the request is given up on: a model
	// that takes longer than the deadline.
	blockFrom int
	// always, when set, is the chat's message every time: a model that
	// never stops calling tools.
	always map[string]any
	// capabilities is what /api/show reports for every model; nil leaves
	// the field out, as an older server does.
	capabilities []string
	// warmed is the models loaded ahead of a question.
	warmed []string
	// replies, when set, are the chat's messages in turn, for a
	// conversation of more than one round; content is used after them.
	replies []map[string]any
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var models []map[string]string
		for _, m := range f.models {
			models = append(models, map[string]string{"name": m})
		}
		json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.pulled = append(f.pulled, req.Model)
		f.models = append(f.models, req.Model)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"status": "success"})
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := map[string]any{}
		if f.capabilities != nil {
			out["capabilities"] = f.capabilities
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/generate", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.warmed = append(f.warmed, req["model"].(string))
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"done": true})
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		f.mu.Lock()
		f.chats = append(f.chats, req)
		n := len(f.chats)
		message := map[string]any{"role": "assistant", "content": f.content}
		if len(f.replies) > 0 {
			message, f.replies = f.replies[0], f.replies[1:]
		}
		if f.always != nil {
			message = f.always
		}
		block := f.blockFrom != 0 && n >= f.blockFrom
		f.mu.Unlock()
		if block {
			<-r.Context().Done()
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"message": message})
	})
	return mux
}

func testOllama(t *testing.T, f *fakeOllama) *Ollama {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return NewOllama(srv.URL+"/", "qwen2.5:3b")
}

func TestQuestionsBeforeTheModelIsReadyAreNotReady(t *testing.T) {
	o := testOllama(t, &fakeOllama{models: []string{"qwen2.5:3b"}})
	_, err := o.Interpret(context.Background(), Prompt{Question: "who leads?"})
	if !errors.Is(err, ErrNotReady) {
		t.Errorf("err = %v, want ErrNotReady before Prepare", err)
	}
}

func TestPrepareFindsAPresentModelWithoutPulling(t *testing.T) {
	f := &fakeOllama{models: []string{"llama3.2:3b", "qwen2.5:3b"}}
	o := testOllama(t, f)
	o.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !o.Ready() {
		t.Fatal("not ready after Prepare with the model present")
	}
	if len(f.pulled) != 0 {
		t.Errorf("pulled %v, want nothing", f.pulled)
	}
}

func TestPreparePullsAMissingModel(t *testing.T) {
	f := &fakeOllama{}
	o := testOllama(t, f)
	o.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !o.Ready() {
		t.Fatal("not ready after the pull")
	}
	if len(f.pulled) != 1 || f.pulled[0] != "qwen2.5:3b" {
		t.Errorf("pulled %v, want the configured model once", f.pulled)
	}
}

// The model is asked with the whole job in the system message and only
// the question in the user one, constrained to the request schema, at
// temperature zero.
func TestInterpretAsksForARequest(t *testing.T) {
	f := &fakeOllama{models: []string{"qwen2.5:3b"},
		content: `{"kind":"leader","span":"days","days":7,"player":""}`}
	o := testOllama(t, f)
	o.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	req, err := o.Interpret(context.Background(), Prompt{
		Question: "vem har bäst snitt senaste veckan?",
		Asker:    "Bo", Players: []string{"Alma", "Bo"},
		Today: time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if req != (Request{Kind: KindLeader, Span: SpanDays, Days: 7}) {
		t.Errorf("request = %+v", req)
	}

	if len(f.chats) != 1 {
		t.Fatalf("%d chat calls, want 1", len(f.chats))
	}
	chat := f.chats[0]
	if chat["model"] != "qwen2.5:3b" || chat["stream"] != false {
		t.Errorf("model/stream = %v/%v", chat["model"], chat["stream"])
	}
	if _, ok := chat["format"].(map[string]any)["properties"]; !ok {
		t.Errorf("format is not a JSON schema: %v", chat["format"])
	}
	if temp := chat["options"].(map[string]any)["temperature"]; temp != 0.0 {
		t.Errorf("temperature = %v, want 0", temp)
	}
	msgs := chat["messages"].([]any)
	system := msgs[0].(map[string]any)["content"].(string)
	user := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{"Bo", "Alma", "15 September 2026", `"leader"`, `"days"`} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, system)
		}
	}
	if user != "vem har bäst snitt senaste veckan?" {
		t.Errorf("user message = %q, want the question alone", user)
	}
}

// Whatever the model writes outside the known values is "I don't know",
// which gets the help line, rather than an error that gets a log line.
func TestParseRequestNormalises(t *testing.T) {
	tests := []struct {
		content string
		want    Request
	}{
		{`{"kind":"standing","span":"month","days":0,"player":" Bo "}`,
			Request{Kind: KindStanding, Span: SpanMonth, Player: "Bo"}},
		{`{"kind":"weather","span":"month","days":0,"player":""}`,
			Request{Kind: KindUnknown, Span: SpanMonth}},
		{`{"kind":"leader","span":"days","days":0,"player":""}`,
			Request{Kind: KindLeader, Span: SpanMonth}},
		{`{"kind":"leader","span":"month","days":9,"player":""}`,
			Request{Kind: KindLeader, Span: SpanMonth}},
		{`{"kind":"streak","span":"","days":0,"player":""}`,
			Request{Kind: KindStreak, Span: SpanMonth}},
	}
	for _, tc := range tests {
		got, err := parseRequest(tc.content)
		if err != nil {
			t.Errorf("%s: %v", tc.content, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.content, got, tc.want)
		}
	}
	if _, err := parseRequest("Sure! Here is the JSON:"); err == nil {
		t.Error("prose parsed as a request")
	}
}

// Once present, the model is loaded ahead of the first question, with a
// request that asks it nothing.
func TestPrepareLoadsTheModel(t *testing.T) {
	f := &fakeOllama{models: []string{"qwen2.5:3b"}}
	o := testOllama(t, f)
	o.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(f.warmed) != 1 || f.warmed[0] != "qwen2.5:3b" {
		t.Errorf("warmed %v, want the model once", f.warmed)
	}
	if len(f.chats) != 0 {
		t.Errorf("%d chats while warming, want none", len(f.chats))
	}
}

// An agent model the server says cannot call tools is never made ready,
// and Prepare stops rather than trying again; one that can, or a server
// that does not say, is.
func TestTheAgentNeedsAModelThatCallsTools(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		caps  []string
		ready bool
	}{
		{[]string{"completion"}, false},
		{[]string{"completion", "tools"}, true},
		{nil, true},
	} {
		f := &fakeOllama{models: []string{"gemma:7b"}, capabilities: tc.caps}
		srv := httptest.NewServer(f.handler())
		t.Cleanup(srv.Close)
		a := NewAgent(srv.URL, "gemma:7b")
		done := make(chan struct{})
		go func() {
			a.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("capabilities %v: Prepare kept trying", tc.caps)
		}
		if a.Ready() != tc.ready {
			t.Errorf("capabilities %v: ready = %v, want %v", tc.caps, a.Ready(), tc.ready)
		}
	}
	// The placing model needs nothing, whatever the server says.
	f := &fakeOllama{models: []string{"qwen2.5:3b"}, capabilities: []string{"completion"}}
	o := testOllama(t, f)
	o.Prepare(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !o.Ready() {
		t.Error("the placing model was refused for lacking tools")
	}
}

// A model that says it thinks is told not to, on every chat; one that
// does not say so is sent no such setting, which an older server may
// refuse.
func TestThinkingIsTurnedOffOnlyForAModelThatThinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		caps []string
		off  bool
	}{
		{[]string{"completion", "tools", "thinking"}, true},
		{[]string{"completion", "tools"}, false},
		{nil, false},
	} {
		f := &fakeOllama{models: []string{"qwen3:8b"}, capabilities: tc.caps, content: `{"kind":"today"}`}
		srv := httptest.NewServer(f.handler())
		t.Cleanup(srv.Close)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))

		o := NewOllama(srv.URL, "qwen3:8b")
		o.Prepare(context.Background(), logger)
		if _, err := o.Interpret(context.Background(), Prompt{Question: "who played today?", Today: time.Now()}); err != nil {
			t.Fatalf("%v: Interpret: %v", tc.caps, err)
		}
		a := NewAgent(srv.URL, "qwen3:8b")
		a.Prepare(context.Background(), logger)
		if _, err := a.chat(context.Background(), []chatMessage{{Role: "user", Content: "hej"}}); err != nil {
			t.Fatalf("%v: chat: %v", tc.caps, err)
		}
		for i, chat := range f.chats {
			think, set := chat["think"]
			if set != tc.off || (set && think != false) {
				t.Errorf("%v, chat %d: think = %v (set %v), want off %v", tc.caps, i, think, set, tc.off)
			}
		}
	}
}

// The placing model needs nothing of the server, so one that cannot say
// what the model does is no reason to hold it back.
func TestThePlacingModelIsReadyWithoutCapabilities(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	f := &fakeOllama{models: []string{"qwen2.5:3b"}}
	mux.Handle("/api/tags", f.handler())
	mux.Handle("/api/generate", f.handler())
	srv := httptest.NewServer(mux) // no /api/show: a 404
	t.Cleanup(srv.Close)
	o := NewOllama(srv.URL, "qwen2.5:3b")
	// Bounded: held back, Prepare would try again for ever.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.Prepare(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !o.Ready() {
		t.Error("the placing model was held back by a server that cannot show capabilities")
	}
}

func TestReasoningIsNeverPartOfTheAnswer(t *testing.T) {
	t.Parallel()
	if got := withoutThinking("<think>\nThe user asks…\n</think>\n\nStockholm."); got != "Stockholm." {
		t.Errorf("got %q", got)
	}
	if got := withoutThinking("Just the answer."); got != "Just the answer." {
		t.Errorf("got %q", got)
	}
}
