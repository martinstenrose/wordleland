package reply

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

const senderUUID = "b1d4e8a2-0000-4000-8000-0123456789ab"

// canned is an Interpreter that answers with one request, or one error.
type canned struct {
	req Request
	err error
}

func (c canned) Interpret(context.Context, Prompt) (Request, error) { return c.req, c.err }

type collector struct {
	mu   sync.Mutex
	sent []string
}

func (c *collector) send(_ context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, text)
	return nil
}

func (c *collector) last(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	return c.sent[len(c.sent)-1]
}

// replyDB is a migrated store with one player whose Signal identity is
// claimed, which is what makes the sender "somebody" to the answer.
func replyDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(ctx, db, store.Migrations()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	for _, name := range []string{"Alma", "Bo"} {
		if _, err := store.CreatePlayer(ctx, db, store.SystemActor(), name, strings.ToLower(name)); err != nil {
			t.Fatalf("CreatePlayer(%s): %v", name, err)
		}
	}
	// A result through the bridge's own path claims nothing; the identity
	// is linked the way the CLI does it, and the result filed by slug.
	bo, err := store.PlayerBySlug(ctx, db, "bo")
	if err != nil {
		t.Fatalf("PlayerBySlug: %v", err)
	}
	if _, err := store.LinkIdentity(ctx, db, store.SystemActor(), bo.ID, "signal", senderUUID, "", false); err != nil {
		t.Fatalf("LinkIdentity: %v", err)
	}
	current := wordle.PuzzleForDate(time.Now())
	for _, slug := range []string{"alma", "bo"} {
		for p := current - 11; p <= current; p++ {
			g := 3
			if slug == "bo" {
				g = 4
			}
			sub := ingest.Submission{Slug: slug, PuzzleNo: p, Solved: true, Guesses: &g}
			if _, err := ingest.Apply(ctx, db, store.SystemActor(), sub, false); err != nil {
				t.Fatalf("seed %s %d: %v", slug, p, err)
			}
		}
	}
	return db
}

func newAnswerer(t *testing.T, db *sql.DB, interp Interpreter) (func(context.Context, string, string, string) error, *collector) {
	t.Helper()
	cats, err := i18n.Load()
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	var c collector
	return New(db, cats, "en", interp, c.send, slog.New(slog.NewTextHandler(io.Discard, nil))), &c
}

// The sender's claimed identity is who "I" is.
func TestAnswersAboutTheAsker(t *testing.T) {
	db := replyDB(t)
	answer, c := newAnswerer(t, db, canned{req: Request{Kind: KindStanding, Span: SpanDays, Days: 7}})

	if err := answer(context.Background(), senderUUID, "how am I doing this week?", ""); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); !strings.HasPrefix(got, "Bo: place 2 of 2 (the last 7 days), 4.00 on average over 7 games.") {
		t.Errorf("got %q", got)
	}
}

func TestAnUnclaimedSenderIsAskedWhoTheyMean(t *testing.T) {
	db := replyDB(t)
	answer, c := newAnswerer(t, db, canned{req: Request{Kind: KindStanding, Span: SpanMonth}})

	if err := answer(context.Background(), "nobody-in-particular", "how am I doing?", ""); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); !strings.HasPrefix(got, "Who do you mean?") {
		t.Errorf("got %q", got)
	}
}

func TestABareMentionGetsTheHelpLine(t *testing.T) {
	db := replyDB(t)
	answer, c := newAnswerer(t, db, canned{err: errors.New("must not be asked")})

	if err := answer(context.Background(), senderUUID, "  ", ""); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); !strings.HasPrefix(got, "I can answer") {
		t.Errorf("got %q", got)
	}
}

// Not ready is an answer, not a failure.
func TestAModelStillLoadingSaysSo(t *testing.T) {
	db := replyDB(t)
	answer, c := newAnswerer(t, db, canned{err: ErrNotReady})

	if err := answer(context.Background(), senderUUID, "who leads?", ""); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got := c.last(t); !strings.Contains(got, "still getting set up") {
		t.Errorf("got %q", got)
	}
}

// A model that fails is reported, and the group is told something rather
// than left waiting for an answer that is not coming.
func TestAFailingModelIsReportedAndApologised(t *testing.T) {
	db := replyDB(t)
	answer, c := newAnswerer(t, db, canned{err: errors.New("connection refused")})

	err := answer(context.Background(), senderUUID, "who leads?", "")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v, want the model's failure", err)
	}
	if got := c.last(t); !strings.Contains(got, "couldn't work that one out") {
		t.Errorf("got %q", got)
	}
}
