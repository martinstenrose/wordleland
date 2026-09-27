package bridge

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/martinstenrose/wordleland/internal/config"
)

// An answer from the agent takes rounds of a larger model, and gets the
// longer deadline; without the agent the placing model's own is kept.
func TestTheAgentGetsTheLongerAnswerDeadline(t *testing.T) {
	t.Parallel()
	respond := func(context.Context, Message) error { return nil }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		agent string
		want  any
	}{
		{"", respondTimeout},
		{"qwen2.5:7b", agentRespondTimeout},
	} {
		cfg := config.Bridge{
			SignalAPIURL: "http://signal-cli-rest-api:8080", SignalAccount: "+46700000000",
			SignalGroupID: "c2FtcGxlLWdyb3VwLWlk", Replies: true, LLMAgentModel: tc.agent,
		}
		b, err := New(cfg, nil, nil, respond, logger)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if b.filer.respondTimeout != tc.want {
			t.Errorf("agent %q: deadline %v, want %v", tc.agent, b.filer.respondTimeout, tc.want)
		}
	}
}
