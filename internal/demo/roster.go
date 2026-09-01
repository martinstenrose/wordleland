// Package demo generates synthetic play data for a staging deployment that
// has no Signal bridge to fill its board from. Everything it produces —
// names, play patterns, guess counts — is invented, deterministic given a
// seed, and never derived from or resembling any real person or group.
package demo

import (
	"fmt"
	"math/rand"
)

// firstNames and lastNames are an ordinary-looking, entirely invented pool.
// Names are combined rather than fixed to a roster so that no single name
// here is "the" synthetic player — see CLAUDE.md on never committing a
// literal roster.
var firstNames = []string{
	"Erik", "Anna", "Lars", "Karin", "Johan", "Maria", "Nils", "Emma",
	"Oskar", "Ida", "Gustav", "Elin", "Anton", "Sara", "Viktor", "Klara",
	"Fredrik", "Linnea", "David", "Sofia", "Simon", "Alice", "Marcus", "Julia",
}

// lastNames pairs with firstNames. Both pools are common Swedish surnames,
// none tied to the group this software was built for.
var lastNames = []string{
	"Andersson", "Johansson", "Karlsson", "Nilsson", "Eriksson", "Larsson",
	"Olsson", "Persson", "Svensson", "Gustafsson", "Pettersson", "Jonsson",
	"Lindqvist", "Bergstrom", "Lundgren", "Sandberg", "Holm", "Berg",
	"Dahl", "Ekstrom", "Wallin", "Astrom", "Nystrom", "Sjoberg",
}

// Role marks the handful of personas the seed deliberately shapes so the
// board's own callouts and admin screens have something to show, rather
// than leaving it to chance whether any of them fire.
type Role int

const (
	// RoleOrdinary plays with an unremarkable, randomised miss rate.
	RoleOrdinary Role = iota
	// RoleUnbroken never misses a day, guaranteeing a streak the "Unbroken"
	// callout can report.
	RoleUnbroken
	// RoleMissing plays normally at first and then stops well before the
	// end of the backfill window, guaranteeing the "Missing" callout.
	RoleMissing
	// RoleRetired plays normally during the backfill; the seed verb retires
	// the player afterwards, for the "no longer in the group" rendering.
	RoleRetired
)

// Persona is one synthetic player's stable traits: what they are like, not
// what they did on any particular day. tick derives the same Persona for a
// given name every run, from PersonaFor, so a player's behaviour stays
// consistent across invocations without any of this being stored.
type Persona struct {
	Name string
	Role Role

	// HardModeRate is the per-game probability of playing in hard mode. It
	// is either zero or high: docs/decisions.md found hard mode splits by
	// player, not spread evenly across one player's games.
	HardModeRate float64

	// MissRate is the per-day probability of not playing at all, for the
	// ordinary days in a persona's history. RoleUnbroken ignores it.
	MissRate float64
}

// NewRoster returns n personas, deterministic for a given seed.
//
// The first three carry the special roles: index 0 is RoleUnbroken, index 1
// is RoleMissing, index 2 is RoleRetired (if n allows; a roster smaller than
// that simply has fewer of them, ordinary personas first). Assigning by
// position rather than choosing randomly keeps `seed` from occasionally
// producing a roster where nobody plays every day, which would silently
// break the "Unbroken" callout's precondition.
func NewRoster(seed int64, n int) ([]Persona, error) {
	if n <= 0 {
		return nil, fmt.Errorf("roster size must be positive, got %d", n)
	}
	max := len(firstNames) * len(lastNames)
	if n > max {
		return nil, fmt.Errorf("roster size %d exceeds the %d available name combinations", n, max)
	}

	rng := rand.New(rand.NewSource(seed))

	// A Fisher-Yates shuffle over every (first, last) pairing, so names are
	// distinct and their order is fully determined by seed.
	combos := make([]int, max)
	for i := range combos {
		combos[i] = i
	}
	rng.Shuffle(len(combos), func(i, j int) { combos[i], combos[j] = combos[j], combos[i] })

	roles := []Role{RoleUnbroken, RoleMissing, RoleRetired}

	roster := make([]Persona, n)
	for i := 0; i < n; i++ {
		idx := combos[i]
		name := firstNames[idx/len(lastNames)] + " " + lastNames[idx%len(lastNames)]

		role := RoleOrdinary
		if i < len(roles) {
			role = roles[i]
		}

		// Rates come from PersonaFor, keyed on the name rather than rolled
		// here, so a player's hard-mode and miss rates are the same
		// whether they are being freshly backfilled or, days later,
		// reconstructed by tick from nothing but their name.
		p := PersonaFor(name)
		p.Role = role
		roster[i] = p
	}
	return roster, nil
}
