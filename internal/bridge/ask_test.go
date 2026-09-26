package bridge

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A result posted while the model is thinking is filed at once: the
// question is answered beside the worker, not on it.
func TestAResultDoesNotWaitForAnAnswer(t *testing.T) {
	f, cap := testFiler(t)
	release := make(chan struct{})
	f.respond = func(context.Context, Message) error {
		<-release
		return nil
	}

	f.handle(context.Background(), question("who leads?"))
	f.handle(context.Background(), msg("Wordle 1 891 3/6*"))

	if len(cap.sent()) != 1 {
		t.Fatalf("filed %d results while a question was being answered, want 1", len(cap.sent()))
	}
	close(release)
	f.wait()
}

// One question at a time: the model has one CPU's worth of attention.
func TestQuestionsAreAnsweredOneAtATime(t *testing.T) {
	f, _ := testFiler(t)
	var inFlight, peak atomic.Int32
	f.respond = func(context.Context, Message) error {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		return nil
	}

	for i := 0; i < maxQuestionsInHand; i++ {
		f.handle(context.Background(), question("who leads?"))
	}
	f.wait()

	if peak.Load() != 1 {
		t.Errorf("%d answers ran at once, want 1", peak.Load())
	}
}

// A question beyond the line is dropped, and said so, rather than answered
// a minute later to a conversation that has moved on.
func TestAQuestionBeyondTheLineIsDropped(t *testing.T) {
	f, cap := testFiler(t)
	release := make(chan struct{})
	var answered atomic.Int32
	f.respond = func(context.Context, Message) error {
		<-release
		answered.Add(1)
		return nil
	}

	for i := 0; i < maxQuestionsInHand+2; i++ {
		f.handle(context.Background(), question("who leads?"))
	}
	close(release)
	f.wait()

	if got := answered.Load(); got != maxQuestionsInHand {
		t.Errorf("answered %d questions, want %d", got, maxQuestionsInHand)
	}
	if !strings.Contains(cap.log(), "dropping a question") {
		t.Errorf("the dropped questions were not logged:\n%s", cap.log())
	}
}
