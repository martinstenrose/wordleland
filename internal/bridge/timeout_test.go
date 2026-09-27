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
		b, err := New(cfg, nil, nil, nil, respond, logger)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if b.filer.respondTimeout != tc.want {
			t.Errorf("agent %q: deadline %v, want %v", tc.agent, b.filer.respondTimeout, tc.want)
		}
	}
}

// The bridge looks quotes up in the client the app posts with, so a reply
// to a post it sent is recognised. With a second client of its own it
// would find nothing, which is how quoting failed before.
func TestTheBridgeRemembersWhatTheAppPosted(t *testing.T) {
	t.Parallel()
	cfg := config.Bridge{
		SignalAPIURL: "http://signal-cli-rest-api:8080", SignalAccount: "+46700000000",
		SignalGroupID: "c2FtcGxlLWdyb3VwLWlk", Replies: true,
	}
	client, err := NewClient(cfg.SignalAPIURL, cfg.SignalAccount, cfg.SignalGroupID)
	if err != nil {
		t.Fatal(err)
	}
	client.sent.add(1_756_000_000_000, "📊 Wordle 1 560: 3 of 4 in.")
	b, err := New(cfg, client, nil, nil, func(context.Context, Message) error { return nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := b.filer.posts.Sent(1_756_000_000_000); !ok || text != "📊 Wordle 1 560: 3 of 4 in." {
		t.Errorf("Sent = %q, %v; want the post the app sent", text, ok)
	}
}
