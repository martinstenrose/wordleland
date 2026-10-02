package reply

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The question before is what a follow-up is read against: the last one
// the bot answered, its last part when it had several, and not a
// thank-you or a question it could not place.
func TestAFollowUpIsToldTheQuestionBefore(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	var next Request
	var seen []*Request
	capture := interpreterFunc(func(_ context.Context, p Prompt) (Request, error) {
		seen = append(seen, p.Previous)
		return next, nil
	})
	answer, _ := newAnswerer(t, db, capture)
	ask := func(req Request) {
		t.Helper()
		next = req
		if err := answer(context.Background(), senderUUID, "…", "", nil); err != nil {
			t.Fatal(err)
		}
	}

	ask(Request{Kind: KindLeader, Span: SpanMonth, Also: []Request{{Kind: KindStreak, Player: "Bo"}}})
	ask(Request{Kind: KindThanks})
	ask(Request{Kind: KindUnknown})
	ask(Request{Kind: KindStreak, Player: "Alma"})

	if seen[0] != nil {
		t.Errorf("the first question was told %+v", seen[0])
	}
	streak := Request{Kind: KindStreak, Player: "Bo"}
	for i := 1; i <= 3; i++ {
		if seen[i] == nil || Describe(*seen[i]) != Describe(streak) {
			t.Errorf("question %d was told %v, want the streak, the last part of the first", i+1, seen[i])
		}
	}
}

// A chat moves on: what was asked longer ago than the window is nothing
// to follow.
func TestTheQuestionBeforeIsForgottenAfterAWhile(t *testing.T) {
	t.Parallel()
	var last lastAnswered
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.Local)
	last.remember(Request{Kind: KindLeader, Span: SpanMonth}, now)
	if last.recall(now.Add(followUpWindow)) == nil {
		t.Error("forgotten within the window")
	}
	if got := last.recall(now.Add(followUpWindow + time.Second)); got != nil {
		t.Errorf("remembered %+v after the window", got)
	}
}

// The model is shown the request before after the fixed instructions,
// which the server reuses only up to the first difference, and not at all
// when there is none.
func TestThePromptCarriesTheQuestionBeforeLast(t *testing.T) {
	t.Parallel()
	p := PlacingPrompt("och Bo då?")
	with := systemPrompt(p)
	described := Describe(*p.Previous)
	at := strings.Index(with, described)
	if at < 0 {
		t.Fatalf("the prompt does not carry %s:\n%s", described, with)
	}
	if fixed := strings.Index(with, "Today is "); at < fixed {
		t.Error("the question before comes ahead of what changes once a day")
	}
	p.Previous = nil
	if without := systemPrompt(p); strings.Contains(without, "follow-up") {
		t.Errorf("a prompt with nothing before still speaks of a follow-up:\n%s", without)
	}
}
