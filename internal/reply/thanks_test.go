package reply

import (
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
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
// figures for the period it says, which here have Alma first on 3.00
// every day and Bo behind her; no period is the board, all time.
func TestPraiseOfAPlayerIsNotTakenAsThanks(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	for _, tc := range []struct{ question, asker, want string }{
		{"Alma är bäst! 👑", "Bo", "Stämmer! Bäst totalt: Alma, med 3,00 i snitt. 👑"},
		{"Bo är bäst!", "Alma", "Snyggt av Bo, men bäst totalt: Alma, med 3,00 i snitt. 😉"},
		{"Bo är bäst 😎", "Bo", "Djärvt påstått – bäst totalt: Alma, med 3,00 i snitt. 😉"},
		{"jag menar, Alma är kung", "Alma", "Svårt att säga emot – bäst totalt: du, med 3,00 i snitt. 👑"},
		// Praise that claims nothing the figures can settle is cheered.
		{"grym Bo!", "Alma", "Heja Bo! 👑"},
		{"grym Bo!", "Bo", "Självförtroendet är det inget fel på! 😄"},
		// More names are praise of them all, checked together.
		{"Alma och Bo är bäst!", "Cid Larsson", "Delvis rätt! Bäst totalt: Alma, med 3,00 i snitt. 👑"},
		{"Alma och Bo är grymma", "Cid Larsson", "Heja Alma och Bo! 👑"},
		{"Bo, Cid och Alma är bäst!", "Bo", "Delvis rätt! Bäst totalt: Alma, med 3,00 i snitt. 👑"},
		{"Bo och Cid är bäst!", "Alma", "Snyggt av Bo och Cid Larsson, men bäst totalt: Alma, med 3,00 i snitt. 😉"},
		{"Alma, Bo och Cid är grymma", "Bo", "Heja Alma, Bo och Cid Larsson! 👑"},
		// A period said is the period checked: a day by its guesses.
		{"Bo var bäst idag!", "Alma", "Snyggt av Bo, men bäst i dag: Alma, med 3 försök. 😉"},
		{"Alma var bäst igår", "Bo", "Stämmer! Bäst i går: Alma, med 3 försök. 👑"},
		{"Bo är bäst den här veckan", "Alma", "Snyggt av Bo, men bäst den här veckan: Alma, med 3,00 i snitt. 😉"},
		{"Alma är bäst den här månaden", "Bo", "Stämmer! Bäst i september: Alma, med 3,00 i snitt. 👑"},
		// A month by name is not one the answer checks: a cheer.
		{"Alma var bäst i augusti", "Bo", "Heja Alma! 👑"},
		// Praise of the bot is still thanked.
		{"duktig bot!", "Bo", "Tack! 🙂"},
	} {
		req := ground(Request{Kind: KindThanks}, Prompt{Question: tc.question, Asker: tc.asker,
			Players: []string{"Alma", "Bo", "Cid Larsson"}, Today: fixtureNow()})
		asker := map[string]store.Player{"Alma": alma, "Bo": bo, "Cid Larsson": cid}[tc.asker]
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

// "Är du smart eller dum?" is about the bot, and "Det förstod jag inte"
// is the one answer that settles it the wrong way.
func TestAQuestionAboutTheBotIsAnswered(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	got := answer(translator(t, "sv"), Request{Kind: KindBot}, &bo, players, results, fixtureNow())
	if got != "Smart nog att räkna snitt, dum nog att aldrig få se dagens ord. 🤖" {
		t.Errorf("got %q", got)
	}
}
