package reply

import (
	"context"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// A mention of another player in the question reaches the model as their
// name; the bot's own mention, and an account that is nobody's player,
// become nothing.
func TestMentionsInAQuestionBecomeNames(t *testing.T) {
	db := replyDB(t)
	var seen Prompt
	capture := interpreterFunc(func(_ context.Context, p Prompt) (Request, error) {
		seen = p
		return Request{Kind: KindUnknown}, nil
	})
	answer, _ := newAnswerer(t, db, capture)

	body := mentionPlaceholder + " how is " + mentionPlaceholder + " doing, and " + mentionPlaceholder + "?"
	if err := answer(context.Background(), senderUUID, body, "", []string{"", senderUUID, "nobody-in-particular"}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if seen.Question != "how is Bo doing, and ?" {
		t.Errorf("question = %q, want the bot's mention gone, Bo named, the stranger gone", seen.Question)
	}
}

// "Who usually posts first?", "can anyone still win?" and "who has the most
// 1s?" are about the group even when a claimed player asks them.
func TestGroupWideQuestionsStayGroupWideForAClaimedAsker(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()
	stamp(results, alma.ID, 7, 30)
	stamp(results, bo.ID, 21, 5)

	got := answer(translator(t, "en"), Request{Kind: KindHabits}, &bo, players, results, now)
	if !strings.HasPrefix(got, "Opens the day most often: Alma") {
		t.Errorf("habits from a claimed asker: %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindCatchup}, &bo, players, results, now)
	if !strings.HasPrefix(got, "September: Alma leads on 3.00") {
		t.Errorf("catch-up from a claimed asker: %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindCount, Guesses: 4}, &alma, players, results, now)
	if got != "Most 4s: Bo, 14." {
		t.Errorf("count from a claimed asker: %q", got)
	}
}

// A name that fits two players is nobody: the answer asks rather than
// picking whichever was listed first.
func TestAnAmbiguousNameIsNotGuessed(t *testing.T) {
	anna := store.Player{ID: 4, Slug: "anna", Name: "Anna", Active: true}
	annika := store.Player{ID: 5, Slug: "annika", Name: "Annika", Active: true}
	players := []store.Player{annika, anna}

	if _, ok := findPlayer("Ann", players); ok {
		t.Error("\"Ann\" matched one of Anna and Annika")
	}
	if p, ok := findPlayer("Anna", players); !ok || p.ID != anna.ID {
		t.Errorf("\"Anna\" = %v, %v; want Anna exactly", p, ok)
	}
	if p, ok := findPlayer("anni", players); !ok || p.ID != annika.ID {
		t.Errorf("\"anni\" = %v, %v; want Annika alone", p, ok)
	}
}

func TestAnAbsurdSpanIsAllTime(t *testing.T) {
	got, _ := parseRequest(`{"kind":"leader","span":"days","days":1000000000,"worst":false,"player":"","topic":"","date":"","month":"","guesses":0}`)
	if got.Span != SpanAll || got.Days != 0 {
		t.Errorf("a billion days = %+v, want all time", got)
	}
	got, _ = parseRequest(`{"kind":"leader","span":"days","days":365,"worst":false,"player":"","topic":"","date":"","month":"","guesses":0}`)
	if got.Span != SpanDays || got.Days != 365 {
		t.Errorf("a year = %+v, want kept", got)
	}
}

// A closed month is named mid-sentence in the 🏆 wording, where Swedish
// keeps the month lowercase.
func TestAClosedMonthKeepsTheCatalogueCase(t *testing.T) {
	players, results := closedMonths(t)
	got := answer(translator(t, "sv"), Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-07"}, nil, players, results, fixtureNow())
	if !strings.Contains(got, "tog juli") {
		t.Errorf("got %q, want the month lowercase mid-sentence", got)
	}
}
