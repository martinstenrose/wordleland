package reply

import (
	"context"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/i18n"
)

// A rules question is answered from the catalogue for its topic, in the
// group's language, and a rules question about nothing on the list gets
// the list.
func TestRulesAreExplainedFromTheCatalogue(t *testing.T) {
	players, results := fixture(t)

	got := answer(translator(t, "en"), Request{Kind: KindRules, Topic: TopicMiss}, nil, players, results, fixtureNow())
	if !strings.HasPrefix(got, "A miss is a day's puzzle you didn't post.") {
		t.Errorf("miss, en: %q", got)
	}
	got = answer(translator(t, "sv"), Request{Kind: KindRules, Topic: TopicStreak}, nil, players, results, fixtureNow())
	if !strings.HasPrefix(got, "En svit är lösta pussel i följd.") {
		t.Errorf("streak, sv: %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindRules}, nil, players, results, fixtureNow())
	if !strings.HasPrefix(got, "I can explain:") {
		t.Errorf("no topic: %q", got)
	}
}

// Every topic has a text in every language: a missing one would render as
// the key, which the i18n tests catch for the catalogues but not for the
// list of topics the code knows.
func TestEveryTopicHasAText(t *testing.T) {
	cats, err := i18n.Load()
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	for locale := range cats {
		tr := i18n.NewTranslator(cats, locale)
		for _, topic := range Topics {
			key := "reply.rules." + string(topic)
			if tr.T(key) == key {
				t.Errorf("%s has no text for %s", locale, topic)
			}
		}
	}
}

func TestParseRequestKeepsATopicOnlyForRules(t *testing.T) {
	got, err := parseRequest(`{"kind":"rules","span":"month","days":0,"player":"","topic":"hardmode"}`)
	if err != nil || got.Topic != TopicHardMode {
		t.Errorf("rules/hardmode: %+v, %v", got, err)
	}
	got, _ = parseRequest(`{"kind":"rules","span":"month","days":0,"player":"","topic":"weather"}`)
	if got.Kind != KindRules || got.Topic != "" {
		t.Errorf("rules about nothing known: %+v, want rules with no topic", got)
	}
	got, _ = parseRequest(`{"kind":"leader","span":"month","days":0,"player":"","topic":"miss"}`)
	if got.Topic != "" {
		t.Errorf("a topic survived on a non-rules request: %+v", got)
	}
}

// What the model is told is the whole of what it could ever repeat: player
// names, which the board shows anyone, the asker's name and the date. No
// results, no contact details, no account state — none of it is in the
// prompt, so no question can get it out.
func TestTheModelIsToldOnlyNamesAndTheDate(t *testing.T) {
	db := replyDB(t)
	var seen Prompt
	capture := interpreterFunc(func(_ context.Context, p Prompt) (Request, error) {
		seen = p
		return Request{Kind: KindUnknown}, nil
	})
	answer, _ := newAnswerer(t, db, capture)

	if err := answer(context.Background(), senderUUID, "what's Alma's email?"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if seen.Asker != "Bo" {
		t.Errorf("asker = %q, want the claimed player's name", seen.Asker)
	}
	if strings.Join(seen.Players, ",") != "Alma,Bo" {
		t.Errorf("players = %v, want the names and nothing else", seen.Players)
	}
	// The Prompt type is the contract: a new field here is a new thing the
	// model is told, and this test is where that is decided.
	_ = Prompt{Question: "", Asker: "", Players: nil, Today: seen.Today}
}

type interpreterFunc func(context.Context, Prompt) (Request, error)

func (f interpreterFunc) Interpret(ctx context.Context, p Prompt) (Request, error) { return f(ctx, p) }
