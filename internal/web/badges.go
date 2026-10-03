package web

import (
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// badgeView is a badge as a page names it: what it is called and why it
// was earned.
type badgeView struct {
	Name string
	Why  string
}

// badgeViews names a list of badge keys.
func badgeViews(t translator, keys []string) []badgeView {
	views := make([]badgeView, 0, len(keys))
	for _, k := range keys {
		views = append(views, badgeView{Name: t.T("badge." + k), Why: t.T("badge." + k + ".why")})
	}
	return views
}

// badgeExamples is a grid that earns each badge, drawn beside it in a
// player's list so the shape is seen rather than described. The day's
// badges, which compare two grids, show one of the pair.
var badgeExamples = map[string]wordle.Grid{
	wordle.BadgeRoyalStaircase: "gnnnn/ggnnn/gggnn/ggggn/ggggg",
	wordle.BadgeGreenStaircase: "nyynn/ggnnn/gggnn/ggggn/ggggg",
	wordle.BadgeGrandStaircase: "ynnnn/nynnn/nnynn/nnnyn/ggggg",
	wordle.BadgeStaircase:      "gynnn/gnynn/gnnyn/ggggg",
	wordle.BadgeSpaceInvader:   "nynyn/yngny/ngggn/ggggg",
	wordle.BadgeCheckerboard:   "gngng/ngngn/ggggg",
	wordle.BadgeSeaOfGreens:    "nngnn/gngng/ggggg",
	wordle.BadgeAnagram:        "nnynn/yyyyy/ggggg",
	wordle.BadgeLateBloomer:    "nynnn/ynyny/ggggg",
	wordle.BadgeFromNothing:    "nnnnn/ggggg",
	wordle.BadgeTrapEscape:     "ngggg/ngggg/ngggg/ggggg",
	stats.BadgeTwins:           "nynnn/ngygn/ggggg",
	stats.BadgeMirror:          "nnnyn/ngygn/ggggg",
	stats.BadgeSameOpening:     "ynnnn/ngnyn/ggggg",
}

// badgeRow is one badge in a player's list: earned or still to earn.
type badgeRow struct {
	badgeView
	Example string
	Earned  bool
	Count   string
	// Last is the puzzle it was last earned on and where that day is.
	Last     string
	LastHref string
}

// playerBadges is the player's list of badges, every one there is, the
// ones earned with how often and when last.
func playerBadges(t translator, results []store.BoardResult, playerID int64, prefix string) []badgeRow {
	tallies := stats.TallyBadges(stats.ComputeBadges(results), playerID)
	rows := make([]badgeRow, 0, len(tallies))
	for _, tally := range tallies {
		row := badgeRow{
			badgeView: badgeViews(t, []string{tally.Badge})[0],
			Example:   string(badgeExamples[tally.Badge]),
			Earned:    tally.Count > 0,
		}
		if row.Earned {
			row.Count = "×" + t.Integer(tally.Count)
			row.Last = t.T("player.badges.last", t.Puzzle(tally.Last))
			row.LastHref = puzzlePath(prefix, tally.Last)
		}
		rows = append(rows, row)
	}
	return rows
}
