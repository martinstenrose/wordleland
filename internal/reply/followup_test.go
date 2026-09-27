package reply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// Asking what the bot can do is help, answered with the list; a question it
// cannot place gets one short line pointing at that.
func TestHelpIsTheListAndUnknownIsOneLine(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()

	if got := answer(translator(t, "sv"), Request{Kind: KindHelp}, nil, players, results, now); !strings.HasPrefix(got, "Jag kan svara på") {
		t.Errorf("help: %q", got)
	}
	got := answer(translator(t, "sv"), Request{Kind: KindUnknown}, nil, players, results, now)
	if got != "Det förstod jag inte. Fråga, så berättar jag vad jag kan svara på." {
		t.Errorf("unknown: %q", got)
	}
}

// Nobody named on a standing question is the whole table.
func TestStandingForNobodyIsTheWholeTable(t *testing.T) {
	players, results := fixture(t)
	got := answer(translator(t, "sv"), Request{Kind: KindStanding, Span: SpanMonth}, &bo, players, results, fixtureNow())
	if got != "September:\n1. Alma 3,00\n2. Bo 4,20" {
		t.Errorf("got %q", got)
	}
}

// Every rule text is chat length now: two sentences, well under a screen.
func TestRuleTextsAreShort(t *testing.T) {
	tr := translator(t, "en")
	for _, topic := range Topics {
		text := tr.T("reply.rules." + string(topic))
		if len(text) > 170 {
			t.Errorf("%s is %d characters, want chat length: %q", topic, len(text), text)
		}
	}
}

// A question the model could not place is kept, text only; help, thanks
// and answered questions are not.
func TestUnplacedQuestionsAreKept(t *testing.T) {
	db := replyDB(t)
	ctx := context.Background()

	ask := func(interp Interpreter, question string) {
		t.Helper()
		answer, _ := newAnswerer(t, db, interp)
		_ = answer(ctx, senderUUID, question, "", nil)
	}
	ask(canned{req: Request{Kind: KindUnknown}}, "can you order pizza?")
	ask(canned{req: Request{Kind: KindHelp}}, "what can you do?")
	ask(canned{req: Request{Kind: KindThanks}}, "good bot")
	ask(canned{req: Request{Kind: KindStreak}}, "who has the longest streak?")
	ask(canned{err: errors.New("model produced garbage")}, "vem är bäst på pingis?")

	kept, err := store.ListUnansweredQuestions(ctx, db)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var texts []string
	for _, q := range kept {
		texts = append(texts, q.Question)
	}
	if got := strings.Join(texts, "|"); got != "vem är bäst på pingis?|can you order pizza?" {
		t.Errorf("kept %q, want the unplaced question and the failed one, newest first", got)
	}
}
