package reply

import (
	"fmt"
	"sort"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// habits reads the posting order the daily recap reads — who opened and
// closed the day, over the last stats.HabitWindow days that had an order —
// and, for one player, when they usually post: the median of their recent
// posting times, which one late night does not move.
func habits(t i18n.Translator, req Request, asker *store.Player,
	players []store.Player, results []store.BoardResult, now time.Time) string {

	current := wordle.PuzzleForDate(now)
	h := stats.ComputePostingHabits(results, current)

	// Nobody named is the group's habits, whoever is asking: "who posts
	// first?" from a claimed player is not about them.
	if req.Player != "" {
		p, ok, text := whom(t, req, asker, players)
		if !ok {
			return text
		}
		clock, ok := usualTime(results, p.ID, current)
		if !ok {
			return t.T("reply.habits.player.none", p.Name)
		}
		return t.T("reply.habits.player", p.Name, clock, h.First[p.ID].Days, h.Last[p.ID].Days, h.Days)
	}

	if h.Days == 0 {
		return t.T("reply.habits.none")
	}
	first, firstDays := mostDays(h.First, players)
	last, lastDays := mostDays(h.Last, players)
	return t.T("reply.habits.first", joinNames(t, first), firstDays, h.Days) + "\n" +
		t.T("reply.habits.last", joinNames(t, last), lastDays)
}

// mostDays names whoever holds a position most often, ties in name order.
func mostDays(habits map[int64]stats.Habit, players []store.Player) ([]string, int) {
	best := 0
	for _, h := range habits {
		best = max(best, h.Days)
	}
	var names []string
	for _, p := range players {
		if habits[p.ID].Days == best && best > 0 {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, best
}

// usualTime is the median wall-clock time of a player's last HabitWindow
// stamped results, on the clock where the group lives.
func usualTime(results []store.BoardResult, player int64, through int) (string, bool) {
	var minutes []int
	for i := len(results) - 1; i >= 0 && len(minutes) < stats.HabitWindow; i-- {
		r := results[i]
		if r.PlayerID != player || r.PostedAt == nil || r.PuzzleNo > through {
			continue
		}
		at := r.PostedAt.In(time.Local)
		minutes = append(minutes, at.Hour()*60+at.Minute())
	}
	if len(minutes) == 0 {
		return "", false
	}
	sort.Ints(minutes)
	median := minutes[len(minutes)/2]
	return fmt.Sprintf("%02d:%02d", median/60, median%60), true
}
