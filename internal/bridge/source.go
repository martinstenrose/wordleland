package bridge

import "context"

// MessageSource delivers incoming Signal messages.
//
// Run returns only when ctx is cancelled or the source has failed in a way
// it cannot recover from. A source that reconnects does so behind this
// interface, so the pipeline above it never sees a disconnection.
//
// The interface exists so the whole bridge can be tested without a
// Signal account: fakeSource replays fixtures through exactly the path the
// websocket uses.
type MessageSource interface {
	Run(ctx context.Context, out chan<- Message) error
}

// fakeSource replays a fixed set of messages, then waits for cancellation.
type fakeSource struct {
	messages []Message
	// sent is closed once every message has been handed over, so a test can
	// wait for delivery without sleeping.
	sent chan struct{}
}

func newFakeSource(messages ...Message) *fakeSource {
	return &fakeSource{messages: messages, sent: make(chan struct{})}
}

func (f *fakeSource) Run(ctx context.Context, out chan<- Message) error {
	for _, m := range f.messages {
		select {
		case out <- m:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	close(f.sent)

	<-ctx.Done()
	return ctx.Err()
}
