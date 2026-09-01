package stats

import (
	"math"
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

// Monthly ranking has no minimum attendance threshold. Missed concluded
// days already prevent a short run of good scores from winning the month.
func TestMonthRanksPlayersBelowTenGames(t *testing.T) {
	players := []store.Player{player(1, "regular"), player(2, "cameo")}

	// Both play in the same month; one only three times.
	results := run(1, 1871, 1890, 4, false)
	results = append(results, run(2, 1871, 1873, 2, false)...)

	date, _ := wordle.DateForPuzzle(1871)
	months := ComputeMonths(players, results, DefaultOptions(today(t)))
	m := monthOf(t, months, date.Year(), date.Month())

	if got := slugs(m.Ranked); len(got) != 2 || got[0] != "regular" || got[1] != "cameo" {
		t.Errorf("Ranked = %v, want regular then cameo", got)
	}
	if len(m.Thin) != 0 {
		t.Errorf("Thin = %v, want nobody", slugs(m.Thin))
	}
	// The cameo's three 2s and 28 missed August days score 202/31, so removing
	// the threshold does not let a cherry-picked appearance win the month.
	if m.Winners[0].Slug != "regular" {
		t.Errorf("winner = %s, want the regular", m.Winners[0].Slug)
	}
	if got, want := *m.Ranked[1].Average, 202.0/31; got != want {
		t.Errorf("cameo average = %.2f, want %.2f", got, want)
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
	if got, want := *m.Margin, 40.0/31; math.Abs(got-want) > 1e-12 {
		t.Errorf("margin = %.2f, want %.2f", got, want)
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

	// Alma leads September; bosse won August. Use a fixed clock and derive
	// puzzle numbers from it so the fixture cannot cross a real month boundary.
	current := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.Local)
	september := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	august := wordle.PuzzleForDate(time.Date(2026, time.August, 1, 0, 0, 0, 0, time.Local))
	var results []store.BoardResult
	results = append(results, run(1, september, september+11, 2, false)...)
	results = append(results, run(2, september, september+11, 5, false)...)
	results = append(results, run(1, august, august+11, 5, false)...)
	results = append(results, run(2, august, august+11, 2, false)...)

	months := ComputeMonths(players, results, DefaultOptions(current))
	season := ComputeSeason(months, current)

	byPlayer := map[string]SeasonRow{}
	for _, r := range season.Rows {
		byPlayer[r.Slug] = r
	}

	// The running month is marked as such but is not counted as Alma's win.
	if byPlayer["alma"].Wins != 0 {
		t.Errorf("Alma has %d wins, want 0 while only leading September", byPlayer["alma"].Wins)
	}
	if byPlayer["bosse"].Wins != 1 {
		t.Errorf("Bosse has %d wins, want the closed August win", byPlayer["bosse"].Wins)
	}
	var running int
	for _, m := range byPlayer["alma"].Marks {
		if m.Running {
			running++
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

// Even a short appearance gets a monthly place and therefore a season mark.
func TestSeasonMarksMonthsBelowTenGames(t *testing.T) {
	players := []store.Player{player(1, "alma"), player(2, "cameo")}

	results := run(1, 1871, 1900, 3, false)
	results = append(results, run(2, 1871, 1873, 3, false)...) // three games only

	months := ComputeMonths(players, results, DefaultOptions(today(t)))
	season := ComputeSeason(months, today(t))
	wantDate, _ := wordle.DateForPuzzle(1871)

	for _, row := range season.Rows {
		if row.Slug != "cameo" {
			continue
		}
		for _, m := range row.Marks {
			if m.Year == wantDate.Year() && m.Month == wantDate.Month() {
				if m.Rank == 0 {
					t.Error("a player below ten games was left unranked")
				}
				return
			}
		}
		t.Fatal("the cameo has no season mark for its month")
	}
	t.Fatal("the cameo is missing from the season")
}

// When two players are tied on wins and podiums, SeasonRow.Ranked breaks
// the tie: the player ranked in more months sits above one ranked in
// fewer, even if that other player's single best month was better.
func TestSeasonBreaksWinPodiumTiesOnMonthsRanked(t *testing.T) {
	players := []store.Player{
		player(1, "carl"), player(2, "dana"), player(3, "elin"),
		player(4, "alma"), player(5, "bosse"),
	}

	current := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.Local)
	augStart := wordle.PuzzleForDate(time.Date(2026, time.August, 1, 0, 0, 0, 0, time.Local))
	septStart := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	octStart := wordle.PuzzleForDate(time.Date(2026, time.October, 1, 0, 0, 0, 0, time.Local))
	augEnd := septStart - 1
	septEnd := octStart - 1

	var results []store.BoardResult
	// Carl, dana and elin take the podium in both months.
	results = append(results, run(1, augStart, septEnd, 2, false)...)
	results = append(results, run(2, augStart, septEnd, 3, false)...)
	results = append(results, run(3, augStart, septEnd, 4, false)...)
	// Alma is ranked but off the podium in both months.
	results = append(results, run(4, augStart, septEnd, 6, false)...)
	// Bosse only plays August, off the podium there too, but with a
	// better average than alma ever manages.
	results = append(results, run(5, augStart, augEnd, 5, false)...)

	months := ComputeMonths(players, results, DefaultOptions(current))
	season := ComputeSeason(months, current)

	byPlayer := map[string]SeasonRow{}
	for _, r := range season.Rows {
		byPlayer[r.Slug] = r
	}
	alma, bosse := byPlayer["alma"], byPlayer["bosse"]

	if alma.Wins != bosse.Wins || alma.Podiums != bosse.Podiums {
		t.Fatalf("fixture is not tied: alma wins=%d podiums=%d, bosse wins=%d podiums=%d",
			alma.Wins, alma.Podiums, bosse.Wins, bosse.Podiums)
	}
	if alma.Ranked <= bosse.Ranked {
		t.Fatalf("alma.Ranked = %d, want more than bosse.Ranked = %d", alma.Ranked, bosse.Ranked)
	}
	if *alma.Best <= *bosse.Best {
		t.Fatalf("fixture is not discriminating: alma.Best = %v should be worse than bosse.Best = %v",
			*alma.Best, *bosse.Best)
	}

	var almaPos, bossePos int
	for i, r := range season.Rows {
		switch r.Slug {
		case "alma":
			almaPos = i
		case "bosse":
			bossePos = i
		}
	}
	if almaPos >= bossePos {
		t.Errorf("alma (ranked %d months) sorted below bosse (ranked %d months) despite a tie on wins and podiums",
			alma.Ranked, bosse.Ranked)
	}
}

// A month is a competition over a fixed set of days: turning up eleven
// times with good scores must not beat turning up every day.
func TestMonthCountsAMissedDayAsAFailure(t *testing.T) {
	players := []store.Player{player(1, "everyday"), player(2, "cherry")}

	// The group posts on 1871-1890. Everyday plays all twenty at 4; Cherry
	// plays half of them at 2. The other eleven August days count too.
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
	// Everyday: twenty 4s and eleven misses. Cherry: ten 2s and 21 misses.
	if got, want := *m.Ranked[0].Average, 157.0/31; got != want || m.Ranked[0].Slug != "everyday" {
		t.Errorf("winner is %s on %.2f, want everyday on %.2f", m.Ranked[0].Slug, got, want)
	}
	if got, want := *m.Ranked[1].Average, 167.0/31; got != want {
		t.Errorf("cherry averages %.2f, want %.2f — ten 2s and 21 misses at 7", got, want)
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

	// Everyday has already played September 10. Latecomer has played from
	// the first through the ninth, but still has the rest of today to play.
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.Local)
	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	current := wordle.PuzzleForDate(now)
	results := run(1, first, current, 4, false)
	results = append(results, run(2, first, current-1, 2, false)...)

	m := monthOf(t, ComputeMonths(players, results, DefaultOptions(now)),
		now.Year(), now.Month())

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

// The monthly window is the calendar, so a concluded day counts as missed
// even when nobody in the group posted a result for it.
func TestMonthCountsDaysNobodyPlayed(t *testing.T) {
	players := []store.Player{player(1, "alma")}

	// Twelve games from the start of August, then no posts for 19 days.
	first := wordle.PuzzleForDate(time.Date(2026, time.August, 1, 0, 0, 0, 0, time.Local))
	results := run(1, first, first+11, 4, false)

	date, _ := wordle.DateForPuzzle(first)
	m := monthOf(t, ComputeMonths(players, results, DefaultOptions(today(t))),
		date.Year(), date.Month())

	if len(m.Ranked) != 1 {
		t.Fatalf("Ranked = %v, want alma", slugs(m.Ranked))
	}
	if got, want := *m.Ranked[0].Average, 181.0/31; got != want {
		t.Errorf("alma averages %.2f, want %.2f — 12 fours and 19 missed days", got, want)
	}
	if m.Days != 31 {
		t.Errorf("month covers %d days, want all 31 calendar days", m.Days)
	}
}
