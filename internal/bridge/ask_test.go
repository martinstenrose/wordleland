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

// A question that waited behind slow answers past maxQuestionWait is
// dropped when its turn comes, not answered to a conversation that has
// moved on.
func TestAQuestionThatWaitedTooLongIsDropped(t *testing.T) {
	f, cap := testFiler(t)
	start := time.Date(2026, time.September, 27, 20, 0, 0, 0, time.UTC)
	var elapsed atomic.Int64
	f.now = func() time.Time { return start.Add(time.Duration(elapsed.Load())) }
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var answered atomic.Int32
	f.respond = func(context.Context, Message) error {
		started <- struct{}{}
		<-release
		answered.Add(1)
		return nil
	}

	f.handle(context.Background(), question("who leads?"))
	<-started // the first has its turn; the clock moves while it answers
	f.handle(context.Background(), question("and last week?"))
	elapsed.Store(int64(maxQuestionWait + time.Second))
	close(release)
	f.wait()

	if got := answered.Load(); got != 1 {
		t.Errorf("answered %d questions, want 1", got)
	}
	if !strings.Contains(cap.log(), "waited too long") {
		t.Errorf("the dropped question was not logged:\n%s", cap.log())
	}
}

// The deadline the answer runs under is the filer's, not the constant: an
// agent's answer gets its minutes.
func TestTheAnswerRunsUnderTheFilersDeadline(t *testing.T) {
	f, _ := testFiler(t)
	f.respondTimeout = agentRespondTimeout
	var left atomic.Int64
	f.respond = func(ctx context.Context, _ Message) error {
		if d, ok := ctx.Deadline(); ok {
			left.Store(int64(time.Until(d)))
		}
		return nil
	}
	f.handle(context.Background(), question("who leads?"))
	f.wait()
	if got := time.Duration(left.Load()); got <= respondTimeout {
		t.Errorf("answer had %v, want more than %v", got, respondTimeout)
	}
}

// A panic while answering is logged and costs that answer, not the
// process, and the next question is still answered.
func TestAPanickingAnswerIsContained(t *testing.T) {
	f, cap := testFiler(t)
	p := &fakePresence{}
	f.presence = p
	var calls atomic.Int32
	f.respond = func(context.Context, Message) error {
		if calls.Add(1) == 1 {
			panic("assignment to entry in nil map")
		}
		return nil
	}
	f.handle(context.Background(), question("who leads?"))
	f.wait()
	f.handle(context.Background(), question("and now?"))
	f.wait()
	if calls.Load() != 2 {
		t.Errorf("%d answers attempted, want 2", calls.Load())
	}
	if !strings.Contains(cap.log(), "answering a question panicked") {
		t.Errorf("the panic was not logged:\n%s", cap.log())
	}
	// Both answers took the typing indicator down, the panicked one too.
	if seq := strings.Join(p.sequence(), ","); strings.Count(seq, "stopped") != 2 {
		t.Errorf("typing was not stopped after each answer: %s", seq)
	}
}
