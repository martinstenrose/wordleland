package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/wordle"
)

// placingAs is a model server that places every question as content.
func placingAs(t *testing.T, content string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"test:1b"}]}`)
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	mux.HandleFunc("POST /api/generate", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"done":true}`) })
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"message":{"content":%q}}`, content)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// A question is answered as the bot would answer it, "me" meaning the
// player named, and printed rather than posted.
func TestAskAnswersAsAPlayer(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n", "user", "create", "--email", "admin@example.tld", "--admin")
	today := strconv.Itoa(wordle.PuzzleForDate(time.Now()))
	for _, p := range []struct{ name, guesses string }{{"Martin", "3"}, {"Bo", "5"}} {
		c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", p.name)
		c.mustRun("", "--as", "admin@example.tld", "results", "set",
			"--player", strings.ToLower(p.name), "--puzzle", today, "--guesses", p.guesses)
	}
	url := placingAs(t, `{"kind":"score","span":"month"}`)

	out := c.mustRun("", "ask", "--url", url, "--model", "test:1b", "--locale", "sv", "--player", "martin", "vad", "fick", "jag?")
	if !strings.Contains(out, `placed as: {"kind":"score","span":"month"}`) {
		t.Errorf("the placing is not shown:\n%s", out)
	}
	if !strings.Contains(out, "Martin, ") || !strings.Contains(out, ": 3/6.") {
		t.Errorf("not answered as Martin:\n%s", out)
	}
	if out := c.mustRun("", "ask", "--url", url, "--model", "test:1b", "vad fick jag?"); !strings.Contains(out, "Who do you mean?") {
		t.Errorf("without --player there is no \"me\":\n%s", out)
	}
	if _, err := c.run("", "ask", "--url", url, "--model", "test:1b", "--player", "zed", "vem leder?"); err == nil ||
		!strings.Contains(err.Error(), `no player "zed"`) {
		t.Errorf("an unknown player: %v", err)
	}
}

// Trying a question out keeps nothing: one the bot could not place is not
// added to the owner's list of what the group asked.
func TestAskKeepsNothing(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	url := placingAs(t, `{"kind":"unknown","span":"month"}`)
	c.mustRun("", "ask", "--url", url, "--model", "test:1b", "vad är huvudstaden i Norge?")
	if out := c.mustRun("", "questions", "list"); !strings.Contains(out, "No unanswered questions.") {
		t.Errorf("a tried question was kept:\n%s", out)
	}
	if _, err := c.run("", "ask", "--url", url, "--model", "test:1b"); err == nil {
		t.Error("no question was accepted")
	}
}
