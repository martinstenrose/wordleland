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
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		f.mu.Lock()
		f.chats = append(f.chats, req)
		content := f.content
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": content}})
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
		{`{"kind":"leader","span":"month","days":0,"player":" Bo "}`,
			Request{Kind: KindLeader, Span: SpanMonth, Player: "Bo"}},
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
