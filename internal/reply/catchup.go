package reply

import (
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// Thresholds on the average a chaser needs over the days left. Below one a
// pass is arithmetically impossible — a 1 is the best a day can be. Below
// two it takes near-perfect rounds, which is worth saying differently from
// "an average of 2.8 does it".
const (
	impossibleBelow = 1.0
	miracleBelow    = 2.0
)

// catchup answers "can X still win the month": the gap to the leader, the
// days the chaser can still play, and the average over those days that
// would take them past — on the one assumption that the leader keeps
// their current pace, which is stated in the sentence.
//
// The arithmetic is the month's own. Everyone's final average is over the
// same denominator, the month's days, with a day not played scoring 7; so
// the chaser's need is (leader's average × month length − chaser's points
// so far) ÷ days left, and today is one of those days only if the chaser
// has not played it yet.
func catchup(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	months := stats.ComputeMonths(players, results, stats.DefaultOptions(now))
	label := t.T("month." + strconv.Itoa(int(now.Month())))
	var m stats.Month
	for _, cand := range months {
		if cand.Year == now.Year() && cand.Month == now.Month() {
			m = cand
		}
	}
	if len(m.Winners) == 0 {
		return t.T("reply.leader.none", capitalized(label))
	}

	race := newRace(m, results, now)
	leaders := joinNames(t, names(m.Winners))
	leaderAvg := t.Decimal(race.leader, 2)

	// A player named — the asker's own name when they ask about themselves,
	// which is how the model reports "can I still win". Nobody named is
	// the whole field, whoever is asking: "can anyone still catch up?"
	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		mp, found := rankedPlayer(m, p.ID)
		if !found {
			return t.T("reply.catchup.notPlayed", p.Name, label)
		}
		if isWinner(m, p.ID) {
			return leadersView(t, label, leaders, m, race)
		}
		need, left := race.need(mp)
		points := race.pointsBehind(mp)
		head := capitalized(label)
		switch {
		case left == 0:
			return t.T("reply.catchup.over", head, p.Name, points, leaders)
		case need < impossibleBelow:
			return t.T("reply.catchup.impossible", head, p.Name, points, leaders, left)
		case need < miracleBelow:
			return t.T("reply.catchup.hard", head, p.Name, points, leaders, left, t.Decimal(need, 2))
		default:
			// The assumption with its number: "keeps pace" means the
			// leader's average stays where it is, and saying what it is
			// tells the chaser what they are being measured against.
			return t.T("reply.catchup.possible", head, p.Name, points, leaders, left, t.Decimal(need, 2),
				leaders, leaderAvg)
		}
	}

	// Nobody in particular: the race in a line, not everyone's numbers.
	// With much of the month left anybody can win it, and saying so is
	// the honest answer; nearer the end, who can still pass. Either way a
	// tip, said as one.
	left := race.afterNow + 1
	head := capitalized(label)
	var chasers []string
	for _, mp := range m.Ranked {
		if isWinner(m, mp.ID) {
			continue
		}
		if need, days := race.need(mp); days > 0 && need >= impossibleBelow {
			chasers = append(chasers, mp.Name)
		}
	}
	var line string
	switch {
	case left > openRaceDays:
		line = t.T("reply.catchup.open", head, leaders, leaderAvg, left)
	case len(chasers) == 0:
		line = t.T("reply.catchup.decided", head, leaders, leaderAvg)
	default:
		named := chasers
		if len(named) > maxChasersNamed {
			named = append(named[:maxChasersNamed:maxChasersNamed], t.T("reply.catchup.more", len(chasers)-maxChasersNamed))
		}
		if left == 1 {
			line = t.T("reply.catchup.close.last", head, leaders, leaderAvg, joinNames(t, named))
		} else {
			line = t.T("reply.catchup.close", head, leaders, leaderAvg, left, joinNames(t, named))
		}
	}
	if len(chasers) > 0 {
		if tip, ok := forecast(m, race, results, now); ok {
			key := "reply.catchup.tip"
			if isWinner(m, tip.player.ID) {
				key = "reply.catchup.tip.leader"
			}
			line += " " + t.T(key, tip.player.Name, t.Decimal(tip.lastFive, 2), t.Decimal(tip.thirty, 2))
		}
	}
	return line
}

// openRaceDays is how many days left make the month anybody's: a third of
// it and more, when a week's bad luck still undoes any lead. maxChasersNamed
// is how many of those still able to pass are named before "and N more".
const (
	openRaceDays    = 10
	maxChasersNamed = 3
)

// tip is the player the forecast picks, with the two averages it read.
type tip struct {
	player           stats.MonthPlayer
	lastFive, thirty float64
}

// forecast picks who is likeliest to win the month: each player's points
// so far plus the days left at what they are scoring lately — halfway
// between their last five results and their last 30 days, weighed by how
// many of those 30 days they played, since a day not played is a 7 in
// the month. A guess and said as one; only a player who can still win,
// and has played lately, is picked.
func forecast(m stats.Month, race race, results []store.BoardResult, now time.Time) (tip, bool) {
	opts := stats.DefaultOptions(now)
	current := wordle.PuzzleForDate(now)
	byPlayer := map[int64][]store.BoardResult{}
	// The window is 30 days, or the group's whole history when that is
	// shorter: a day before anybody played is nobody's miss.
	earliest := current
	for _, r := range results {
		earliest = min(earliest, r.PuzzleNo)
		if r.PuzzleNo > current-30 && r.PuzzleNo <= current {
			byPlayer[r.PlayerID] = append(byPlayer[r.PlayerID], r)
		}
	}
	var best tip
	bestFinal := math.Inf(1)
	for _, mp := range m.Ranked {
		need, left := race.need(mp)
		recent := byPlayer[mp.ID]
		if mp.Average == nil || len(recent) == 0 || !isWinner(m, mp.ID) && (left == 0 || need < impossibleBelow) {
			continue
		}
		slices.SortFunc(recent, func(a, b store.BoardResult) int { return b.PuzzleNo - a.PuzzleNo })
		lastFive, _ := stats.MeanScore(recent[:min(5, len(recent))], opts)
		thirty, played := stats.MeanScore(recent, opts)
		if played == 0 {
			continue
		}
		share := min(float64(len(recent))/float64(min(30, current-earliest+1)), 1)
		pace := share*(lastFive+thirty)/2 + (1-share)*7
		scored := race.scored(mp.ID)
		final := (*mp.Average*float64(scored) + pace*float64(race.length-scored)) / float64(race.length)
		if final < bestFinal {
			best, bestFinal = tip{mp, lastFive, thirty}, final
		}
	}
	return best, !math.IsInf(bestFinal, 1)
}

// leadersView is the question from the top of the table: how safe the lead
// is, measured against the nearest chaser.
func leadersView(t i18n.Translator, label, leaders string, m stats.Month, race race) string {
	chasers := runnersUp(m)
	if len(chasers) == 0 {
		return t.T("reply.catchup.alone", leaders, label)
	}
	chaser := chasers[0]
	need, left := race.need(chaser)
	points := race.pointsBehind(chaser)
	if left == 0 || need < impossibleBelow {
		return t.T("reply.catchup.safe", leaders, label, points, left)
	}
	return t.T("reply.catchup.leads", leaders, label, points, left, chaser.Name, t.Decimal(need, 2),
		leaders, t.Decimal(race.leader, 2))
}

// newRace reads the current month's arithmetic: its length, how much of it
// is over, and who has played today. m must have a leader.
func newRace(m stats.Month, results []store.BoardResult, now time.Time) race {
	current := wordle.PuzzleForDate(now)
	monthEnd := wordle.PuzzleForDate(time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location()))
	r := race{
		length:    monthEnd - m.First,
		concluded: current - m.First,
		afterNow:  monthEnd - current - 1,
		leader:    *m.Winners[0].Average,
		today:     map[int64]bool{},
	}
	for _, res := range results {
		if res.PuzzleNo == current {
			r.today[res.PlayerID] = true
		}
	}
	return r
}

// scored is how many of the month's days are in a player's average now:
// the concluded ones, a day not played counting 7, and today once played.
func (r race) scored(id int64) int {
	if r.today[id] {
		return r.concluded + 1
	}
	return r.concluded
}

// race is the month's arithmetic, shared by every line above.
type race struct {
	// length is the month's puzzles; concluded how many are over; afterNow
	// how many follow today.
	length, concluded, afterNow int
	leader                      float64
	// today is who has already played today's puzzle: for them today is
	// scored, for everyone else it is still a day left.
	today map[int64]bool
}

// need is the average over the chaser's remaining days that would put
// them past the leader's current average, and how many days that is.
func (r race) need(p stats.MonthPlayer) (float64, int) {
	scored := r.concluded
	left := r.afterNow + 1
	if r.today[p.ID] {
		scored++
		left--
	}
	if left <= 0 {
		return 0, 0
	}
	sum := *p.Average * float64(scored)
	return (r.leader*float64(r.length) - sum) / float64(left), left
}

// pointsBehind is the gap to the leader in hundredths of a guess, the unit
// every recap uses. Negative for the leader themselves.
func (r race) pointsBehind(p stats.MonthPlayer) int {
	return int(math.Round((*p.Average - r.leader) * 100))
}

func rankedPlayer(m stats.Month, id int64) (stats.MonthPlayer, bool) {
	for _, p := range m.Ranked {
		if p.ID == id {
			return p, true
		}
	}
	return stats.MonthPlayer{}, false
}

func isWinner(m stats.Month, id int64) bool {
	for _, w := range m.Winners {
		if w.ID == id {
			return true
		}
	}
	return false
}
