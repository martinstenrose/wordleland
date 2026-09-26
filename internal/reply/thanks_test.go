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
	if got != "Tack! 🙂" {
		t.Errorf("got %q", got)
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
		`"vem är bäst?"`, "who is leading or winning when no"} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}
