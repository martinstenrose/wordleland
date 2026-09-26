package reply

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// The fixture is the 15th of a 30-day month, both players having played
// today: Alma on 3.00, Bo on 4.20 (one X). Bo has 15 days left and 63
// points scored; to finish under 3.00 over 30 days he needs 90 − 63 = 27
// over 15 days, an average of 1.80 — near-perfect rounds.
func TestCatchupForAChaser(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()

	got := answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Bo"}, nil, players, results, now)
	want := "September: Bo is 120 points behind Alma with 15 days left. Passing takes an average of 1.80 over those days — near-perfect rounds, but not impossible."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}

	// "Can I still win?" from the chaser.
	got = answer(translator(t, "sv"), Request{Kind: KindCatchup, Player: "Bo"}, &bo, players, results, now)
	want = "September: Bo ligger 120 punkter efter Alma med 15 dagar kvar. Att gå om kräver ett snitt på 1,80 över de dagarna — nästan perfekta rundor, men inte omöjligt."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// A closer chaser gets a plain answer with the assumption spelled out.
func TestCatchupWithinReach(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	// Cid: twelve 3s and three 4s, 48 over 15 days — 3.20, twenty points
	// behind. Needs 90 − 48 = 42 over 15 days: 2.80.
	results = append(results, play(t, cid.ID, current-14, current-3, 3, 0)...)
	results = append(results, play(t, cid.ID, current-2, current, 4, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Cid"}, nil, players, results, now)
	want := "September: Cid Larsson is 20 points behind Alma with 15 days left. An average of 2.80 or better over those days does it, if Alma keeps averaging 3.00."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestCatchupOutOfReach(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	// Cid on 6.00: needs 90 − 90 = 0 over 15 days.
	results = append(results, play(t, cid.ID, current-14, current, 6, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Cid"}, nil, players, results, now)
	want := "September: Cid Larsson is 300 points behind Alma with 15 days left — out of reach, even a 1 every day wouldn't do it."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The leader asking gets the same arithmetic from the other side.
func TestCatchupFromTheLeader(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()

	got := answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Alma"}, &alma, players, results, now)
	want := "Alma leads September by 120 points with 15 days left. Bo would need to average 1.80 or better to pass, if Alma keeps averaging 3.00."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Nobody named and the asker unknown: everyone's chances at once. Days left
// here are the days after today.
func TestCatchupForEveryone(t *testing.T) {
	players, results := fixture(t)
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	results = append(results, play(t, cid.ID, current-14, current, 6, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindCatchup}, nil, players, results, now)
	want := "September: Alma leads on 3.00 with 15 days left.\nStill in it: Bo (needs 1.80).\nOut of reach: Cid Larsson."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Today counts as a day left only for somebody who has not played it.
func TestCatchupCountsTodayForWhoeverHasNotPlayedIt(t *testing.T) {
	now := fixtureNow()
	current := wordle.PuzzleForDate(now)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	results := play(t, alma.ID, first, current, 3, 0)
	// Bo has not played today: 14 days scored, thirteen 4s and a 5, 57
	// points (4.07), and 16 days left including today. Needs 90 − 57 = 33
	// over 16: 2.06.
	results = append(results, play(t, bo.ID, first, current-2, 4, 0)...)
	results = append(results, play(t, bo.ID, current-1, current-1, 5, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Bo"}, nil, []store.Player{alma, bo}, results, now)
	want := "September: Bo is 107 points behind Alma with 16 days left. An average of 2.06 or better over those days does it, if Alma keeps averaging 3.00."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestCatchupWhenTheMonthIsPlayedOut(t *testing.T) {
	now := time.Date(2026, time.September, 30, 20, 0, 0, 0, time.Local)
	current := wordle.PuzzleForDate(now)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	results := append(play(t, alma.ID, first, current, 3, 0), play(t, bo.ID, first, current, 4, 0)...)

	got := answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Bo"}, nil, []store.Player{alma, bo}, results, now)
	if got != "September: Bo is 100 points behind Alma and the month is played out." {
		t.Errorf("got %q", got)
	}
	got = answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Alma"}, nil, []store.Player{alma, bo}, results, now)
	if got != "Alma leads September by 100 points with 0 days left: nobody can catch them now." {
		t.Errorf("got %q", got)
	}
}

func TestCatchupForSomebodyWhoHasNotPlayedTheMonth(t *testing.T) {
	players, results := fixture(t)
	got := answer(translator(t, "en"), Request{Kind: KindCatchup, Player: "Cid"}, nil, players, results, fixtureNow())
	if got != "Cid Larsson hasn't played in September yet." {
		t.Errorf("got %q", got)
	}
}
