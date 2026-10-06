package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/reply"
)

// fakeModel places every question as the test expects, except the ones it
// is told to get wrong: a stand-in for the model server.
func fakeModel(t *testing.T, wrong map[string]bool) *httptest.Server {
	t.Helper()
	return fakeModelTimed(t, wrong, nil)
}

// fakeModelTimed is fakeModel reporting how long it read each prompt: the
// call's number in, nanoseconds out.
func fakeModelTimed(t *testing.T, wrong map[string]bool, reading func(call int) int64) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	want := map[string]reply.Request{}
	for _, c := range reply.PlacingCases {
		want[c.Question] = c.Want
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"test:1b"}]}`)
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"capabilities":["completion"]}`)
	})
	mux.HandleFunc("POST /api/generate", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"done":true}`)
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.Unmarshal(body, &req)
		question := req.Messages[len(req.Messages)-1].Content
		answer := want[question]
		if wrong[question] {
			answer = reply.Request{Kind: reply.KindUnknown}
		}
		content, _ := json.Marshal(answer)
		answered := map[string]any{"message": map[string]string{"content": string(content)}}
		if reading != nil {
			answered["prompt_eval_duration"] = reading(int(calls.Add(1)))
			answered["eval_duration"] = int64(3 * time.Second)
		}
		json.NewEncoder(w).Encode(answered)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// The test reports each miss as it happens and a score at the end, and
// needs no database.
func TestPlacingTestScoresAModel(t *testing.T) {
	t.Parallel()
	missed := reply.PlacingCases[0].Question
	srv := fakeModel(t, map[string]bool{missed: true})
	var out bytes.Buffer
	if err := run([]string{"--db", "/nonexistent/wordleland.db", "placing-test", "--url", srv.URL, "--model", "test:1b"}, &out); err != nil {
		t.Fatalf("placing-test: %v\n%s", err, out.String())
	}
	n := len(reply.PlacingCases)
	for _, want := range []string{
		"✗", missed, "kind: got unknown, want leader",
		fmt.Sprintf("test:1b: %d of %d placed (%d%%)", n-1, n, 100*(n-1)/n),
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "✓") {
		t.Errorf("a placed question was listed without --all:\n%s", out.String())
	}
}

// A model the server cannot provide is an error, not a score of nothing.
func TestPlacingTestSaysWhenTheModelIsNotThere(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"models":[]}`) })
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":"pull model manifest: file does not exist"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	err := runPlacingTestWithin(t, srv.URL, "nope:1b")
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("err = %v, want the model named as not ready", err)
	}
}

func runPlacingTestWithin(t *testing.T, url, model string) error {
	t.Helper()
	var out bytes.Buffer
	return run([]string{"--db", "/nonexistent/wordleland.db", "placing-test", "--url", url, "--model", model,
		"--wait", "1s"}, &out)
}

// Reading the instructions once and only each question after is a cache
// that works, and the summary says so.
func TestPlacingTestSeesTheCache(t *testing.T) {
	t.Parallel()
	srv := fakeModelTimed(t, nil, func(call int) int64 {
		if call == 1 {
			return int64(20 * time.Second)
		}
		return int64(400 * time.Millisecond)
	})
	var out bytes.Buffer
	if err := run([]string{"placing-test", "--url", srv.URL, "--model", "test:1b"}, &out); err != nil {
		t.Fatalf("placing-test: %v\n%s", err, out.String())
	}
	for _, want := range []string{", cache works", "Prompt cache: works — reading the prompt took 20.0 s the first time and 0.4 s after (median). Writing: median 3.0 s."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestCacheVerdict(t *testing.T) {
	t.Parallel()
	usage := func(first time.Duration, rest ...time.Duration) []reply.Usage {
		out := []reply.Usage{{Reading: first}}
		for _, r := range rest {
			out = append(out, reply.Usage{Reading: r, Writing: time.Second})
		}
		return out
	}
	s := time.Second
	tests := []struct {
		usage []reply.Usage
		short string
		says  string
	}{
		{usage(20*s, s/2, s/2, 22*s), "cache works", "works"},
		{usage(22*s, 16*s, 15*s, 16*s), "no cache", "not working"},
		{usage(s/2, s/2, s/2), "cache works", "already read by an earlier question"},
		// The instructions still held from an earlier run: the first
		// question read them in a couple of seconds, not tens.
		{usage(17*s/10, 7*s/10, 7*s/10, 6*s/10), "cache works", "already read by an earlier question"},
		{usage(s/2, 16*s, 15*s), "no cache", "not working"},
		{usage(20 * s), "", ""},
	}
	for _, tc := range tests {
		line, short := cacheVerdict(tc.usage)
		if short != tc.short || !strings.Contains(line, tc.says) {
			t.Errorf("%v: got %q, %q; want %q and a line saying %q", tc.usage, short, line, tc.short, tc.says)
		}
	}
}
