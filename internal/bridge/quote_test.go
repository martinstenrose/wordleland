package bridge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/martinstenrose/wordleland/internal/config"
)

// replyEnvelope is a mention of the bot posted as a reply to an earlier
// message: Signal carries the quoted message's id, author and text along —
// all as the replying client wrote them.
func replyEnvelope(body, quotedAuthor, quotedText string) string {
	return fmt.Sprintf(`{
	  "envelope": {
	    "sourceUuid": %q,
	    "sourceName": %q,
	    "timestamp": 1787490859545,
	    "dataMessage": {
	      "message": %q,
	      "mentions": [{"number": %q, "start": 0, "length": 1}],
	      "quote": {"id": 1787400000000, "author": %q, "authorNumber": %q, "authorUuid": "0d2f1b4e-0000-4000-8000-00000000abcd", "text": %q},
	      "groupInfo": { "groupId": %q }
	    }
	  }
	}`, testUUID, testName, body, testAccount, quotedAuthor, quotedAuthor, quotedText, testGroupID)
}

// The envelope keeps only the id of a reply to the bot's post, never the
// text the reply carries: that is the replying client's claim.
func TestAReplyToTheBotsOwnPostCarriesItsID(t *testing.T) {
	msg, ok := decodeEnvelope(t, replyEnvelope(MentionPlaceholder+" what does this mean?", testAccount, "whatever the client says"))
	if !ok {
		t.Fatal("a reply was dropped")
	}
	if !msg.MentionsBot {
		t.Error("MentionsBot = false on a reply that mentions the bot")
	}
	if msg.QuoteID != 1787400000000 {
		t.Errorf("QuoteID = %d, want the quoted message's id", msg.QuoteID)
	}
	if msg.Quoted != "" {
		t.Errorf("Quoted = %q, want nothing from the reply itself", msg.Quoted)
	}
}

// Somebody else's words are not the bot's business, replied to or not.
func TestAReplyToAnotherMemberCarriesNothing(t *testing.T) {
	msg, ok := decodeEnvelope(t, replyEnvelope(MentionPlaceholder+" is this right?", "+46700000001", "private words"))
	if !ok {
		t.Fatal("a reply was dropped")
	}
	if msg.QuoteID != 0 || msg.Quoted != "" {
		t.Errorf("QuoteID/Quoted = %d/%q, want nothing from another member's message", msg.QuoteID, msg.Quoted)
	}
}

func TestAPlainMessageQuotesNothing(t *testing.T) {
	msg, _ := decodeEnvelope(t, dataEnvelope("Wordle 1 891 3/6*", testGroupID))
	if msg.QuoteID != 0 || msg.Quoted != "" {
		t.Errorf("QuoteID/Quoted = %d/%q on a message that replies to nothing", msg.QuoteID, msg.Quoted)
	}
}

// fakePosts is what the bot sent, as a test remembers it.
type fakePosts map[int64]string

func (p fakePosts) Sent(id int64) (string, bool) {
	text, ok := p[id]
	return text, ok
}

// The post a reply quotes is the bot's own copy of what it sent, found by
// the message id. A reply claiming a post the bot never sent — or sent
// before this process started — carries nothing, whatever text it brings.
func TestAQuotedPostIsTheBotsOwnCopy(t *testing.T) {
	f, _ := testFiler(t)
	f.posts = fakePosts{1787400000000: "Wordle 1891 — everyone's in."}
	var got []string
	f.respond = func(_ context.Context, m Message) error {
		got = append(got, m.Quoted)
		return nil
	}

	known := question("what does this mean?")
	known.QuoteID = 1787400000000
	f.handle(context.Background(), known)
	f.wait()

	forged := question("what does this mean?")
	forged.QuoteID = 42
	f.handle(context.Background(), forged)
	f.wait()

	if len(got) != 2 || got[0] != "Wordle 1891 — everyone's in." || got[1] != "" {
		t.Errorf("quoted = %q, want the bot's copy, then nothing", got)
	}
}

// Send remembers what it posted by the id signal-cli reports, which is
// what a later reply's quote names. signal-cli-rest-api writes that id as
// a JSON string; a number is accepted too, so the string form is the one
// this test must not skip.
func TestSendRemembersThePostByItsID(t *testing.T) {
	for name, response := range map[string]string{
		"as signal-cli-rest-api writes it, a string": `{"timestamp": "1787400000000"}`,
		"as a number": `{"timestamp": 1787400000000}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, response)
			}))
			t.Cleanup(srv.Close)
			c, err := NewClient(srv.URL, testAccount, testGroupID)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if err := c.Send(context.Background(), "Wordle 1891 — everyone's in."); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if text, ok := c.Sent(1787400000000); !ok || text != "Wordle 1891 — everyone's in." {
				t.Errorf("Sent = %q, %v; want the post by its id", text, ok)
			}
			if _, ok := c.Sent(42); ok {
				t.Error("an id the bot never sent was known")
			}
		})
	}
}

// A send whose answer carries no usable id still went out; it is only not
// remembered.
func TestSendWithoutAnIDIsStillSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"timestamp": "not-a-number"}`)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, testAccount, testGroupID)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// The memory of posts is bounded; the oldest go first.
func TestTheSentLogIsBounded(t *testing.T) {
	var l sentLog
	for i := 1; i <= sentCap+5; i++ {
		l.add(int64(i), "post")
	}
	if _, ok := l.get(1); ok {
		t.Error("the oldest post was still remembered past the cap")
	}
	if _, ok := l.get(int64(sentCap + 5)); !ok {
		t.Error("the newest post was forgotten")
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
