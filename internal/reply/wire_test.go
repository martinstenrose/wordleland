package reply

import (
	"reflect"
	"testing"
	"time"
)

// The model's words for a day, a month and a span are counted from the day
// the question was asked: the arithmetic is here, not the model's.
func TestTheModelsWordsBecomeDatesHere(t *testing.T) {
	t.Parallel()
	// A Tuesday.
	today := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		content string
		want    Request
	}{
		{`{"kind":"score","player":"Bo","date":"yesterday"}`, Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2026-09-14"}},
		{`{"kind":"score","player":"Bo","date":"daybeforeyesterday"}`, Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2026-09-13"}},
		// Today is the default day, however it is written.
		{`{"kind":"day","date":"today"}`, Request{Kind: KindDay, Span: SpanMonth}},
		{`{"kind":"day","date":"2026-09-15"}`, Request{Kind: KindDay, Span: SpanMonth}},
		{`{"kind":"day","date":""}`, Request{Kind: KindDay, Span: SpanMonth}},
		// "I fredags" is the Friday before; on a Tuesday, last Tuesday is a
		// week ago.
		{`{"kind":"day","date":"friday"}`, Request{Kind: KindDay, Span: SpanMonth, Date: "2026-09-11"}},
		{`{"kind":"day","date":"tuesday"}`, Request{Kind: KindDay, Span: SpanMonth, Date: "2026-09-08"}},
		// A what-if's days are still to come.
		{`{"kind":"whatif","scores":[{"player":"Bo","guesses":3}],"date":"friday"}`,
			Request{Kind: KindWhatIf, Span: SpanMonth, Date: "2026-09-18", Scores: []Hypothetical{{"Bo", 3}}}},
		{`{"kind":"whatif","scores":[{"player":"Bo","guesses":3}],"date":"tomorrow"}`,
			Request{Kind: KindWhatIf, Span: SpanMonth, Date: "2026-09-16", Scores: []Hypothetical{{"Bo", 3}}}},
		// A day of a month is the last time it came round; a year the
		// model made up gives way to that, and one that has happened stands.
		{`{"kind":"score","player":"Bo","date":"09-03"}`, Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2026-09-03"}},
		{`{"kind":"score","player":"Bo","date":"12-24"}`, Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2025-12-24"}},
		{`{"kind":"score","player":"Bo","date":"3000-09-03"}`, Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2026-09-03"}},
		{`{"kind":"score","player":"Bo","date":"2025-07-05"}`, Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2025-07-05"}},
		{`{"kind":"whatif","scores":[{"player":"Bo","guesses":3}],"date":"09-20"}`,
			Request{Kind: KindWhatIf, Span: SpanMonth, Date: "2026-09-20", Scores: []Hypothetical{{"Bo", 3}}}},
		// A puzzle by its number is that puzzle's day.
		{`{"kind":"score","player":"Bo","date":"1900"}`, Request{Kind: KindScore, Span: SpanMonth, Player: "Bo", Date: "2026-09-01"}},
		// A month by name is the last one of that name; the running month
		// is no named month at all.
		{`{"kind":"leader","span":"august","worst":false}`, Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-08"}},
		{`{"kind":"leader","span":"lastmonth","worst":false}`, Request{Kind: KindLeader, Span: SpanMonth, Month: "2026-08"}},
		{`{"kind":"leader","span":"october","worst":false}`, Request{Kind: KindLeader, Span: SpanMonth, Month: "2025-10"}},
		{`{"kind":"leader","span":"september","worst":false}`, Request{Kind: KindLeader, Span: SpanMonth}},
		{`{"kind":"leader","span":"2025-07","worst":false}`, Request{Kind: KindLeader, Span: SpanMonth, Month: "2025-07"}},
		{`{"kind":"catchup","player":"anyone","month":"september"}`, Request{Kind: KindCatchup, Span: SpanMonth}},
		{`{"kind":"wins","player":"anyone","month":"none"}`, Request{Kind: KindWins, Span: SpanMonth}},
		{`{"kind":"wins","player":"anyone","month":"june"}`, Request{Kind: KindWins, Span: SpanMonth, Month: "2026-06"}},
		// Days as one word.
		{`{"kind":"standing","player":"Bo","span":"14d"}`, Request{Kind: KindStanding, Span: SpanDays, Days: 14, Player: "Bo"}},
		{`{"kind":"leader","span":"lastweek","worst":true}`, Request{Kind: KindLeader, Span: SpanLastWeek, Worst: true}},
		// A kind's own name for the other end.
		{`{"kind":"puzzles","span":"month","easiest":true}`, Request{Kind: KindPuzzles, Span: SpanMonth, Worst: true}},
		{`{"kind":"count","player":"anyone","guesses":7,"orbetter":false,"fewest":true}`,
			Request{Kind: KindCount, Span: SpanMonth, Guesses: 7, Worst: true}},
		{`{"kind":"leader","span":"month","worst":false,"also":[{"kind":"puzzles","span":"august","easiest":true}]}`,
			Request{Kind: KindLeader, Span: SpanMonth, Also: []Request{{Kind: KindPuzzles, Span: SpanMonth, Month: "2026-08", Worst: true}}}},
	}
	for _, tc := range tests {
		got, err := parseRequestAt(tc.content, today)
		if err != nil {
			t.Fatalf("%s: %v", tc.content, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s\n got %+v\nwant %+v", tc.content, got, tc.want)
		}
	}
	// With no day to count from, a word names nothing and the default
	// stands: nothing is guessed.
	got, _ := parseRequest(`{"kind":"score","player":"Bo","date":"yesterday"}`)
	if got.Date != "" {
		t.Errorf("a word with no today became %q", got.Date)
	}
}
