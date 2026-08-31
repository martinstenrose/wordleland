package stats

import "sort"

// Traits earned from the figures, not stored against anybody.
//
// The design carried a hardcoded list; these are derived instead, so a
// trait is something the play produced and changes when the play does.
// Each is a key the view localises — nothing here is a sentence.
//
// One is displayed per player. Fixed state labels take precedence; active
// players build a pool of earned descriptions that rotates with the puzzle.
// A player who has earned nothing gets nothing: padding everyone out with a
// label would make the ones that mean something worthless.
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
	TraitStreaker  = "streaker"  // an active streak worth noting, below ironman
	TraitVeteran   = "veteran"   // a substantial history
	TraitFlawless  = "flawless"  // no failures across a substantial history
	TraitSpeedster = "speedster" // unusually many two-guess solves
	TraitThreepeat = "threepeat" // threes are their signature score
	TraitFourish   = "fourish"   // fours are their signature score
	TraitEscape    = "escape"    // unusually many six-guess escapes
	TraitSwitcher  = "switcher"  // regularly uses both normal and hard mode
	TraitHotHand   = "hot-hand"  // the latest five played games are excellent
	TraitCleanRun  = "clean-run" // ten straight recent solves
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
	traitCloserShare     = 0.35
	traitVeteranGames    = 100
	traitFlawlessGames   = 30
	traitSpecialistShare = 0.4
	traitRareScoreShare  = 0.2
	traitStreakerStreak  = 7
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
	medianSpread  float64
	hasSpread     bool
	currentPuzzle int
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
		return Traiter{currentPuzzle: board.CurrentPuzzle}
	}
	sort.Float64s(spreads)
	return Traiter{medianSpread: spreads[len(spreads)/2], hasSpread: true, currentPuzzle: board.CurrentPuzzle}
}

// For returns the key suffix a player has earned, or "" for none.
func (n Traiter) For(p Player) string { return trait(p, n) }

// Trait returns the key suffix a player has earned, or "" for none.
//
// State-of-play labels come first because a trait about form would be
// misleading for somebody who has stopped playing.
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

	var earned []string
	if p.Games > 0 && float64(p.HardModeGames)/float64(p.Games) >= traitPuristShare {
		earned = append(earned, TraitPurist)
	} else if p.Games >= MinGames {
		share := float64(p.HardModeGames) / float64(p.Games)
		if share >= 0.25 && share <= 0.75 {
			earned = append(earned, TraitSwitcher)
		}
	}

	// Live signals join standing achievements in the earned pool. Neither
	// kind permanently masks the other.
	if p.Delta != nil {
		if *p.Delta <= -Significance {
			earned = append(earned, TraitClimbing)
		}
		if *p.Delta >= Significance {
			earned = append(earned, TraitSlipping)
		}
	}

	if p.CurrentStreak >= traitIronmanStreak {
		earned = append(earned, TraitIronman)
	} else if p.CurrentStreak >= traitStreakerStreak {
		earned = append(earned, TraitStreaker)
	}
	// A first-guess solve, but only a recent one. Counted over the whole
	// history it stops being rare — on a year of play most of the roster
	// has one, and a label most people carry says nothing about any of
	// them. The window is the same one the callout uses.
	if solvedInOne(p.Series) {
		earned = append(earned, TraitSniper)
	}

	if p.Spread != nil && n.hasSpread {
		if *p.Spread <= n.medianSpread-traitSpreadGap {
			earned = append(earned, TraitMetronome)
		}
		if *p.Spread >= n.medianSpread+traitSpreadGap {
			earned = append(earned, TraitWildcard)
		}
	}

	// Indices 4, 5 and 6 are five guesses, six guesses and a failure.
	if p.Games > 0 {
		late := p.Distribution[4] + p.Distribution[5] + p.Distribution[6]
		if float64(late)/float64(p.Games) >= traitCloserShare {
			earned = append(earned, TraitCloser)
		}
		if p.Games >= traitVeteranGames {
			earned = append(earned, TraitVeteran)
		}
		if p.Games >= traitFlawlessGames && p.Distribution[6] == 0 {
			earned = append(earned, TraitFlawless)
		}
		if float64(p.Distribution[1])/float64(p.Games) >= traitRareScoreShare {
			earned = append(earned, TraitSpeedster)
		}
		if float64(p.Distribution[2])/float64(p.Games) >= traitSpecialistShare {
			earned = append(earned, TraitThreepeat)
		}
		if float64(p.Distribution[3])/float64(p.Games) >= traitSpecialistShare {
			earned = append(earned, TraitFourish)
		}
		if float64(p.Distribution[5])/float64(p.Games) >= traitRareScoreShare {
			earned = append(earned, TraitEscape)
		}
	}
	if recentPlayedAverage(p.Series, 5) <= 3.2 {
		earned = append(earned, TraitHotHand)
	}
	if recentSolvedRun(p.Series) >= 10 {
		earned = append(earned, TraitCleanRun)
	}
	if len(earned) == 0 {
		return ""
	}

	// A player can earn several true descriptions. Rotate among them with
	// the puzzle so one durable achievement cannot hide every live one.
	// The choice is deterministic: every rendering of a given day agrees.
	index := (n.currentPuzzle + int(p.ID)) % len(earned)
	if index < 0 {
		index += len(earned)
	}
	return earned[index]
}

func recentPlayedAverage(series []float64, count int) float64 {
	var total float64
	seen := 0
	for i := len(series) - 1; i >= 0 && seen < count; i-- {
		if series[i] <= 0 {
			continue
		}
		total += series[i]
		seen++
	}
	if seen < count {
		return 99
	}
	return total / float64(seen)
}

func recentSolvedRun(series []float64) int {
	run := 0
	for i := len(series) - 1; i >= 0; i-- {
		if series[i] <= 0 || series[i] >= failedAsSeven {
			break
		}
		run++
	}
	return run
}
