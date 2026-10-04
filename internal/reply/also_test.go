package reply

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// A message that asks two things gets both answered, in one post, in the
// order asked.
func TestAMessageThatAsksTwoThingsGetsBothAnswers(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	req := Request{Kind: KindLeader, Span: SpanMonth,
		Also: []Request{{Kind: KindStreak, Span: SpanMonth, Player: "Bo"}}}
	answer, c := newAnswerer(t, db, canned{req: req})
	if err := answer(context.Background(), senderUUID, "who leads, and is my streak still going?", "", nil); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(c.last(t), "\n\n")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "📊") || !strings.HasPrefix(parts[1], "You're on ") {
		t.Errorf("got %q, want the leader and then Bo's streak", c.last(t))
	}
}

// Further questions are kept only where they are questions, once each, and
// at most two; a thank-you ahead of a question gives way to it.
func TestParseRequestFurtherQuestions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    Request
	}{
		{"two questions",
			`{"kind":"leader","span":"month","also":[{"kind":"streak","span":"month","player":"Bo","worst":true}]}`,
			Request{Kind: KindLeader, Span: SpanMonth, Also: []Request{{Kind: KindStreak, Span: SpanMonth, Player: "Bo"}}}},
		{"a thank-you and a question",
			`{"kind":"thanks","span":"month","also":[{"kind":"today","span":"month"}]}`,
			Request{Kind: KindToday, Span: SpanMonth}},
		{"the same question twice, and a greeting",
			`{"kind":"leader","span":"month","also":[{"kind":"leader","span":"month"},{"kind":"unknown","span":"month"}]}`,
			Request{Kind: KindLeader, Span: SpanMonth}},
		{"too many",
			`{"kind":"leader","span":"month","also":[{"kind":"streak","span":"month"},{"kind":"today","span":"month"},{"kind":"group","span":"month"}]}`,
			Request{Kind: KindLeader, Span: SpanMonth, Also: []Request{{Kind: KindStreak, Span: SpanMonth}, {Kind: KindToday, Span: SpanMonth}}}},
		{"nested further questions are dropped",
			`{"kind":"leader","span":"month","also":[{"kind":"today","span":"month","also":[{"kind":"group","span":"month"}]}]}`,
			Request{Kind: KindLeader, Span: SpanMonth, Also: []Request{{Kind: KindToday, Span: SpanMonth}}}},
	}
	for _, tc := range tests {
		got, err := parseRequest(tc.content)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\ngot  %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}
