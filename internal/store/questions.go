package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// UnansweredQuestion is one question the Signal bot could not place: what
// was asked and when. Never who — see the migration.
type UnansweredQuestion struct {
	ID       int64
	Question string
	AskedAt  time.Time
}

// maxQuestionLen bounds what is kept of a question. A question is a
// sentence; anything longer is a pasted message, and the first few hundred
// characters say what it was about.
const maxQuestionLen = 500

// RecordUnansweredQuestion keeps a question the bot could not answer, text
// only. Not written to the activity log: it is bookkeeping, not a change
// anybody made to the data.
func RecordUnansweredQuestion(ctx context.Context, q Querier, question string) error {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil
	}
	if utf8.RuneCountInString(question) > maxQuestionLen {
		runes := []rune(question)
		question = string(runes[:maxQuestionLen])
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO unanswered_questions (question) VALUES (?)`, question,
	); err != nil {
		return fmt.Errorf("record unanswered question: %w", err)
	}
	return nil
}

// ListUnansweredQuestions returns every kept question, newest first.
func ListUnansweredQuestions(ctx context.Context, q Querier) ([]UnansweredQuestion, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, question, asked_at FROM unanswered_questions ORDER BY asked_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list unanswered questions: %w", err)
	}
	defer rows.Close()

	var out []UnansweredQuestion
	for rows.Next() {
		var u UnansweredQuestion
		if err := rows.Scan(&u.ID, &u.Question, &u.AskedAt); err != nil {
			return nil, fmt.Errorf("scan unanswered question: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteExpiredUnansweredQuestions drops questions older than olderThan,
// reporting how many. Same shape as DeleteExpiredPendingResults: a
// non-positive window deletes nothing.
func DeleteExpiredUnansweredQuestions(ctx context.Context, q Querier, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	res, err := q.ExecContext(ctx,
		`DELETE FROM unanswered_questions WHERE asked_at < ?`, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("delete expired unanswered questions: %w", err)
	}
	return res.RowsAffected()
}
