package stats

import (
	"slices"
	"strings"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Badges earned by comparing the grids of one day. The grid's own shapes
// are wordle.GridBadges; these need the rest of the group to say.
//
// Like traits, badges are worked out from the grids each time and never
// stored: a corrected grid changes what it earned, and a rule changed here
// applies to the whole history at once.
const (
	// BadgeTwins is a grid somebody else posted too, square for square.
	BadgeTwins = "twins"
	// BadgeMirror is a grid somebody else posted flipped left to right.
	BadgeMirror = "mirror"
	// BadgeSameOpening is the whole group's first row alike.
	BadgeSameOpening = "same-opening"
)

// pairRows is how long a grid has to be before matching another is worth a
// badge. Two-row grids are few enough that two of them alike, or mirrored,
// is a coincidence rather than an event.
const pairRows = 3

// sameOpeningPlayers is how many grids a day needs before the whole group
// opening alike says anything: two players alike is only a pair.
const sameOpeningPlayers = 3

// Badges is every badge, grid and day, in the order a list of them shows.
var Badges = append(slices.Clone(wordle.GridBadges), BadgeTwins, BadgeMirror, BadgeSameOpening)

// BadgeResult names one result: one player's grid on one puzzle.
type BadgeResult struct {
	PlayerID int64
	PuzzleNo int
}

// ComputeBadges returns what every result with a grid earned, in Badges
// order. A result that earned nothing is absent.
func ComputeBadges(results []store.BoardResult) map[BadgeResult][]string {
	days := map[int][]store.BoardResult{}
	for _, r := range results {
		days[r.PuzzleNo] = append(days[r.PuzzleNo], r)
	}
	out := map[BadgeResult][]string{}
	for no, day := range days {
		for id, badges := range DayBadges(day) {
			out[BadgeResult{PlayerID: id, PuzzleNo: no}] = badges
		}
	}
	return out
}

// DayBadges returns what each player's grid earned on one puzzle, given
// every result for that puzzle, keyed by player. A player who earned
// nothing is absent.
func DayBadges(day []store.BoardResult) map[int64][]string {
	earned := map[int64]map[string]bool{}
	give := func(id int64, badge string) {
		if earned[id] == nil {
			earned[id] = map[string]bool{}
		}
		earned[id][badge] = true
	}

	var grids []store.BoardResult
	for _, r := range day {
		if r.Grid == "" {
			continue
		}
		grids = append(grids, r)
		for _, b := range wordle.Grid(r.Grid).Badges() {
			give(r.PlayerID, b)
		}
	}

	for i, a := range grids {
		if len(wordle.Grid(a.Grid).Rows()) < pairRows {
			continue
		}
		for _, b := range grids[i+1:] {
			switch {
			case a.Grid == b.Grid:
				give(a.PlayerID, BadgeTwins)
				give(b.PlayerID, BadgeTwins)
			case a.Grid == mirrored(b.Grid):
				give(a.PlayerID, BadgeMirror)
				give(b.PlayerID, BadgeMirror)
			}
		}
	}

	// Everyone who played, not everyone with a grid: a result filed by hand
	// has no first row to compare, and the group is not alike if one of
	// them is unknown.
	if len(grids) >= sameOpeningPlayers && len(grids) == len(day) {
		first := func(g string) string { return wordle.Grid(g).Rows()[0] }
		alike := true
		for _, r := range grids[1:] {
			alike = alike && first(r.Grid) == first(grids[0].Grid)
		}
		if alike {
			for _, r := range grids {
				give(r.PlayerID, BadgeSameOpening)
			}
		}
	}

	out := make(map[int64][]string, len(earned))
	for id, set := range earned {
		for _, b := range Badges {
			if set[b] {
				out[id] = append(out[id], b)
			}
		}
	}
	return out
}

// mirrored is a grid with every row reversed.
func mirrored(g string) string {
	rows := wordle.Grid(g).Rows()
	for i, row := range rows {
		b := []byte(row)
		slices.Reverse(b)
		rows[i] = string(b)
	}
	return strings.Join(rows, "/")
}

// BadgeTally is how often one player has earned one badge.
type BadgeTally struct {
	Badge string
	Count int
	// Last is the most recent puzzle it was earned on, 0 if never.
	Last int
}

// TallyBadges counts one player's badges, one entry per badge in Badges
// order, the ones never earned included: a list of what there is to earn
// is half of what makes one worth having.
func TallyBadges(badges map[BadgeResult][]string, playerID int64) []BadgeTally {
	tallies := make([]BadgeTally, len(Badges))
	index := make(map[string]int, len(Badges))
	for i, b := range Badges {
		tallies[i].Badge = b
		index[b] = i
	}
	for key, earned := range badges {
		if key.PlayerID != playerID {
			continue
		}
		for _, b := range earned {
			t := &tallies[index[b]]
			t.Count++
			t.Last = max(t.Last, key.PuzzleNo)
		}
	}
	return tallies
}

// Openings is how many of the 243 first rows there are a player has
// opened with.
func Openings(results []store.BoardResult, playerID int64) int {
	seen := map[string]bool{}
	for _, r := range results {
		if r.PlayerID == playerID && r.Grid != "" {
			seen[wordle.Grid(r.Grid).Rows()[0]] = true
		}
	}
	return len(seen)
}

// PossibleOpenings is every first row there could be: three colours, five
// squares.
const PossibleOpenings = 243
