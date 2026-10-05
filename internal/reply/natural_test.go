package reply

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// closeRace is September with Alma on 3s every day and Bo on 3s but for
// one 4, seven points behind: a lead one bad day undoes. Cid has not
// played.
func closeRace(t *testing.T, now time.Time) ([]store.Player, []store.BoardResult) {
	t.Helper()
	current := wordle.PuzzleForDate(now)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	results := play(t, alma.ID, first, current, 3, 0)
	bos := play(t, bo.ID, first, current, 3, 0)
	bos[0].Guesses = 4
	return []store.Player{alma, bo, cid}, append(results, bos...)
}

// The month's lead gets one remark, the first that is true: the asker is
// the leader, the race is close with days to go, or today settles it.
func TestTheLeadGetsOneRemark(t *testing.T) {
	t.Parallel()
	mid := fixtureNow()
	last := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.Local)
	tests := []struct {
		name  string
		now   time.Time
		close bool
		asker *store.Player
		want  string
	}{
		{name: "a clear lead, asked by somebody else", now: mid, asker: &bo,
			want: "📊 September: Alma leads on 3.00 on average, 120 points clear of Bo."},
		{name: "the leader asks", now: mid, asker: &alma,
			want: "📊 September: Alma leads on 3.00 on average, 120 points clear of Bo. That's you. 👑"},
		{name: "close, with days to go", now: mid, close: true,
			want: "📊 September: Alma leads on 3.00 on average, 7 points clear of Bo. Close, with 15 days to go."},
		{name: "the month's last day", now: last,
			want: "📊 September: Alma leads on 3.00 on average, 110 points clear of Bo. Today settles it."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			players, results := fixtureAt(t, tc.now)
			if tc.close {
				players, results = closeRace(t, tc.now)
			}
			got := answer(translator(t, "en"), Request{Kind: KindLeader, Span: SpanMonth}, tc.asker, players, results, tc.now)
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}

	// A named month and a span other than the month are not the race the
	// remark is about.
	players, results := fixture(t)
	for _, req := range []Request{
		{Kind: KindLeader, Span: SpanMonth, Month: "2026-09"},
		{Kind: KindLeader, Span: SpanAll},
	} {
		if got := answer(translator(t, "en"), req, &alma, players, results, mid); got[len(got)-1] != '.' {
			t.Errorf("%+v: got %q, want no remark", req, got)
		}
	}
}

// fixtureAt is the answer tests' fixture, played through to now.
func fixtureAt(t *testing.T, now time.Time) ([]store.Player, []store.BoardResult) {
	t.Helper()
	current := wordle.PuzzleForDate(now)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	results := play(t, alma.ID, first, current, 3, 0)
	results = append(results, play(t, bo.ID, first, current, 4, wordle.PuzzleForDate(fixtureNow())-10)...)
	return []store.Player{alma, bo, cid}, results
}

// A place is a number; the gap to the place that matters is what makes it
// a race: the lead for a leader, the company of a shared lead, the points
// to the place above for anybody else.
func TestAStandingSaysTheGap(t *testing.T) {
	t.Parallel()
	now := fixtureNow()
	players, results := closeRace(t, now)
	tr := translator(t, "en")

	if got := answer(tr, Request{Kind: KindStanding, Span: SpanMonth, Player: "Alma"}, nil, players, results, now); got !=
		"Alma: place 1 of 2 (September), 3.00 on average over 15 games. 7 points clear of Bo." {
		t.Errorf("leader: %q", got)
	}
	if got := answer(tr, Request{Kind: KindStanding, Span: SpanMonth, Player: "Bo"}, nil, players, results, now); got !=
		"Bo: place 2 of 2 (September), 3.07 on average over 15 games. 7 points behind Alma." {
		t.Errorf("chaser: %q", got)
	}

	// Level at the top: Bo's 4 made a 3 as well.
	results = append([]store.BoardResult(nil), results...)
	for i := range results {
		results[i].Guesses = 3
	}
	if got := answer(tr, Request{Kind: KindStanding, Span: SpanMonth, Player: "Bo"}, &bo, players, results, now); got !=
		"You're in place 1 of 2 (September), 3.00 on average over 15 games. Level at the top with Alma." {
		t.Errorf("shared lead: %q", got)
	}
}

// Asked about themselves, a player is answered as "you" — not their own
// name read back to them.
func TestTheAskerIsAnsweredAsYou(t *testing.T) {
	t.Parallel()
	now := fixtureNow()
	players, results := fixture(t)
	failed := now.AddDate(0, 0, -10).Format(DateLayout)
	tr := translator(t, "en")
	tests := []struct {
		req  Request
		want string
	}{
		{Request{Kind: KindStreak, Player: "Bo"}, "You're on 10 days in a row, 10 at best."},
		{Request{Kind: KindScore, Player: "Bo", Date: failed}, "5 September was an X for you — not solved."},
		{Request{Kind: KindScore, Player: "Bo", Date: "2026-07-05"}, "You have no result for 5 July."},
		{Request{Kind: KindWins, Player: "Bo"}, "You've won 0 months."},
	}
	for _, tc := range tests {
		if got := answer(tr, tc.req, &bo, players, results, now); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.req.Kind, got, tc.want)
		}
	}
	// Swedish, the group's language.
	if got := answer(translator(t, "sv"), Request{Kind: KindScore}, &alma, players, results, now); got != "Du behövde 3 försök den 15 september." {
		t.Errorf("sv: got %q", got)
	}
}

// The bot says a stock line in its next wording each time: the same
// "thanks" twice running is what reads as a machine.
func TestAStockLineIsNotSaidTheSameTwiceRunning(t *testing.T) {
	t.Parallel()
	db := replyDB(t)
	answer, c := newAnswerer(t, db, canned{req: Request{Kind: KindThanks}})
	var said []string
	for range 2 {
		if err := answer(context.Background(), senderUUID, "good bot", "", nil); err != nil {
			t.Fatal(err)
		}
		said = append(said, c.last(t))
	}
	if said[0] == said[1] {
		t.Errorf("said %q twice running", said[0])
	}
}

// A gap too small to show in points is still a gap: "0 points behind"
// reads as level, which the places beside it say it is not.
func TestAGapThatRoundsToNothingIsNotZeroPoints(t *testing.T) {
	t.Parallel()
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	// Three hundred days of 3s each, but for one 4 of Bo's: a third of a
	// point apart, all time.
	results := play(t, alma.ID, current-299, current, 3, 0)
	bos := play(t, bo.ID, current-299, current, 3, 0)
	bos[0].Guesses = 4
	players, results := []store.Player{alma, bo}, append(results, bos...)
	tr := translator(t, "en")

	got := answer(tr, Request{Kind: KindStanding, Span: SpanAll, Player: "Bo"}, nil, players, results, now)
	if !strings.HasSuffix(got, "Just behind Alma.") || strings.Contains(got, "0 points") {
		t.Errorf("the chaser: %q", got)
	}
	got = answer(tr, Request{Kind: KindStanding, Span: SpanAll, Player: "Alma"}, nil, players, results, now)
	if !strings.HasSuffix(got, "Just ahead of Bo.") || strings.Contains(got, "0 points") {
		t.Errorf("the leader: %q", got)
	}
}
