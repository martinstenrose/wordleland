package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Record, list newest first, and sweep what is older than the window —
// with a backdated row written the way the janitor compares, so the
// comparison is like for like.
func TestUnansweredQuestionsAreKeptThenSwept(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db, Migrations()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := RecordUnansweredQuestion(ctx, db, "  can the bot dance?  "); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := RecordUnansweredQuestion(ctx, db, "vad heter Bos katt?"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := RecordUnansweredQuestion(ctx, db, "   "); err != nil {
		t.Fatalf("record blank: %v", err)
	}

	got, err := ListUnansweredQuestions(ctx, db)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d, want 2 (a blank question is nothing)", len(got))
	}
	if got[0].Question != "vad heter Bos katt?" || got[1].Question != "can the bot dance?" {
		t.Errorf("order/trim wrong: %q, %q", got[0].Question, got[1].Question)
	}
	if time.Since(got[0].AskedAt) > time.Minute {
		t.Errorf("asked_at = %v, want about now", got[0].AskedAt)
	}

	// Age one row past the window.
	if _, err := db.ExecContext(ctx,
		`UPDATE unanswered_questions SET asked_at = datetime('now', '-31 days') WHERE id = ?`, got[1].ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	n, err := DeleteExpiredUnansweredQuestions(ctx, db, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1", n)
	}
	if n, _ := DeleteExpiredUnansweredQuestions(ctx, db, 0); n != 0 {
		t.Errorf("a zero window swept %d", n)
	}
	got, _ = ListUnansweredQuestions(ctx, db)
	if len(got) != 1 || got[0].Question != "vad heter Bos katt?" {
		t.Errorf("after the sweep: %+v", got)
	}
}

func TestALongQuestionIsCut(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db, Migrations()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	long := strings.Repeat("å", 600)
	if err := RecordUnansweredQuestion(ctx, db, long); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, _ := ListUnansweredQuestions(ctx, db)
	if len(got) != 1 || len([]rune(got[0].Question)) != maxQuestionLen {
		t.Errorf("kept %d runes, want %d", len([]rune(got[0].Question)), maxQuestionLen)
	}
}
