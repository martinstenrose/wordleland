package reply

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Every kind is asked about at least once: a kind the test never asks is
// one a model could lose without the test noticing.
func TestThePlacingTestCoversEveryKind(t *testing.T) {
	t.Parallel()
	asked := map[Kind]bool{}
	for _, c := range PlacingCases {
		asked[c.Want.Kind] = true
	}
	for _, k := range Kinds {
		if !asked[k] {
			t.Errorf("no placing case for kind %q", k)
		}
	}
}

// Each case's answer is one the parse would keep: a model that wrote it
// exactly would pass, so a miss is the model's and not the case's.
func TestThePlacingCasesAreReachable(t *testing.T) {
	t.Parallel()
	for _, c := range PlacingCases {
		written, err := json.Marshal(c.Want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseRequest(string(written))
		if err != nil {
			t.Fatalf("%s: %v", c.Question, err)
		}
		if misses := placingMisses(c, got); len(misses) > 0 {
			t.Errorf("%s: the case's own answer misses: %v", c.Question, misses)
		}
	}
}

// Only the kind and the named fields count; a versus is the same pair in
// either order, the asker standing in for an empty side.
func TestPlacingMisses(t *testing.T) {
	t.Parallel()
	c := PlacingCase{Want: Request{Kind: KindScore, Player: "Bo", Date: "2026-09-14", Span: SpanAll}, Check: "player date"}
	if m := placingMisses(c, Request{Kind: KindScore, Player: "bo", Date: "2026-09-14"}); len(m) != 0 {
		t.Errorf("an unchecked span or a name's case counted: %v", m)
	}
	if m := placingMisses(c, Request{Kind: KindScore, Player: "Bo"}); len(m) != 1 || !strings.HasPrefix(m[0], "date:") {
		t.Errorf("a missing date: %v", m)
	}
	if m := placingMisses(c, Request{Kind: KindDay}); len(m) != 1 || !strings.HasPrefix(m[0], "kind:") {
		t.Errorf("a wrong kind is one miss, whatever else: %v", m)
	}
	pair := PlacingCase{Want: Request{Kind: KindVersus, Player: "Bo"}, Check: "pair"}
	for _, got := range []Request{
		{Kind: KindVersus, Player: "Bo"},
		{Kind: KindVersus, Player: "Alma", Other: "Bo"},
		{Kind: KindVersus, Player: "Bo", Other: "Alma"},
	} {
		if m := placingMisses(pair, got); len(m) != 0 {
			t.Errorf("%+v: %v", got, m)
		}
	}
	if m := placingMisses(pair, Request{Kind: KindVersus, Player: "Cid"}); len(m) != 1 {
		t.Errorf("the wrong opponent passed: %v", m)
	}
}

// byQuestion answers each question from a table, the way a model would.
type byQuestion map[string]Request

func (b byQuestion) Interpret(_ context.Context, p Prompt) (Request, error) {
	return b[p.Question], nil
}

func TestRunPlacingScoresEachQuestion(t *testing.T) {
	t.Parallel()
	cases := PlacingCases[:3]
	model := byQuestion{cases[0].Question: cases[0].Want, cases[1].Question: {Kind: KindStanding}}
	var reported []string
	results := RunPlacing(context.Background(), model, cases, func(r PlacingResult) { reported = append(reported, r.Case.Question) })
	if len(results) != 3 || !reflect.DeepEqual(reported, []string{cases[0].Question, cases[1].Question, cases[2].Question}) {
		t.Fatalf("results %d, reported %v", len(results), reported)
	}
	if !results[0].Placed() || results[1].Placed() || results[2].Placed() {
		t.Errorf("placed: %v %v %v", results[0].Placed(), results[1].Placed(), results[2].Placed())
	}
}

// A model that writes "me" for the asker placed the question right: the bot
// reads it as the asker.
func TestPlacingReadsMeAsTheAsker(t *testing.T) {
	t.Parallel()
	c := PlacingCase{Want: Request{Kind: KindCount, Player: "Alma", Guesses: 2}, Check: "player guesses"}
	if m := placingMisses(c, Request{Kind: KindCount, Player: Asker, Guesses: 2}); len(m) != 0 {
		t.Errorf("\"me\" for the asker missed: %v", m)
	}
}
