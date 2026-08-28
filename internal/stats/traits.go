package stats

import "sort"

// Traits earned from the figures, not stored against anybody.
//
// The design carried a hardcoded list; these are derived instead, so a
// trait is something the play produced and changes when the play does.
// Each is a key the view localises — nothing here is a sentence.
//
// One per player, first match wins. A player who has earned nothing gets
// nothing: padding everyone out with a label would make the ones that mean
// something worthless.
const (
	TraitGhost     = "ghost"     // on the roster, never filed
	TraitNewcomer  = "newcomer"  // too few games to say anything yet
	TraitLapsed    = "lapsed"    // a real history, but not lately
	TraitPurist    = "purist"    // hard mode, near enough always
	TraitIronman   = "ironman"   // has not missed a day in a long run
	TraitSniper    = "sniper"    // has solved one on the first guess
	TraitMetronome = "metronome" // the same score, over and over
	TraitWildcard  = "wildcard"  // anything from a 2 to a failure
	TraitClimbing  = "climbing"  // playing well above their own average
	TraitSlipping  = "slipping"  // and the other way
	TraitCloser    = "closer"    // gets there, but on the last guess or two
)

// Trait thresholds. Each is deliberately hard enough to clear that the
// label means something when it appears.
const (
	// traitPuristShare is how much hard mode makes someone a purist.
	traitPuristShare = 0.9
	// traitIronmanStreak is a streak worth a name of its own.
	traitIronmanStreak = 30
	// traitSpreadGap is how far from the group's own middle a player's
	// spread has to sit before it is worth a name.
	//
	// Relative rather than absolute: what counts as steady depends on the
	// group. Fixed thresholds guessed in advance either catch everybody or
	// nobody — on this roster every spread falls between 0.90 and 1.10, so
	// a cutoff at 0.7 would have made two of these labels unreachable. The
	// gap keeps a genuinely uniform group from being handed one anyway.
	traitSpreadGap = 0.15
	// traitSpreadMin is how many players must have a spread before comparing
	// against the group means anything. With two, one of them *is* the
	// middle, so the comparison is against themselves.
	traitSpreadMin = 3
	// traitCloserShare is how much of someone's play lands on 5, 6 or a
	// failure before "gets there in the end" is a fair description.
	traitCloserShare = 0.35
)

// solvedInOne reports a one-guess solve inside the form window. A zero in
// the series is a day not played rather than a score.
func solvedInOne(series []float64) bool {
	for _, v := range series {
		if v == 1 {
			return true
		}
	}
	return false
}

// Traiter assigns traits with the group in view, which some of the
// rules need: steadiness is a comparison, not an absolute.
type Traiter struct {
	medianSpread float64
	hasSpread    bool
}

// NewTraiter reads the group's middle from the board.
func NewTraiter(board Board) Traiter {
	var spreads []float64
	for _, group := range [][]Player{board.Ranked, board.Unranked} {
		for _, p := range group {
			if p.Spread != nil {
				spreads = append(spreads, *p.Spread)
			}
		}
	}
	if len(spreads) < traitSpreadMin {
		return Traiter{}
	}
	sort.Float64s(spreads)
	return Traiter{medianSpread: spreads[len(spreads)/2], hasSpread: true}
}

// For returns the key suffix a player has earned, or "" for none.
func (n Traiter) For(p Player) string { return trait(p, n) }

// Trait returns the key suffix a player has earned, or "" for none.
//
// Order is deliberate: what someone is doing now beats what they once did,
// and the state-of-play labels come first because a trait about form
// would be misleading for somebody who has stopped playing.
func Trait(p Player) string { return trait(p, Traiter{}) }

func trait(p Player, n Traiter) string {
	switch p.Reason {
	case ReasonInactive:
		return TraitGhost
	case ReasonNoRecentGames:
		return TraitLapsed
	case ReasonLowData:
		return TraitNewcomer
	}

	// Purist first among the earned ones: playing hard mode is a way of
	// playing rather than a result, so it does not come and go.
	if p.Games > 0 && float64(p.HardModeGames)/float64(p.Games) >= traitPuristShare {
		return TraitPurist
	}

	// Then the live signals, ahead of the standing ones. A long streak
	// would otherwise mask form for every regular — and regulars are
	// exactly who has long streaks — leaving two of these labels unreachable
	// in practice.
	if p.Delta != nil {
		if *p.Delta <= -Significance {
			return TraitClimbing
		}
		if *p.Delta >= Significance {
			return TraitSlipping
		}
	}

	if p.CurrentStreak >= traitIronmanStreak {
		return TraitIronman
	}
	// A first-guess solve, but only a recent one. Counted over the whole
	// history it stops being rare — on a year of play most of the roster
	// has one, and a label most people carry says nothing about any of
	// them. The window is the same one the callout uses.
	if solvedInOne(p.Series) {
		return TraitSniper
	}

	if p.Spread != nil && n.hasSpread {
		if *p.Spread <= n.medianSpread-traitSpreadGap {
			return TraitMetronome
		}
		if *p.Spread >= n.medianSpread+traitSpreadGap {
			return TraitWildcard
		}
	}

	// Indices 4, 5 and 6 are five guesses, six guesses and a failure.
	if p.Games > 0 {
		late := p.Distribution[4] + p.Distribution[5] + p.Distribution[6]
		if float64(late)/float64(p.Games) >= traitCloserShare {
			return TraitCloser
		}
	}
	return ""
}
