package reply

import (
	"reflect"
	"strings"
	"testing"
)

// "3 or better" is the board's own column, and the fewest of something is
// asked about as often as the most.
func TestCountsOrBetterAndFewest(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	now := fixtureNow()
	tests := []struct {
		loc  string
		req  Request
		want string
	}{
		{"en", Request{Kind: KindCount, Player: "Alma", Guesses: 3, OrBetter: true},
			"Alma: 15 results of 3 or better out of 15 games (100%)."},
		{"sv", Request{Kind: KindCount, Player: "Bo", Guesses: 3, OrBetter: true},
			"Bo: 0 resultat på 3 eller bättre av 15 spel (0 %)."},
		{"en", Request{Kind: KindCount, Guesses: 4, OrBetter: true}, "Most results of 4 or better: Alma, 15."},
		{"en", Request{Kind: KindCount, Guesses: 2, OrBetter: true}, "Nobody has any results of 2 or better yet."},
		{"sv", Request{Kind: KindCount, Guesses: 7, Worst: true}, "Färst X: Alma, 0."},
	}
	for _, tc := range tests {
		if got := answer(translator(t, tc.loc), tc.req, nil, players, results, now); got != tc.want {
			t.Errorf("%+v:\ngot  %q\nwant %q", tc.req, got, tc.want)
		}
	}
}

// What the model writes for the new fields is kept only where it means
// something, and a puzzle number becomes its date here, not in the model.
func TestParseRequestNewFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content string
		want    Request
	}{
		{`{"kind":"score","span":"month","player":"Bo","puzzle":1904}`,
			Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2026-09-05"}},
		{`{"kind":"day","span":"month","puzzle":1904}`,
			Request{Kind: KindDay, Span: SpanMonth, Date: "2026-09-05"}},
		{`{"kind":"leader","span":"week","puzzle":1904,"date":"2026-09-05"}`,
			Request{Kind: KindLeader, Span: SpanWeek}},
		{`{"kind":"count","span":"month","player":"Bo","guesses":3,"orbetter":true}`,
			Request{Kind: KindCount, Span: SpanMonth, Player: "Bo", Guesses: 3, OrBetter: true}},
		{`{"kind":"count","span":"month","guesses":7,"orbetter":true,"worst":true}`,
			Request{Kind: KindCount, Span: SpanMonth, Guesses: 7, Worst: true}},
		{`{"kind":"versus","span":"lastweek","player":"Alma","other":"alma"}`,
			Request{Kind: KindVersus, Span: SpanLastWeek, Player: "Alma"}},
		{`{"kind":"versus","span":"all","player":"Alma","other":" Bo "}`,
			Request{Kind: KindVersus, Span: SpanAll, Player: "Alma", Other: "Bo"}},
		{`{"kind":"standing","span":"month","other":"Bo","scores":[{"player":"Bo","guesses":3}]}`,
			Request{Kind: KindStanding, Span: SpanMonth}},
		{`{"kind":"whatif","span":"month","player":"Bo","date":"2026-09-16","scores":[{"player":" Bo ","guesses":3},{"player":"Alma","guesses":9},{"player":"","guesses":2},{"player":"bo","guesses":5}]}`,
			Request{Kind: KindWhatIf, Span: SpanMonth, Date: "2026-09-16", Scores: []Hypothetical{{"Bo", 3}}}},
		{`{"kind":"records","span":"month","player":"Bo","worst":true}`,
			Request{Kind: KindRecords, Span: SpanMonth}},
		{`{"kind":"puzzles","span":"days","days":7,"worst":true,"month":"2026-08"}`,
			Request{Kind: KindPuzzles, Span: SpanMonth, Worst: true, Month: "2026-08"}},
	}
	for _, tc := range tests {
		got, err := parseRequest(tc.content)
		if err != nil {
			t.Errorf("%s: %v", tc.content, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\ngot  %+v\nwant %+v", tc.content, got, tc.want)
		}
	}
}

// A kind the model is not told about is one it cannot pick, and a span it
// is not told about one it cannot say.
func TestThePromptDescribesEveryKindAndSpan(t *testing.T) {
	t.Parallel()
	prompt := systemPrompt(Prompt{})
	for _, k := range Kinds {
		if !strings.Contains(prompt, `"`+string(k)+`"`) {
			t.Errorf("the prompt never mentions kind %q", k)
		}
	}
	for _, s := range Spans {
		if !strings.Contains(prompt, `"`+string(s)+`"`) {
			t.Errorf("the prompt never mentions span %q", s)
		}
	}
	for _, tone := range Tones {
		if !strings.Contains(prompt, `"`+string(tone)+`"`) {
			t.Errorf("the prompt never mentions tone %q", tone)
		}
	}
}

// "Hur många 2:or har vi?" is the group's total, and a pronoun for the
// asker is the asker: the model writes them where names should be.
func TestPronounsForTheGroupAndTheAsker(t *testing.T) {
	t.Parallel()
	players, results := fixture(t)
	now := fixtureNow()
	sv := translator(t, "sv")
	for _, tc := range []struct {
		content string
		want    string
	}{
		{`{"kind":"count","span":"all","player":"vi","guesses":4}`, "Gruppen: 14 4:or på 30 spel (47 %)."},
		{`{"kind":"count","span":"all","player":"gruppen","guesses":3,"orbetter":true}`, "Gruppen: 15 resultat på 3 eller bättre av 30 spel (50 %)."},
		{`{"kind":"count","span":"all","player":"we"}`, "Gruppen över 30 spel: 0×1, 0×2, 15×3, 14×4, 0×5, 0×6, 1×X."},
		{`{"kind":"count","span":"all","player":"me","guesses":3}`, "Alma: 15 3:or av 15 spel (100 %)."},
		{`{"kind":"streak","span":"all","player":"jag"}`, "Alma: 15 dagar i rad nu, som bäst 15."},
		{`{"kind":"streak","span":"all","player":"vi"}`, "🔥 Längsta pågående svit: Alma, 15 dagar."},
	} {
		req, err := parseRequest(tc.content)
		if err != nil {
			t.Fatal(err)
		}
		got := answer(sv, withAsker(req, &alma), &alma, players, results, now)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.content, got, tc.want)
		}
	}
	// Nobody asking: "me" is nobody, and the answer asks who.
	req, _ := parseRequest(`{"kind":"count","span":"all","player":"mig","guesses":3}`)
	if got := answer(sv, withAsker(req, nil), nil, players, results, now); !strings.HasPrefix(got, "Vem menar du?") {
		t.Errorf("\"me\" with nobody asking: %q", got)
	}
}
