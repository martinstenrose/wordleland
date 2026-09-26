package stats

import (
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// A span longer than the group's history starts where the history does.
// Days before the first result are nobody's misses: a ten-day-old group
// asked about the last hundred days is scored over ten.
func TestRecentStartsWhereTheHistoryDoes(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bo")}
	now := today(t)
	current := wordle.PuzzleForDate(now)

	results := run(1, current-9, current, 3, false)
	results = append(results, run(2, current-9, current, 4, false)...)

	m := ComputeRecent(players, results, DefaultOptions(now), 100)

	if m.Days != 10 {
		t.Errorf("Days = %d, want the 10 the group has played", m.Days)
	}
	if got := slugs(m.Ranked); len(got) != 2 || got[0] != "alma" {
		t.Fatalf("ranked %v, want alma then bo", got)
	}
	if *m.Ranked[0].Average != 3 || *m.Ranked[1].Average != 4 {
		t.Errorf("averages %v and %v, want the played 3 and 4 with no phantom misses",
			*m.Ranked[0].Average, *m.Ranked[1].Average)
	}

	if empty := ComputeRecent(players, nil, DefaultOptions(now), 7); len(empty.Ranked) != 0 || empty.Days != 0 {
		t.Errorf("no results scored something: %+v", empty)
	}
}
