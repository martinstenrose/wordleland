package stats

import (
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

func calloutOf(cs []Callout, kind string) (Callout, bool) {
	for _, c := range cs {
		if c.Kind == kind {
			return c, true
		}
	}
	return Callout{}, false
}

// Nothing clearing its threshold is a valid answer, and the view omits
// the card rather than padding it.
func TestNoCalloutsWhenNothingIsRemarkable(t *testing.T) {
	now := today(t)
	// Everyone flat at 4, no failures, no absences, no first-guess solves.
	var players []store.Player
	var results []store.BoardResult
	for i := 1; i <= 4; i++ {
		players = append(players, player(int64(i), "p"+string(rune('a'+i-1))))
		results = append(results, run(i, 1871, 1900, 4, false)...)
	}

	board := Compute(players, results, DefaultOptions(now))
	cs := ComputeCallouts(board, results, now)

	if _, ok := calloutOf(cs, CalloutOnForm); ok {
		t.Error("an on-form callout fired with every delta at zero")
	}
	if _, ok := calloutOf(cs, CalloutOffForm); ok {
		t.Error("an off-form callout fired with every delta at zero")
	}
	if _, ok := calloutOf(cs, CalloutOneAndDone); ok {
		t.Error("a first-guess callout fired with no first-guess solves")
	}
}

// A delta below the floor is noise and must not be published as a finding.
func TestFormCalloutsRespectTheSignificanceFloor(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "alma")}

	// 40 games at 4, then the last 30 shifted just barely. The form window
	// is the last 30 puzzles, so a small shift keeps the delta under 0.25.
	var results []store.BoardResult
	results = append(results, run(1, 1841, 1870, 4, false)...)
	results = append(results, run(1, 1871, 1900, 4, false)...)
	board := Compute(players, results, DefaultOptions(now))
	if cs := ComputeCallouts(board, results, now); len(cs) > 0 {
		for _, c := range cs {
			if c.Kind == CalloutOnForm || c.Kind == CalloutOffForm {
				t.Errorf("form callout %+v fired on a zero delta", c)
			}
		}
	}

	// Now a shift that clearly clears it: 5s historically, 3s lately.
	var big []store.BoardResult
	big = append(big, run(1, 1841, 1870, 6, false)...)
	big = append(big, run(1, 1871, 1900, 3, false)...)
	board = Compute(players, big, DefaultOptions(now))
	c, ok := calloutOf(ComputeCallouts(board, big, now), CalloutOnForm)
	if !ok {
		t.Fatal("no on-form callout for a player who improved by a wide margin")
	}
	if c.Slug != "alma" {
		t.Errorf("callout names %q", c.Slug)
	}
	if c.Value < Significance {
		t.Errorf("callout value %.2f is below the floor it supposedly cleared", c.Value)
	}
}

// The design asserts a streak reaches the start of the window without
// checking. A current streak that is not the player's longest is not
// "unbroken", and must not be reported as one.
func TestUnbrokenRequiresTheStreakToBeTheirLongest(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "alma")}

	// A long early run, then a break, then a shorter current run.
	var results []store.BoardResult
	results = append(results, run(1, 1830, 1869, 4, false)...)
	results = append(results, run(1, 1886, 1900, 4, false)...) // gap at 1870-1885
	board := Compute(players, results, DefaultOptions(now))

	if c, ok := calloutOf(ComputeCallouts(board, results, now), CalloutUnbroken); ok {
		t.Errorf("unbroken fired for a current streak shorter than their longest: %+v", c)
	}

	// An unbroken history does fire.
	whole := run(1, 1830, 1900, 4, false)
	board = Compute(players, whole, DefaultOptions(now))
	c, ok := calloutOf(ComputeCallouts(board, whole, now), CalloutUnbroken)
	if !ok {
		t.Fatal("unbroken did not fire for a player who has never missed")
	}
	if c.Count != 71 {
		t.Errorf("streak = %d, want 71", c.Count)
	}
}

// The design claims "the only" first-guess solve without counting them.
// The callout is bounded to the form window: over a long history there is
// always a first-guess solve somewhere, and a callout that always fires is
// decoration rather than a finding.
func TestOneAndDoneIgnoresSolvesOutsideTheWindow(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "alma")}

	results := run(1, 1871, 1900, 4, false)
	results = append(results, result(1, 1800, 1, false)) // long ago
	board := Compute(players, results, DefaultOptions(now))

	if c, ok := calloutOf(ComputeCallouts(board, results, now), CalloutOneAndDone); ok {
		t.Errorf("a first-guess solve from outside the window fired: %+v", c)
	}
}

func TestOneAndDoneCountsTheSolves(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "alma"), player(2, "bosse")}

	results := append(run(1, 1871, 1900, 4, false), run(2, 1871, 1900, 4, false)...)
	results = append(results, result(1, 1880, 1, false)) // one, by alma, inside the window
	board := Compute(players, results, DefaultOptions(now))

	c, ok := calloutOf(ComputeCallouts(board, results, now), CalloutOneAndDone)
	if !ok {
		t.Fatal("no callout for a first-guess solve")
	}
	if c.Count != 1 || c.Slug != "alma" {
		t.Errorf("callout = %+v, want one solve credited to alma", c)
	}

	// A second solve by someone else: the count rises and no single player
	// is named, so the copy cannot claim "the only".
	results = append(results, result(2, 1881, 1, false))
	board = Compute(players, results, DefaultOptions(now))
	c, _ = calloutOf(ComputeCallouts(board, results, now), CalloutOneAndDone)
	if c.Count != 2 {
		t.Errorf("Count = %d, want 2", c.Count)
	}
	if c.Slug != "" {
		t.Errorf("Slug = %q, want empty when more than one player has done it", c.Slug)
	}
}

// A retired player is not missing, they left. Saying so would be true
// forever and useful never.
func TestMissingSkipsRetiredPlayers(t *testing.T) {
	now := today(t)
	retired := player(1, "gone")
	retired.Active = false
	players := []store.Player{retired}

	results := run(1, 1820, 1860, 4, false)
	board := Compute(players, results, DefaultOptions(now))

	if c, ok := calloutOf(ComputeCallouts(board, results, now), CalloutMissing); ok {
		t.Errorf("missing fired for a retired player: %+v", c)
	}
}

// "Missing" is for regulars. Someone who tried it twice and drifted off is
// not a loss worth announcing.
func TestMissingOnlyFiresForARegular(t *testing.T) {
	now := today(t)
	players := []store.Player{player(1, "alma"), player(2, "cameo")}

	results := run(1, 1820, 1860, 4, false)                    // 41 games, gone since
	results = append(results, run(2, 1859, 1860, 4, false)...) // 2 games, also gone
	board := Compute(players, results, DefaultOptions(now))

	c, ok := calloutOf(ComputeCallouts(board, results, now), CalloutMissing)
	if !ok {
		t.Fatal("no missing callout for a regular who stopped")
	}
	if c.Slug != "alma" {
		t.Errorf("missing names %q, want the player with a real history", c.Slug)
	}
}
