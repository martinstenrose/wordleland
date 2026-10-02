package reply

import (
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// A question said with feeling is met in kind, and the line in kind is as
// true as the figures under it: a brag is a bold claim only from somebody
// who is not on top.
func TestAnAnswerMeetsTheTone(t *testing.T) {
	t.Parallel()
	players, results := fixture(t) // Alma leads September; Bo is second.
	now := fixtureNow()
	tests := []struct {
		name  string
		req   Request
		asker *store.Player
		want  string // the line the answer opens with, "" for none
	}{
		{"the leader brags", Request{Kind: KindLeader, Span: SpanMonth, Tone: ToneBoast}, &alma, "Fair enough."},
		{"the chaser brags", Request{Kind: KindLeader, Span: SpanMonth, Tone: ToneBoast}, &bo, "Bold claim."},
		{"the leader worries", Request{Kind: KindCatchup, Player: "Alma", Tone: ToneWorried}, &alma, "No need to worry."},
		{"the chaser worries", Request{Kind: KindStanding, Span: SpanMonth, Player: "Bo", Tone: ToneWorried}, &bo, "Hang in there."},
		{"a tease about somebody else", Request{Kind: KindStanding, Player: "Bo", Tone: ToneTease}, &alma, "Be nice. 😄"},
		{"a tease about oneself", Request{Kind: KindStanding, Player: "Bo", Tone: ToneTease}, &bo, ""},
		{"a brag about something that is not the race", Request{Kind: KindStreak, Player: "Bo", Tone: ToneBoast}, &bo, ""},
		{"a brag with nobody to judge", Request{Kind: KindLeader, Span: SpanMonth, Tone: ToneBoast}, nil, ""},
		{"a brag from somebody with no place", Request{Kind: KindLeader, Span: SpanMonth, Tone: ToneBoast}, &cid, ""},
		{"asked plainly", Request{Kind: KindLeader, Span: SpanMonth}, &bo, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toneLine(translator(t, "en"), tc.req, tc.asker, players, results, now)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The line in kind opens the post, on its own line, and the answer under
// it is the one a plain question gets.
func TestTheToneLineOpensThePost(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	plain, c := newAnswerer(t, db, canned{req: Request{Kind: KindLeader, Span: SpanMonth}})
	if err := plain(t.Context(), senderUUID, "vem leder?", "", nil); err != nil {
		t.Fatal(err)
	}
	want := c.last(t)
	boast, c := newAnswerer(t, db, canned{req: Request{Kind: KindLeader, Span: SpanMonth, Tone: ToneBoast}})
	if err := boast(t.Context(), senderUUID, "jag leder väl? 😎", "", nil); err != nil {
		t.Fatal(err)
	}
	// The sender is Bo, behind Alma.
	if got := c.last(t); got != "Bold claim.\n"+want {
		t.Errorf("got %q, want the plain answer under %q", got, "Bold claim.")
	}
}

// A tone the model made up is no tone, and the further questions of a
// message take none of their own: it is the message that was said so.
func TestParseRequestKeepsOnlyAKnownTone(t *testing.T) {
	t.Parallel()
	for content, want := range map[string]Tone{
		`{"kind":"leader","tone":"boast"}`:                                              ToneBoast,
		`{"kind":"leader","tone":"plain"}`:                                              "",
		`{"kind":"leader","tone":"furious"}`:                                            "",
		`{"kind":"thanks","tone":"worried","also":[{"kind":"catchup","tone":"boast"}]}`: ToneWorried,
	} {
		got, err := parseRequest(content)
		if err != nil {
			t.Fatal(err)
		}
		if got.Tone != want {
			t.Errorf("%s: tone %q, want %q", content, got.Tone, want)
		}
		for _, a := range got.Also {
			if a.Tone != "" {
				t.Errorf("%s: a further question has tone %q", content, a.Tone)
			}
		}
	}
	if !strings.Contains(systemPrompt(Prompt{}), "never changes the kind") {
		t.Error("the prompt does not say the tone leaves the request alone")
	}
}
