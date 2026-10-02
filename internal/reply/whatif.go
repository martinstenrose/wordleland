package reply

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// whatIf scores results that have not happened into the month: "if Martin
// gets a 6 tomorrow and Ibrahim a 3, who leads?".
//
// Each named player's average takes the made-up result as their next
// scored day — (average × days scored + result) ÷ (days scored + 1), the
// month's own arithmetic — and everyone else's stays where it is, which
// the answer says. That is the question as it is asked: what that one
// result would do, not a forecast of every day between now and then.
func whatIf(t i18n.Translator, req Request, players []store.Player,
	results []store.BoardResult, now time.Time) string {

	if len(req.Scores) == 0 {
		return t.T("reply.whatif.which")
	}
	type made struct {
		player  store.Player
		guesses int
	}
	var scores []made
	for _, h := range req.Scores {
		p, ok := findPlayer(h.Player, players)
		if !ok {
			all := make([]string, 0, len(players))
			for _, p := range players {
				all = append(all, p.Name)
			}
			if h.Player == Asker {
				// "Om jag får en 2:a" from somebody who is not a player.
				return t.T("reply.standing.who", joinNames(t, all))
			}
			return t.T("reply.player.unknown", h.Player, joinNames(t, all))
		}
		scores = append(scores, made{p, h.Guesses})
	}

	current := wordle.PuzzleForDate(now)
	day := whatIfDay(req, now, results, current, func(id int64) bool {
		for _, s := range scores {
			if s.player.ID == id {
				return true
			}
		}
		return false
	})
	puzzle := wordle.PuzzleForDate(day)
	label := dayLabel(t, day, now)
	switch {
	case puzzle < current:
		return t.T("reply.whatif.past")
	case day.Year() != now.Year() || day.Month() != now.Month():
		return t.T("reply.whatif.nextMonth", capitalized(t.T("month."+strconv.Itoa(int(now.Month())))))
	}

	m := currentMonth(players, results, now)
	if len(m.Winners) == 0 {
		return t.T("reply.leader.none", capitalized(t.T("month."+strconv.Itoa(int(now.Month())))))
	}
	race := newRace(m, results, now)

	// Everyone as they stand, then the made-up results on top.
	avg := map[int64]float64{}
	name := map[int64]string{}
	for _, mp := range m.Ranked {
		avg[mp.ID], name[mp.ID] = *mp.Average, mp.Name
	}
	var parts []string
	for _, s := range scores {
		if puzzle == current && race.today[s.player.ID] {
			// Today's result is in: it is not a what-if any more.
			return t.T("reply.whatif.played", s.player.Name)
		}
		n := race.scored(s.player.ID)
		a, ok := avg[s.player.ID]
		if !ok {
			// Nothing this month yet: every concluded day is a 7.
			a = 7
		}
		avg[s.player.ID] = (a*float64(n) + float64(s.guesses)) / float64(n+1)
		name[s.player.ID] = s.player.Name
		parts = append(parts, t.T("reply.whatif.gets", s.player.Name, t.T("reply.whatif.score."+strconv.Itoa(s.guesses))))
	}

	before := map[int64]bool{}
	for _, w := range m.Winners {
		before[w.ID] = true
	}
	ids := make([]int64, 0, len(avg))
	for id := range avg {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if avg[ids[i]] != avg[ids[j]] {
			return avg[ids[i]] < avg[ids[j]]
		}
		return name[ids[i]] < name[ids[j]]
	})
	// Compared in hundredths, the unit every average is shown in, so two
	// players the answer shows as equal are not ranked apart.
	hundredths := func(id int64) int { return int(math.Round(avg[id] * 100)) }
	var top []string
	same := true
	for _, id := range ids {
		if hundredths(id) != hundredths(ids[0]) {
			break
		}
		top = append(top, name[id])
		same = same && before[id]
	}
	same = same && len(top) == len(m.Winners)

	lines := []string{t.T("reply.whatif.head", joinNames(t, parts), label)}
	lead := t.Decimal(avg[ids[0]], 2)
	// Who the line about the lead names, so the lines after it do not
	// say them again.
	named := map[int64]bool{}
	switch {
	case len(top) > 1:
		lines = append(lines, t.T("reply.whatif.tie", joinNames(t, top), lead))
		for _, id := range ids[:len(top)] {
			named[id] = true
		}
	case len(ids) == 1:
		lines = append(lines, t.T("reply.whatif.alone", top[0], lead))
		named[ids[0]] = true
	default:
		next := ids[1]
		points := hundredths(next) - hundredths(ids[0])
		key := "reply.whatif.takes"
		if same {
			key = "reply.whatif.keeps"
		}
		lines = append(lines, t.T(key, top[0], lead, points, name[next], t.Decimal(avg[next], 2)))
		named[ids[0]], named[next] = true, true
	}
	// Where each other named player lands.
	for _, s := range scores {
		if named[s.player.ID] {
			continue
		}
		place := 1
		for _, id := range ids {
			if hundredths(id) < hundredths(s.player.ID) {
				place++
			}
		}
		lines = append(lines, t.T("reply.whatif.lands", s.player.Name, t.Decimal(avg[s.player.ID], 2), place))
	}
	note := t.T("reply.whatif.rest")
	if puzzle == wordle.PuzzleForDate(time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location()))-1 {
		note += " " + t.T("reply.whatif.lastDay")
	}
	lines = append(lines, note)
	return strings.Join(lines, "\n")
}

// whatIfDay is the day a what-if is about: the one named, or else today
// when none of the named players has played it yet, and tomorrow when one
// has — "if I get a 2" after posting today's means the next one.
func whatIfDay(req Request, now time.Time, results []store.BoardResult, current int, named func(int64) bool) time.Time {
	if req.Date != "" {
		if d, err := time.ParseInLocation(DateLayout, req.Date, now.Location()); err == nil {
			return d
		}
	}
	for _, r := range results {
		if r.PuzzleNo == current && named(r.PlayerID) {
			return now.AddDate(0, 0, 1)
		}
	}
	return now
}

// dayLabel names a day the way the group says it: today, tomorrow, or the
// date.
func dayLabel(t i18n.Translator, day, now time.Time) string {
	switch wordle.PuzzleForDate(day) - wordle.PuzzleForDate(now) {
	case 0:
		return t.T("reply.day.today")
	case 1:
		return t.T("reply.day.tomorrow")
	case -1:
		return t.T("reply.day.yesterday")
	}
	return t.T("reply.day.on", t.T("reply.date", day.Day(), t.T("month."+strconv.Itoa(int(day.Month())))))
}

// currentMonth is this month's standing under the default rules.
func currentMonth(players []store.Player, results []store.BoardResult, now time.Time) stats.Month {
	for _, m := range stats.ComputeMonths(players, results, stats.DefaultOptions(now)) {
		if m.Year == now.Year() && m.Month == now.Month() {
			return m
		}
	}
	return stats.Month{Year: now.Year(), Month: now.Month()}
}
