package stats

import (
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

func traitOf(t *testing.T, players []store.Player, results []store.BoardResult, slug string) string {
	t.Helper()
	board := Compute(players, results, DefaultOptions(today(t)))
	return Trait(find(t, board, slug))
}

// State of play beats form: a trait about somebody's recent shape would
// be misleading for a player who has stopped.
func TestTraitReportsStateOfPlayFirst(t *testing.T) {
	tests := []struct {
		name    string
		results func() []store.BoardResult
		want    string
	}{
		{"never played", func() []store.BoardResult { return nil }, TraitGhost},
		{"three games", func() []store.BoardResult { return run(1, 1898, 1900, 3, false) }, TraitNewcomer},
		{"stopped in the spring", func() []store.BoardResult { return run(1, 1800, 1840, 3, false) }, TraitLapsed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			players := []store.Player{player(1, "alma")}
			if got := traitOf(t, players, tt.results(), "alma"); got != tt.want {
				t.Errorf("Trait() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTraitForHardModeAndStreaks(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	// Hard mode throughout.
	purist := run(1, 1861, 1900, 4, true)
	if got := traitOf(t, players, purist, "alma"); got != TraitPurist {
		t.Errorf("Trait() = %q, want %q", got, TraitPurist)
	}

	// A long unbroken run in ordinary mode.
	iron := run(1, 1851, 1900, 4, false)
	if got := traitOf(t, players, iron, "alma"); got != TraitIronman {
		t.Errorf("Trait() = %q, want %q", got, TraitIronman)
	}
}

// A first-guess solve is the rarest thing on the board.
// Counted over a whole history a first-guess solve stops being rare, and a
// label most of the roster carries says nothing about any of them.
func TestSniperIgnoresOldFirstGuessSolves(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	results := run(1, 1855, 1900, 4, false)
	results = append(results, result(1, 1820, 1, false)) // long before the window

	if got := traitOf(t, players, results, "alma"); got == TraitSniper {
		t.Error("a first-guess solve from outside the window earned the label")
	}
}

func TestTraitForAFirstGuessSolve(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	// Short of the streak threshold, and flat enough that no form swing
	// fires, so the sniper rule is what is being tested.
	results := run(1, 1880, 1900, 4, false)
	results = append(results, run(1, 1855, 1877, 4, false)...)
	results = append(results, result(1, 1878, 1, false))

	if got := traitOf(t, players, results, "alma"); got != TraitSniper {
		t.Errorf("Trait() = %q, want %q", got, TraitSniper)
	}
}

// Steadiness and volatility are about spread, not average: two players can
// share an average and deserve opposite names.
func TestTraitSeparatesSteadyFromVolatile(t *testing.T) {
	players := []store.Player{player(1, "steady"), player(2, "wild"), player(3, "middling")}

	var results []store.BoardResult
	// Four every day: no spread at all.
	results = append(results, run(1, 1877, 1900, 4, false)...)
	// The same average, reached by alternating extremes.
	for p := 1877; p <= 1900; p++ {
		score := 2
		if p%2 == 0 {
			score = 6
		}
		results = append(results, result(2, p, score, false))
	}
	// A third player in the middle, so the group has a middle that is not
	// one of the two being tested.
	for i, p := 0, 1877; p <= 1900; i, p = i+1, p+1 {
		results = append(results, result(3, p, []int{3, 4, 5, 4}[i%4], false))
	}

	board := Compute(players, results, DefaultOptions(today(t)))
	traits := NewTraiter(board)
	steady, wild := find(t, board, "steady"), find(t, board, "wild")

	// Steadiness is a comparison, so the group is what it is measured
	// against — a lone player is neither steady nor erratic.
	if got := traits.For(steady); got != TraitMetronome {
		t.Errorf("steady player = %q, want %q", got, TraitMetronome)
	}
	if got := traits.For(wild); got != TraitWildcard {
		t.Errorf("volatile player = %q, want %q", got, TraitWildcard)
	}
	// The point of the pair: their averages match, so only spread can be
	// telling them apart.
	if a, b := deref(t, steady.Average), deref(t, wild.Average); a != b {
		t.Fatalf("fixture drifted: averages %v and %v differ", a, b)
	}
}

func TestTraitForFormSwings(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	var climbing []store.BoardResult
	climbing = append(climbing, run(1, 1841, 1870, 6, false)...)
	climbing = append(climbing, run(1, 1871, 1900, 3, false)...)
	if got := traitOf(t, players, climbing, "alma"); got != TraitClimbing {
		t.Errorf("improving player = %q, want %q", got, TraitClimbing)
	}

	var slipping []store.BoardResult
	slipping = append(slipping, run(1, 1841, 1870, 3, false)...)
	slipping = append(slipping, run(1, 1871, 1900, 6, false)...)
	if got := traitOf(t, players, slipping, "alma"); got != TraitSlipping {
		t.Errorf("declining player = %q, want %q", got, TraitSlipping)
	}
}

// Nothing earned means nothing shown. Padding everyone out with a label
// would make the ones that mean something worthless.
func TestTraitIsEmptyWhenNothingIsEarned(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	// Ordinary spread, flat form, no streak worth naming, no first-guess
	// solve, nothing late-heavy.
	var results []store.BoardResult
	pattern := []int{3, 5, 4, 3, 6, 4, 2, 4, 5, 3}
	for i, p := 0, 1860; p <= 1900; i, p = i+1, p+1 {
		if p%9 == 0 {
			continue // a gap, so no long streak
		}
		results = append(results, result(1, p, pattern[i%len(pattern)], false))
	}

	if got := traitOf(t, players, results, "alma"); got != "" {
		t.Errorf("Trait() = %q, want none", got)
	}
}

// Every trait the rules can return needs copy, or a player sees a bare
// key on their own page.
func TestEveryTraitConstantIsReachable(t *testing.T) {
	all := []string{
		TraitGhost, TraitNewcomer, TraitLapsed, TraitPurist, TraitIronman,
		TraitSniper, TraitMetronome, TraitWildcard, TraitClimbing, TraitSlipping,
		TraitCloser,
	}
	seen := map[string]bool{}
	for _, n := range all {
		if seen[n] {
			t.Errorf("duplicate trait key %q", n)
		}
		seen[n] = true
	}
	if len(seen) != len(all) {
		t.Fatalf("got %d distinct keys from %d constants", len(seen), len(all))
	}
}

// Below three players there is no group middle to compare against, so the
// spread rules stand down rather than measuring somebody against himself.
func TestSpreadTraitsNeedAGroup(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse")}
	results := append(run(1, 1877, 1900, 4, false), run(2, 1877, 1900, 3, false)...)

	board := Compute(players, results, DefaultOptions(today(t)))
	traits := NewTraiter(board)
	for _, slug := range []string{"alma", "bosse"} {
		if got := traits.For(find(t, board, slug)); got == TraitMetronome || got == TraitWildcard {
			t.Errorf("%s got %q from a two-player group", slug, got)
		}
	}
}
