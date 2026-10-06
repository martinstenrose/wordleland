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

// Praise of a player is not thanks to the bot: a "Tack!" back would be the
// bot taking their credit. Calling them the best is checked against the
// board, which here has Alma first on 3.00 and Bo behind her.
func TestPraiseOfAPlayerIsNotTakenAsThanks(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	for _, tc := range []struct{ question, asker, want string }{
		{"Alma är bäst! 👑", "Bo", "Stämmer! Alma toppar tabellen med 3,00 i snitt. 👑"},
		{"Bo är bäst!", "Alma", "Snyggt av Bo, men bäst är Alma med 3,00 i snitt. 😉"},
		{"Bo är bäst 😎", "Bo", "Djärvt påstått – bäst är Alma med 3,00 i snitt. 😉"},
		{"jag menar, Alma är kung", "Alma", "Svårt att säga emot – du toppar tabellen med 3,00 i snitt. 👑"},
		// Praise that claims nothing the board can settle is cheered.
		{"grym Bo!", "Alma", "Heja Bo! 👑"},
		{"grym Bo!", "Bo", "Självförtroendet är det inget fel på! 😄"},
		// Praise of the bot is still thanked.
		{"duktig bot!", "Bo", "Tack! 🙂"},
	} {
		req := ground(Request{Kind: KindThanks}, Prompt{Question: tc.question, Asker: tc.asker, Players: []string{"Alma", "Bo", "Cid Larsson"}})
		asker := bo
		if tc.asker == "Alma" {
			asker = alma
		}
		if got := answer(translator(t, "sv"), req, &asker, players, results, fixtureNow()); got != tc.want {
			t.Errorf("%q from %s: got %q, want %q", tc.question, tc.asker, got, tc.want)
		}
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
	for _, want := range []string{`"thanks"`, "asks nothing", "never the asker just because they are asking",
		// "Who is best?" is a career question; "who leads?" is the month's.
		`"vem är bäst?"`, "who is leading or winning when no",
		// The standings with no period are the board, all time.
		`"ställningarna"`} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}
