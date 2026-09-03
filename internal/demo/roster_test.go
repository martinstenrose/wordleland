package demo

import (
	"math/rand"
	"testing"

	"github.com/martinstenrose/wordleland/internal/stats"
)

func TestNewRosterIsReproducibleForSameSeed(t *testing.T) {
	a, err := NewRoster(42, 12)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	b, err := NewRoster(42, 12)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("persona %d differs between runs with the same seed: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestNewRosterDiffersForDifferentSeeds(t *testing.T) {
	a, err := NewRoster(1, 12)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	b, err := NewRoster(2, 12)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	same := true
	for i := range a {
		if a[i].Name != b[i].Name {
			same = false
		}
	}
	if same {
		t.Error("two different seeds produced the exact same roster order; want the seed to matter")
	}
}

func TestNewRosterNamesAreDistinct(t *testing.T) {
	roster, err := NewRoster(7, 12)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	seen := make(map[string]bool)
	for _, p := range roster {
		if seen[p.Name] {
			t.Errorf("duplicate name %q in roster", p.Name)
		}
		seen[p.Name] = true
	}
}

func TestNewRosterRejectsNonPositiveSize(t *testing.T) {
	if _, err := NewRoster(1, 0); err == nil {
		t.Error("NewRoster(0) succeeded, want an error")
	}
	if _, err := NewRoster(1, -1); err == nil {
		t.Error("NewRoster(-1) succeeded, want an error")
	}
}

func TestNewRosterRejectsSizeBeyondNamePool(t *testing.T) {
	huge := len(firstNames)*len(lastNames) + 1
	if _, err := NewRoster(1, huge); err == nil {
		t.Errorf("NewRoster(%d) succeeded, want an error since only %d combinations exist",
			huge, len(firstNames)*len(lastNames))
	}
}

// TestNewRosterAssignsSpecialRoles pins the ordering the backfill depends
// on: it does not search the roster for someone to play every day or
// someone to stop early, it trusts the first three positions.
func TestNewRosterAssignsSpecialRoles(t *testing.T) {
	roster, err := NewRoster(1, 12)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	want := []Role{RoleUnbroken, RoleMissing, RoleRetired}
	for i, role := range want {
		if roster[i].Role != role {
			t.Errorf("roster[%d].Role = %v, want %v", i, roster[i].Role, role)
		}
	}
	for i := len(want); i < len(roster); i++ {
		if roster[i].Role != RoleOrdinary {
			t.Errorf("roster[%d].Role = %v, want RoleOrdinary", i, roster[i].Role)
		}
	}
}

// TestNewRosterSplitsHardModeByPlayer is the realism rule from
// docs/decisions.md: hard mode is roughly half of all results, but that is
// a third of the roster playing it almost exclusively, not everyone playing
// it half the time. A HardModeRate of exactly zero or above 0.8 for every
// persona is what tells the two apart from a rate of ~0.33 across the
// board, which this test would also let through if it only checked the
// average.
func TestNewRosterSplitsHardModeByPlayer(t *testing.T) {
	roster, err := NewRoster(1, 24)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	var hardModers, never int
	for _, p := range roster {
		switch {
		case p.HardModeRate == 0:
			never++
		case p.HardModeRate >= 0.87 && p.HardModeRate <= 0.96:
			hardModers++
		default:
			t.Errorf("persona %q has HardModeRate %v, want 0 or in [0.87, 0.96]", p.Name, p.HardModeRate)
		}
	}
	if hardModers == 0 {
		t.Error("no persona plays hard mode; want roughly a third of the roster to")
	}
	if never == 0 {
		t.Error("every persona plays some hard mode; want roughly two-thirds to never")
	}
}

func TestPersonaForIsDeterministic(t *testing.T) {
	a := PersonaFor("Erik Andersson")
	b := PersonaFor("Erik Andersson")
	if a != b {
		t.Errorf("PersonaFor() returned different traits for the same name: %+v vs %+v", a, b)
	}
}

// TestNewRosterMatchesPersonaFor is what makes tick's reconstruction valid:
// a player's rates during backfill must be exactly what PersonaFor derives
// from their name alone, since that is all tick has to go on days later.
func TestNewRosterMatchesPersonaFor(t *testing.T) {
	roster, err := NewRoster(9, 12)
	if err != nil {
		t.Fatalf("NewRoster() failed: %v", err)
	}
	for _, p := range roster {
		want := PersonaFor(p.Name)
		if p.HardModeRate != want.HardModeRate || p.MissRate != want.MissRate {
			t.Errorf("roster persona %q = %+v, want rates matching PersonaFor() = %+v", p.Name, p, want)
		}
	}
}

func TestPersonaPlayedRoles(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const totalDays = 30

	unbroken := Persona{Role: RoleUnbroken}
	for day := 0; day < totalDays; day++ {
		if !unbroken.Played(rng, day, totalDays) {
			t.Fatalf("RoleUnbroken missed day %d, want it to always play", day)
		}
	}

	missing := Persona{Role: RoleMissing}
	lastPlayed := -1
	for day := 0; day < totalDays; day++ {
		if missing.Played(rng, day, totalDays) {
			lastPlayed = day
		}
	}
	if lastPlayed == -1 {
		t.Fatal("RoleMissing never played; want an early history before it stops")
	}
	if totalDays-1-lastPlayed < 7 {
		t.Errorf("RoleMissing's last played day was %d of %d, want at least 7 days of trailing absence "+
			"for the Missing callout's AbsentDays threshold to clear", lastPlayed, totalDays)
	}
	// The pattern must be monotonic — play, then stop, never interleaved —
	// or a played day inside what should be the trailing absence would
	// reset DaysSince and the Missing callout would never fire.
	for day := lastPlayed + 1; day < totalDays; day++ {
		if missing.Played(rng, day, totalDays) {
			t.Errorf("RoleMissing played again on day %d after stopping on day %d", day, lastPlayed)
		}
	}
}

// TestPersonaMissingClearsAbsentDaysForShortWindows is the case
// TestPersonaPlayedRoles's hardcoded totalDays=30 doesn't reach: a short
// --days run must still leave at least stats.AbsentDays of trailing
// absence, or the "Missing" callout it exists to demonstrate never fires.
func TestPersonaMissingClearsAbsentDaysForShortWindows(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	missing := Persona{Role: RoleMissing}

	for _, totalDays := range []int{stats.AbsentDays + 1, 10, 18, 30, 200} {
		lastPlayed := -1
		for day := 0; day < totalDays; day++ {
			if missing.Played(rng, day, totalDays) {
				lastPlayed = day
			}
		}
		if trailing := totalDays - 1 - lastPlayed; trailing < stats.AbsentDays {
			t.Errorf("totalDays=%d: trailing absence = %d, want at least stats.AbsentDays (%d)",
				totalDays, trailing, stats.AbsentDays)
		}
	}
}

func TestPersonaPlayCentersOnFourGuesses(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	p := Persona{HardModeRate: 0}

	counts := make(map[int]int)
	const trials = 20000
	for i := 0; i < trials; i++ {
		out := p.Play(rng)
		if !out.Solved {
			counts[0]++
			continue
		}
		counts[out.Guesses]++
	}

	if counts[4] == 0 {
		t.Fatal("never sampled a 4-guess solve")
	}
	for guesses, n := range counts {
		if guesses != 4 && n > counts[4] {
			t.Errorf("bucket %v occurred %d times, more than the %d for 4 guesses; want 4 to be the mode", guesses, n, counts[4])
		}
	}
	if counts[0] == 0 {
		t.Error("never sampled a failure; want occasional failures")
	}
	if counts[0] > trials/10 {
		t.Errorf("failed %d/%d times, want failures to stay occasional (<10%%)", counts[0], trials)
	}
}

func TestPersonaPlayNeverStoresSixForAFailure(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	p := Persona{}
	for i := 0; i < 5000; i++ {
		out := p.Play(rng)
		if !out.Solved && out.Guesses != 0 {
			t.Fatalf("failed outcome carries Guesses = %d, want 0 (storage represents a failure as NULL, never 6)", out.Guesses)
		}
	}
}

// TestDailyRNGIsDeterministic pins the property tick depends on: the same
// player, puzzle and salt must draw the same sequence every time, since a
// rerun for a puzzle that left no row to check against has nothing else to
// reproduce the earlier decision from.
func TestDailyRNGIsDeterministic(t *testing.T) {
	a := DailyRNG("Erik Andersson", 1900, 0)
	b := DailyRNG("Erik Andersson", 1900, 0)
	for i := 0; i < 10; i++ {
		x, y := a.Float64(), b.Float64()
		if x != y {
			t.Fatalf("draw %d differs between two DailyRNG() calls with identical inputs: %v vs %v", i, x, y)
		}
	}
}

// TestDailyRNGVariesWithItsInputs is what makes DailyRNG useful rather than
// a constant: a different puzzle, player, or salt must not draw the same
// sequence, or every day (or every player) would play out identically.
func TestDailyRNGVariesWithItsInputs(t *testing.T) {
	base := DailyRNG("Erik Andersson", 1900, 0).Float64()

	if got := DailyRNG("Erik Andersson", 1901, 0).Float64(); got == base {
		t.Error("a different puzzle number drew the same first value")
	}
	if got := DailyRNG("Anna Karlsson", 1900, 0).Float64(); got == base {
		t.Error("a different name drew the same first value")
	}
	if got := DailyRNG("Erik Andersson", 1900, 1).Float64(); got == base {
		t.Error("a different salt drew the same first value")
	}
}
