package reply

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The question before is what a follow-up is read against: the last one
// the bot answered, its last part when it had several, and not a
// thank-you or a question it could not place.
func TestAFollowUpIsToldTheQuestionBefore(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	var next Request
	var seen []*Request
	capture := interpreterFunc(func(_ context.Context, p Prompt) (Request, error) {
		seen = append(seen, p.Previous)
		return next, nil
	})
	answer, _ := newAnswerer(t, db, capture)
	ask := func(req Request) {
		t.Helper()
		next = req
		if err := answer(context.Background(), senderUUID, "…", "", nil); err != nil {
			t.Fatal(err)
		}
	}

	ask(Request{Kind: KindLeader, Span: SpanMonth, Also: []Request{{Kind: KindStreak, Player: "Bo"}}})
	ask(Request{Kind: KindThanks})
	ask(Request{Kind: KindUnknown})
	ask(Request{Kind: KindStreak, Player: "Alma"})

	if seen[0] != nil {
		t.Errorf("the first question was told %+v", seen[0])
	}
	streak := Request{Kind: KindStreak, Player: "Bo"}
	for i := 1; i <= 3; i++ {
		if seen[i] == nil || Describe(*seen[i]) != Describe(streak) {
			t.Errorf("question %d was told %v, want the streak, the last part of the first", i+1, seen[i])
		}
	}
}

// A chat moves on: what was asked longer ago than the window is nothing
// to follow.
func TestTheQuestionBeforeIsForgottenAfterAWhile(t *testing.T) {
	t.Parallel()
	var last lastAnswered
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.Local)
	last.remember(Request{Kind: KindLeader, Span: SpanMonth}, now)
	if last.recall(now.Add(followUpWindow)) == nil {
		t.Error("forgotten within the window")
	}
	if got := last.recall(now.Add(followUpWindow + time.Second)); got != nil {
		t.Errorf("remembered %+v after the window", got)
	}
}

// The model is shown the request before after the fixed instructions,
// which the server reuses only up to the first difference, and not at all
// when there is none.
func TestThePromptCarriesTheQuestionBeforeLast(t *testing.T) {
	t.Parallel()
	p := PlacingPrompt("och Bo då?")
	with := systemPrompt(p)
	described := inWords(*p.Previous)
	at := strings.Index(with, described)
	if at < 0 {
		t.Fatalf("the prompt does not carry %s:\n%s", described, with)
	}
	if fixed := strings.Index(with, "Today is "); at < fixed {
		t.Error("the question before comes ahead of what changes once a day")
	}
	p.Previous = nil
	if without := systemPrompt(p); strings.Contains(without, "follow-up") {
		t.Errorf("a prompt with nothing before still speaks of a follow-up:\n%s", without)
	}
}

// A fragment — "och Bo då?", "den här veckan då?" — is the question before
// it with one thing changed, whatever kind the model came back with; a
// question of its own is not bent by the one before.
func TestAFragmentKeepsTheQuestionBefore(t *testing.T) {
	t.Parallel()
	players := []string{"Alma", "Bo", "Cid", "Dana"}
	standing := Request{Kind: KindStanding, Span: SpanAll, Player: "Alma"}
	tests := []struct {
		question string
		previous Request
		read     Request
		want     Request
	}{
		// The model changed the kind: the kind before stands, with the
		// span the fragment gave.
		{"den här veckan då?", standing, Request{Kind: KindLeader, Span: SpanWeek},
			Request{Kind: KindStanding, Span: SpanWeek, Player: "Alma"}},
		{"och förra månaden?", standing, Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-08"},
			Request{Kind: KindStanding, Span: SpanMonth, Month: "2026-08", Player: "Alma"}},
		// It named somebody: they are who it is about.
		{"och Bo då?", standing, Request{Kind: KindVersus, Span: SpanAll, Player: "Bo"},
			Request{Kind: KindStanding, Span: SpanAll, Player: "Bo"}},
		{"Dana då?", Request{Kind: KindCount, Player: "Bo", Guesses: 2}, Request{Kind: KindUnknown},
			Request{Kind: KindCount, Player: "Dana", Guesses: 2}},
		// It said "I": the asker, whoever was asked about before.
		{"och jag då?", Request{Kind: KindStreak, Player: "Bo"}, Request{Kind: KindStreak, Player: "Bo"},
			Request{Kind: KindStreak, Player: Asker}},
		// It named nobody: still about whoever it was about.
		{"och igår?", Request{Kind: KindScore, Player: "Bo"}, Request{Kind: KindScore, Date: "2026-09-14"},
			Request{Kind: KindScore, Player: "Bo", Date: "2026-09-14"}},
		{"what about all time?", Request{Kind: KindLeader, Span: SpanMonth}, Request{Kind: KindLeader, Span: SpanAll},
			Request{Kind: KindLeader, Span: SpanAll}},
		// A question of its own is read as it stands.
		{"vad fick Dana igår?", Request{Kind: KindVersus, Player: "Bo"}, Request{Kind: KindScore, Player: "Dana", Date: "2026-09-14"},
			Request{Kind: KindScore, Player: "Dana", Date: "2026-09-14"}},
		{"och vem leder?", standing, Request{Kind: KindLeader, Span: SpanMonth},
			Request{Kind: KindLeader, Span: SpanMonth}},
		{"hur många 3:or har Bo då?", standing, Request{Kind: KindCount, Player: "Bo", Guesses: 3},
			Request{Kind: KindCount, Player: "Bo", Guesses: 3}},
	}
	for _, tc := range tests {
		got := ground(tc.read, Prompt{Question: tc.question, Asker: "Alma", Players: players, Previous: &tc.previous})
		if Describe(got) != Describe(tc.want) {
			t.Errorf("%s after %s, read as %s\n got %s\nwant %s", tc.question,
				Describe(tc.previous), Describe(tc.read), Describe(got), Describe(tc.want))
		}
	}
	// With no question before, a fragment is whatever the model read.
	got := ground(Request{Kind: KindLeader, Span: SpanWeek}, Prompt{Question: "den här veckan då?", Asker: "Alma", Players: players})
	if got.Kind != KindLeader {
		t.Errorf("a fragment after nothing became %s", Describe(got))
	}
}

// The model is shown the request before in the words it writes itself.
func TestTheRequestBeforeIsShownInTheModelsWords(t *testing.T) {
	t.Parallel()
	for want, r := range map[string]Request{
		`{"kind":"leader","span":"2026-08","worst":false}`:                          {Kind: KindLeader, Span: SpanMonth, Month: "2026-08"},
		`{"kind":"standing","player":"anyone","span":"14d"}`:                        {Kind: KindStanding, Span: SpanDays, Days: 14},
		`{"kind":"count","player":"Bo","guesses":2,"orbetter":false,"fewest":true}`: {Kind: KindCount, Player: "Bo", Guesses: 2, Worst: true},
		`{"kind":"score","player":"Bo","date":"2026-09-14"}`:                        {Kind: KindScore, Player: "Bo", Date: "2026-09-14"},
		`{"kind":"puzzles","span":"all","easiest":true}`:                            {Kind: KindPuzzles, Span: SpanAll, Worst: true},
		`{"kind":"today"}`: {Kind: KindToday, Span: SpanMonth},
	} {
		if got := inWords(r); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		// And it reads back as the request it was.
		back, err := parseRequest(want)
		if err != nil || Describe(back) != Describe(normalise(r)) {
			t.Errorf("%s reads back as %s, want %s (%v)", want, Describe(back), Describe(normalise(r)), err)
		}
	}
}

// A follow-up to a question about a month, or about the asker, is still
// about them without naming them again.
func TestAFollowUpInheritsWhatItDoesNotSay(t *testing.T) {
	t.Parallel()
	previous := Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-08"}
	got := ground(Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-08", Worst: true},
		Prompt{Question: "och sämst?", Previous: &previous})
	if got.Month != "2026-08" || !got.Worst {
		t.Errorf("a follow-up lost its month: %s", Describe(got))
	}
	previous = Request{Kind: KindStanding, Span: SpanAll, Player: "Alma"}
	got = ground(Request{Kind: KindStanding, Span: SpanWeek, Player: Asker},
		Prompt{Question: "hur var det den här veckan?", Asker: "Alma", Players: []string{"Alma", "Bo"}, Previous: &previous})
	if got.Player != Asker {
		t.Errorf("a follow-up to a question about the asker lost them: %s", Describe(got))
	}
}
