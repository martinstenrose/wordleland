package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// asked records what a Responder was handed.
type asked struct {
	mu   sync.Mutex
	msgs []Message
}

func (a *asked) responder(err error) Responder {
	return func(_ context.Context, m Message) error {
		a.mu.Lock()
		a.msgs = append(a.msgs, m)
		a.mu.Unlock()
		return err
	}
}

func (a *asked) got() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Message(nil), a.msgs...)
}

func question(body string) Message {
	m := msg(MentionPlaceholder + " " + body)
	m.MentionsBot = true
	return m
}

func TestAMentionIsAnsweredNotFiled(t *testing.T) {
	f, cap := testFiler(t)
	var a asked
	f.respond = a.responder(nil)

	f.handle(context.Background(), question("who is leading this month?"))

	if got := a.got(); len(got) != 1 || !strings.Contains(got[0].Body, "who is leading") {
		t.Fatalf("responder got %v, want the question", got)
	}
	if len(cap.sent()) != 0 {
		t.Errorf("a question was filed as a result: %v", cap.sent())
	}
}

// A share can carry a mention too. It is a result first: a score is never
// traded for a reply.
func TestAResultThatMentionsTheBotIsFiled(t *testing.T) {
	f, cap := testFiler(t)
	var a asked
	f.respond = a.responder(nil)

	m := msg("Wordle 1 891 3/6* " + MentionPlaceholder)
	m.MentionsBot = true
	f.handle(context.Background(), m)

	if len(cap.sent()) != 1 {
		t.Fatalf("filed %d results, want 1", len(cap.sent()))
	}
	if len(a.got()) != 0 {
		t.Errorf("a result was also answered as a question")
	}
}

func TestConversationWithoutAMentionIsNotAnswered(t *testing.T) {
	f, _ := testFiler(t)
	var a asked
	f.respond = a.responder(nil)

	f.handle(context.Background(), msg("who is leading this month?"))

	if len(a.got()) != 0 {
		t.Errorf("the responder was called for a message that did not mention the bot")
	}
}

// Replies off: a mention is ordinary conversation, logged as such and
// nothing else.
func TestNilResponderTreatsAMentionAsConversation(t *testing.T) {
	f, cap := testFiler(t)

	f.handle(context.Background(), question("hello?"))

	if len(cap.sent()) != 0 {
		t.Errorf("filed something: %v", cap.sent())
	}
	if !strings.Contains(cap.log(), "did not parse as a result") {
		t.Errorf("expected the ordinary conversation line, got:\n%s", cap.log())
	}
}

func TestResponderFailureIsLoggedWithoutTheQuestion(t *testing.T) {
	f, cap := testFiler(t)
	var a asked
	f.respond = a.responder(errors.New("model unreachable"))

	f.handle(context.Background(), question("secret question text"))

	log := cap.log()
	if !strings.Contains(log, "could not answer") || !strings.Contains(log, "model unreachable") {
		t.Errorf("expected the failure logged, got:\n%s", log)
	}
	if strings.Contains(log, "secret question text") {
		t.Errorf("the question's text was logged:\n%s", log)
	}
}

func TestResponderContextCarriesADeadline(t *testing.T) {
	f, _ := testFiler(t)
	var deadline bool
	f.respond = func(ctx context.Context, _ Message) error {
		_, deadline = ctx.Deadline()
		return nil
	}

	f.handle(context.Background(), question("anything"))

	if !deadline {
		t.Error("the responder ran without a deadline")
	}
}
