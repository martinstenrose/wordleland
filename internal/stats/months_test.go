package stats

import (
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

func monthOf(t *testing.T, months []Month, year int, m time.Month) Month {
	t.Helper()
	for _, mo := range months {
		if mo.Year == year && mo.Month == m {
			return mo
		}
	}
	t.Fatalf("no month %s %d in %d months", m, year, len(months))
	return Month{}
}

// The month a result belongs to comes from its own date, not from the
// puzzle number, so a month boundary lands where the calendar puts it.
func TestMonthsGroupByCalendarMonthNewestFirst(t *testing.T) {
	players := []store.Player{player(1, "alma")}
	results := run(1, 1780, 1900, 4, false)

	months := ComputeMonths(players, results, DefaultOptions(today(t)))
	if len(months) < 3 {
		t.Fatalf("got %d months, want several", len(months))
	}
	for i := 1; i < len(months); i++ {
		prev, cur := months[i-1], months[i]
		if prev.Year < cur.Year || (prev.Year == cur.Year && prev.Month < cur.Month) {
			t.Fatalf("months are not newest first: %v %d then %v %d",
				prev.Month, prev.Year, cur.Month, cur.Year)
		}
	}
	for _, m := range months {
		for _, p := range append(m.Ranked, m.Thin...) {
			_ = p
		}
		if m.Days == 0 {
			t.Errorf("%v %d has no days", m.Month, m.Year)
		}
	}
}

// Below the ten-game minimum a month average is withheld, exactly as on the
// board, and the player is listed as played-but-not-ranked instead.
func TestMonthRankingNeedsTenGames(t *testing.T) {
	players := []store.Player{player(1, "regular"), player(2, "cameo")}

	// Both play in the same month; one only three times.
	results := run(1, 1871, 1890, 4, false)
	results = append(results, run(2, 1871, 1873, 2, false)...)

	date, _ := wordle.DateForPuzzle(1871)
	months := ComputeMonths(players, results, DefaultOptions(today(t)))
	m := monthOf(t, months, date.Year(), date.Month())

	if len(m.Ranked) != 1 || m.Ranked[0].Slug != "regular" {
		t.Errorf("Ranked = %v, want just the regular", slugs(m.Ranked))
	}
	if len(m.Thin) != 1 || m.Thin[0].Slug != "cameo" {
		t.Errorf("Thin = %v, want just the cameo", slugs(m.Thin))
	}
	// The cameo's three 2s must not win the month.
	if m.Winners[0].Slug != "regular" {
		t.Errorf("winner = %s, want the regular", m.Winners[0].Slug)
	}
	if m.Thin[0].Average != nil {
		t.Error("a below-threshold month average was computed anyway")
	}
}

// A tie is a real outcome. Picking one of them by sort order would invent a
// winner the data does not have.
func TestMonthTiesAreJoined(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse"), player(3, "cilla")}

	results := run(1, 1871, 1890, 3, false)
	results = append(results, run(2, 1871, 1890, 3, false)...)
	results = append(results, run(3, 1871, 1890, 5, false)...)

	date, _ := wordle.DateForPuzzle(1871)
	m := monthOf(t, ComputeMonths(players, results, DefaultOptions(today(t))), date.Year(), date.Month())

	if len(m.Winners) != 2 {
		t.Fatalf("Winners = %v, want both tied players", slugs(m.Winners))
	}
	// Both hold rank 1, and the next distinct average skips to 3.
	if m.Ranked[0].Rank != 1 || m.Ranked[1].Rank != 1 {
		t.Errorf("tied ranks = %d and %d, want 1 and 1", m.Ranked[0].Rank, m.Ranked[1].Rank)
	}
	if m.Ranked[2].Rank != 3 {
		t.Errorf("third rank = %d, want 3", m.Ranked[2].Rank)
	}
	if m.Margin == nil {
		t.Fatal("no margin over the next player")
	}
	if got := *m.Margin; got < 1.99 || got > 2.01 {
		t.Errorf("margin = %.2f, want 2.00", got)
	}
}

func TestMonthCountsThreeOrBetterFailsAndBestRun(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	var results []store.BoardResult
	results = append(results, run(1, 1871, 1875, 2, false)...) // five 2s
	results = append(results, result(1, 1876, 0, false))       // a failure breaks the run
	results = append(results, run(1, 1877, 1882, 5, false)...) // six 5s
	results = append(results, result(1, 1884, 4, false))       // gap at 1883

	date, _ := wordle.DateForPuzzle(1871)
	m := monthOf(t, ComputeMonths(players, results, DefaultOptions(today(t))), date.Year(), date.Month())
	p := m.Ranked[0]

	if p.Games != 13 {
		t.Errorf("Games = %d, want 13", p.Games)
	}
	if p.ThreeOrBetter != 5 {
		t.Errorf("ThreeOrBetter = %d, want 5", p.ThreeOrBetter)
	}
	if p.Fails != 1 {
		t.Errorf("Fails = %d, want 1", p.Fails)
	}
	if p.BestRun != 6 {
		t.Errorf("BestRun = %d, want 6 — the failure and the gap both break it", p.BestRun)
	}
}

// Filtering changes the population, not the arithmetic, here too.
func TestMonthsHonourTheHardModeFilter(t *testing.T) {
	players, results := realisticRoster(t)
	opts := DefaultOptions(today(t))

	all := ComputeMonths(players, results, opts)
	opts.HardModeOnly = true
	hard := ComputeMonths(players, results, opts)

	countPlayers := func(ms []Month) int {
		seen := map[string]bool{}
		for _, m := range ms {
			for _, p := range append(m.Ranked, m.Thin...) {
				seen[p.Slug] = true
			}
		}
		return len(seen)
	}
	if a, h := countPlayers(all), countPlayers(hard); h >= a {
		t.Errorf("hard-mode months cover %d players, unfiltered %d — the filter changed nothing", h, a)
	}
	for _, m := range hard {
		for _, p := range append(m.Ranked, m.Thin...) {
			if p.Slug[:4] == "norm" {
				t.Errorf("%s has no hard-mode games but appears in %v %d", p.Slug, m.Month, m.Year)
			}
		}
	}
}

func slugs(ps []MonthPlayer) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Slug)
	}
	return out
}

// A month still being played has a leader, not a winner, so it must not
// count toward the season — that would hand somebody a title they might
// yet lose.
func TestSeasonCountsClosedMonthsOnly(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "bosse")}

	// Alma leads the current month; bosse won the one before it.
	current := today(t)
	var results []store.BoardResult
	results = append(results, run(1, 1871, 1900, 2, false)...)
	results = append(results, run(2, 1871, 1900, 5, false)...)
	results = append(results, run(1, 1830, 1860, 5, false)...)
	results = append(results, run(2, 1830, 1860, 2, false)...)

	months := ComputeMonths(players, results, DefaultOptions(current))
	season := ComputeSeason(months, current)

	byPlayer := map[string]SeasonRow{}
	for _, r := range season.Rows {
		byPlayer[r.Slug] = r
	}

	// The running month is marked as such and won by nobody yet.
	var running int
	for _, m := range byPlayer["alma"].Marks {
		if m.Running {
			running++
			if m.Won && byPlayer["alma"].Wins > 0 {
				t.Error("a lead in an unfinished month was counted as a win")
			}
		}
	}
	if running == 0 {
		t.Fatal("no month is marked as still running")
	}

	// And every player has a mark for every month, so the row lines up.
	if a, b := len(byPlayer["alma"].Marks), len(byPlayer["bosse"].Marks); a != b {
		t.Errorf("mark counts differ: %d and %d", a, b)
	}
}

// Not being ranked in a month is different from finishing last.
func TestSeasonMarksUnrankedMonthsDistinctly(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "cameo")}

	results := run(1, 1871, 1900, 3, false)
	results = append(results, run(2, 1871, 1873, 3, false)...) // three games only

	months := ComputeMonths(players, results, DefaultOptions(today(t)))
	season := ComputeSeason(months, today(t))

	for _, row := range season.Rows {
		if row.Slug != "cameo" {
			continue
		}
		for _, m := range row.Marks {
			if m.Rank != 0 {
				t.Errorf("a player below the minimum was given rank %d", m.Rank)
			}
		}
		return
	}
	t.Fatal("the cameo is missing from the season")
}

// A month is a competition over a fixed set of days: turning up eleven
// times with good scores must not beat turning up every day.
func TestMonthCountsAMissedDayAsAFailure(t *testing.T) {
	players := []store.Player{player(1, "everyday"), player(2, "cherry")}

	// The group plays 1871-1890. Everyday plays all twenty at 4; Cherry
	// plays half of them at 2 and skips the rest.
	results := run(1, 1871, 1890, 4, false)
	for p := 1871; p <= 1880; p++ {
		results = append(results, result(2, p, 2, false))
	}

	date, _ := wordle.DateForPuzzle(1871)
	m := monthOf(t, ComputeMonths(players, results, DefaultOptions(today(t))),
		date.Year(), date.Month())

	if len(m.Ranked) != 2 {
		t.Fatalf("Ranked = %v, want both", slugs(m.Ranked))
	}
	// Cherry: ten 2s and ten missed days at 7 = 4.5, behind Everyday's 4.
	if got := *m.Ranked[0].Average; got != 4 || m.Ranked[0].Slug != "everyday" {
		t.Errorf("winner is %s on %.2f, want everyday on 4.00", m.Ranked[0].Slug, got)
	}
	if got := *m.Ranked[1].Average; got != 4.5 {
		t.Errorf("cherry averages %.2f, want 4.50 — ten 2s and ten misses at 7", got)
	}
	// Games counts days played, not the denominator the average uses.
	if m.Ranked[1].Games != 10 {
		t.Errorf("cherry played %d games, want 10", m.Ranked[1].Games)
	}
}

// Missed days follow CountXAsSeven: with a failure worth nothing there is
// no number an absence could take either.
func TestMonthMissedDaysFollowCountXAsSeven(t *testing.T) {
	players := []store.Player{player(1, "everyday"), player(2, "cherry")}
	results := run(1, 1871, 1890, 4, false)
	for p := 1871; p <= 1880; p++ {
		results = append(results, result(2, p, 2, false))
	}

	opts := DefaultOptions(today(t))
	opts.CountXAsSeven = false

	date, _ := wordle.DateForPuzzle(1871)
	m := monthOf(t, ComputeMonths(players, results, opts), date.Year(), date.Month())

	if len(m.Ranked) != 2 {
		t.Fatalf("Ranked = %v, want both", slugs(m.Ranked))
	}
	if got := *m.Ranked[0].Average; got != 2 || m.Ranked[0].Slug != "cherry" {
		t.Errorf("winner is %s on %.2f, want cherry on 2.00", m.Ranked[0].Slug, got)
	}
}

// Today is still in progress: a player who hasn't posted yet may still play
// it correctly, so it must not be scored as a miss just because somebody
// else already has.
func TestMonthDoesNotCountTodayAsMissedYet(t *testing.T) {
	players := []store.Player{player(1, "everyday"), player(2, "latecomer")}

	// Everyday has already played today's puzzle (2020). Latecomer has
	// played every earlier day this month but not yet today.
	results := run(1, 2001, 2020, 4, false)
	results = append(results, run(2, 2001, 2019, 2, false)...)

	now, err := wordle.DateForPuzzle(2020)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}

	date, _ := wordle.DateForPuzzle(2001)
	m := monthOf(t, ComputeMonths(players, results, DefaultOptions(now)),
		date.Year(), date.Month())

	if len(m.Ranked) != 2 {
		t.Fatalf("Ranked = %v, want both", slugs(m.Ranked))
	}
	var latecomer MonthPlayer
	for _, p := range m.Ranked {
		if p.Slug == "latecomer" {
			latecomer = p
		}
	}
	if got := *latecomer.Average; got != 2 {
		t.Errorf("latecomer averages %.2f, want 2.00 — today isn't missed until it's over", got)
	}
}

// The denominator is the days the group played, not the calendar: a day
// nobody posted is not a day anybody missed.
func TestMonthIgnoresDaysNobodyPlayed(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	// Twelve games inside a month, with a fortnight nobody touched.
	results := run(1, 1871, 1882, 4, false)

	date, _ := wordle.DateForPuzzle(1871)
	m := monthOf(t, ComputeMonths(players, results, DefaultOptions(today(t))),
		date.Year(), date.Month())

	if len(m.Ranked) != 1 {
		t.Fatalf("Ranked = %v, want alma", slugs(m.Ranked))
	}
	if got := *m.Ranked[0].Average; got != 4 {
		t.Errorf("alma averages %.2f, want 4.00 — the days nobody played are not misses", got)
	}
}
