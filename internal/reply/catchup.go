package reply

import (
	"math"
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

	current := wordle.PuzzleForDate(now)
	monthEnd := wordle.PuzzleForDate(time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location()))
	race := race{
		length:    monthEnd - m.First,
		concluded: current - m.First,
		afterNow:  monthEnd - current - 1,
		leader:    *m.Winners[0].Average,
		today:     map[int64]bool{},
	}
	for _, r := range results {
		if r.PuzzleNo == current {
			race.today[r.PlayerID] = true
		}
	}
	leaders := joinNames(t, names(m.Winners))
	leaderAvg := t.Decimal(race.leader, 2)

	if req.Player != "" || asker != nil {
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

	// Nobody in particular: everyone's chances at once.
	var in, out []string
	for _, mp := range m.Ranked {
		if isWinner(m, mp.ID) {
			continue
		}
		need, left := race.need(mp)
		if left == 0 || need < impossibleBelow {
			out = append(out, mp.Name)
			continue
		}
		in = append(in, t.T("reply.catchup.needs", mp.Name, t.Decimal(need, 2)))
	}
	lines := []string{t.T("reply.catchup.head", capitalized(label), leaders, leaderAvg, race.afterNow)}
	if len(in) > 0 {
		lines = append(lines, t.T("reply.catchup.in", joinNames(t, in)))
	}
	if len(out) > 0 {
		lines = append(lines, t.T("reply.catchup.out", joinNames(t, out)))
	}
	return joinLines(lines)
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

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
