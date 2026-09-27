package reply

import (
	"strings"
	"testing"
)

// "Duktig bot" is not a question. It gets a thanks back, not the help
// line, and the model naming the sender on it does not make it about them.
func TestPraiseIsAnsweredInKind(t *testing.T) {
	players, results := fixture(t)
	got := answer(translator(t, "sv"), Request{Kind: KindThanks}, &bo, players, results, fixtureNow())
	if got != "Jag vet. 😎" {
		t.Errorf("got %q", got)
	}
}

// Praise gets one of a few answers, the same for the same words.
func TestPraiseIsAnsweredInMoreThanOneWay(t *testing.T) {
	t.Parallel()
	tr := translator(t, "en")
	seen := map[string]bool{}
	for _, praise := range []string{"good bot", "thanks!", "nice one", "tack", "duktig bot", "great", "love it"} {
		got := thanks(tr, praise)
		if got != thanks(tr, praise) {
			t.Errorf("%q got two different answers", praise)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Errorf("seven kinds of praise got %d answer: %v", len(seen), seen)
	}
}

func TestParseRequestDropsAPlayerWhereNoneCanBeMeant(t *testing.T) {
	for _, kind := range []Kind{KindThanks, KindUnknown, KindToday, KindRules, KindLeader} {
		got, err := parseRequest(`{"kind":"` + string(kind) + `","span":"month","days":0,"worst":false,"player":"Bo","topic":"","date":"","month":"","guesses":0}`)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got.Player != "" {
			t.Errorf("%s kept a player: %+v", kind, got)
		}
	}
	got, _ := parseRequest(`{"kind":"streak","span":"month","days":0,"worst":false,"player":"Bo","topic":"","date":"","month":"","guesses":0}`)
	if got.Player != "Bo" {
		t.Errorf("a streak question lost its player: %+v", got)
	}
}

func TestThePromptSaysANonQuestionIsNotAQuestion(t *testing.T) {
	system := systemPrompt(Prompt{Players: []string{"Alma"}})
	for _, want := range []string{`"thanks"`, "asks nothing", `Always ""`,
		// "Who is best?" is a career question; "who leads?" is the month's.
		`"vem är bäst?"`, "who is leading or winning when no",
		// The standings with no period are the board, all time.
		`"ställningarna"`} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}
