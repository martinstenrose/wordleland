package wordle

import "strings"

// Badges a single grid can earn by its shape. Each is a key the view
// localises; nothing here is a sentence. The badges that compare one grid
// with another — the group on one day — are worked out in internal/stats.
//
// Only a solved grid earns anything: every rule below reads the rows before
// the solving one, and a miss has none.
const (
	// BadgeStaircase is one square, yellow or green, moving a column
	// sideways each row, the same way, over at least three rows, with
	// everything else on those rows staying put.
	BadgeStaircase = "staircase"
	// BadgeGrandStaircase is the same over at least four rows.
	BadgeGrandStaircase = "grand-staircase"
	// BadgeGreenStaircase is a block of greens growing from one edge by a
	// square a row, the rest grey, up to the solve: at least three rows,
	// the solving one included.
	BadgeGreenStaircase = "green-staircase"
	// BadgeRoyalStaircase is the whole green staircase, one green to five,
	// solved in exactly five.
	BadgeRoyalStaircase = "royal-staircase"
	// BadgeSpaceInvader is at least three rows before the solve, every one
	// of them symmetric, none of them blank, and not all the same.
	BadgeSpaceInvader = "space-invader"
	// BadgeCheckerboard is two consecutive rows that alternate green and
	// grey, each the other's negative.
	BadgeCheckerboard = "checkerboard"
	// BadgeSeaOfGreens is a solve without a single yellow, the rule and
	// the name of the badge the game itself awards.
	BadgeSeaOfGreens = "sea-of-greens"
	// BadgeAnagram is a row of five yellows: every letter, none in place.
	BadgeAnagram = "anagram"
	// BadgeLateBloomer is no green at all until the solving row, over at
	// least three guesses — with two it is too ordinary to remark on.
	BadgeLateBloomer = "late-bloomer"
	// BadgeFromNothing is a first row of five greys, then the solve.
	BadgeFromNothing = "from-nothing"
	// BadgeTrapEscape is at least three rows with four greens before the
	// solve: the trap where one letter has too many candidates.
	BadgeTrapEscape = "trap-escape"
)

// GridBadges is every badge a grid can earn, in the order they are listed.
var GridBadges = []string{
	BadgeRoyalStaircase, BadgeGreenStaircase,
	BadgeGrandStaircase, BadgeStaircase,
	BadgeSpaceInvader, BadgeCheckerboard,
	BadgeSeaOfGreens, BadgeAnagram, BadgeLateBloomer, BadgeFromNothing,
	BadgeTrapEscape,
}

// Badges returns what a grid earns, in GridBadges order.
//
// A grid that earns the grander of two badges in one family does not earn
// the lesser as well — a royal flush is not also called a straight flush.
// Badges from different families stack.
func (g Grid) Badges() []string {
	rows := g.Rows()
	if len(rows) == 0 || rows[len(rows)-1] != "ggggg" {
		return nil
	}
	before := rows[:len(rows)-1]

	earned := map[string]bool{}
	switch steps := greenSteps(rows); {
	case steps == 5 && len(rows) == 5:
		earned[BadgeRoyalStaircase] = true
	case steps >= 3:
		earned[BadgeGreenStaircase] = true
	}
	switch steps := longestStep(before); {
	case steps >= 4:
		earned[BadgeGrandStaircase] = true
	case steps >= 3:
		earned[BadgeStaircase] = true
	}
	earned[BadgeSpaceInvader] = spaceInvader(before)
	earned[BadgeCheckerboard] = checkerboard(before)
	earned[BadgeSeaOfGreens] = !strings.Contains(string(g), "y")
	earned[BadgeLateBloomer] = len(rows) >= 3 && !strings.Contains(strings.Join(before, ""), "g")
	earned[BadgeFromNothing] = len(rows) == 2 && rows[0] == "nnnnn"
	fours := 0
	for _, row := range before {
		if row == "yyyyy" {
			earned[BadgeAnagram] = true
		}
		if strings.Count(row, "g") == 4 {
			fours++
		}
	}
	earned[BadgeTrapEscape] = fours >= 3

	var out []string
	for _, b := range GridBadges {
		if earned[b] {
			out = append(out, b)
		}
	}
	return out
}

// longestStep is the most rows over which one square walks sideways a
// column a row, in one direction and one colour, with every other square
// on those rows unchanged and the squares it leaves and enters grey.
func longestStep(rows []string) int {
	best := 0
	for i, row := range rows {
		for c := 0; c < 5; c++ {
			colour := row[c]
			if colour == 'n' {
				continue
			}
			for _, d := range []int{-1, 1} {
				n, at := 1, c
				for j := i + 1; j < len(rows) && steps(rows[j-1], rows[j], at, d, colour); j++ {
					n++
					at += d
				}
				best = max(best, n)
			}
		}
	}
	return best
}

// steps reports whether the square of colour at column at in a moves to
// at+d in b, and nothing else changes.
func steps(a, b string, at, d int, colour byte) bool {
	to := at + d
	if to < 0 || to > 4 || a[at] != colour || b[to] != colour || a[to] != 'n' || b[at] != 'n' {
		return false
	}
	for c := 0; c < 5; c++ {
		if c != at && c != to && a[c] != b[c] {
			return false
		}
	}
	return true
}

// greenSteps is how many rows, ending with the solve, are a block of greens
// growing by one from the same edge, the rest grey. Fewer than two is no
// staircase and reports 0.
func greenSteps(rows []string) int {
	best := 0
	for _, fromLeft := range []bool{true, false} {
		n := 0
		for k := 5; k >= 1 && len(rows)-1-n >= 0; k-- {
			if rows[len(rows)-1-n] != greenBlock(k, fromLeft) {
				break
			}
			n++
		}
		best = max(best, n)
	}
	if best < 2 {
		return 0
	}
	return best
}

// greenBlock is k greens against one edge and greys for the rest.
func greenBlock(k int, fromLeft bool) string {
	if fromLeft {
		return strings.Repeat("g", k) + strings.Repeat("n", 5-k)
	}
	return strings.Repeat("n", 5-k) + strings.Repeat("g", k)
}

// spaceInvader reports at least three rows, every one symmetric, none
// blank and not all alike.
func spaceInvader(rows []string) bool {
	if len(rows) < 3 {
		return false
	}
	alike := true
	for _, row := range rows {
		if row == "nnnnn" || row[0] != row[4] || row[1] != row[3] {
			return false
		}
		alike = alike && row == rows[0]
	}
	return !alike
}

// checkerboard reports two consecutive rows alternating green and grey,
// each the other's negative.
func checkerboard(rows []string) bool {
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		if (a == "gngng" && b == "ngngn") || (a == "ngngn" && b == "gngng") {
			return true
		}
	}
	return false
}
