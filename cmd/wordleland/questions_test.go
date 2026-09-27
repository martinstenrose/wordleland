package main

import (
	"context"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// `questions list` is read-only: no acting admin, the empty state says so,
// and rows come out newest first with a header.
func TestQuestionsList(t *testing.T) {
	c := newCLI(t)

	out := c.mustRun("", "questions", "list")
	if !strings.Contains(out, "No unanswered questions.") {
		t.Errorf("empty state:\n%s", out)
	}

	ctx := context.Background()
	db := c.db()
	for _, q := range []string{"can you order pizza?", "vem är bäst på pingis?"} {
		if err := store.RecordUnansweredQuestion(ctx, db, q); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	db.Close()

	out = c.mustRun("", "questions", "list")
	if !strings.Contains(out, "ASKED") || !strings.Contains(out, "QUESTION") {
		t.Errorf("no header:\n%s", out)
	}
	pizza, pingis := strings.Index(out, "can you order pizza?"), strings.Index(out, "vem är bäst på pingis?")
	if pizza < 0 || pingis < 0 {
		t.Fatalf("questions missing:\n%s", out)
	}
	if pingis > pizza {
		t.Errorf("not newest first:\n%s", out)
	}
}

func TestQuestionsUnknownVerb(t *testing.T) {
	c := newCLI(t)
	if _, err := c.run("", "questions", "purge"); err == nil {
		t.Error("an unknown verb was accepted")
	}
}
