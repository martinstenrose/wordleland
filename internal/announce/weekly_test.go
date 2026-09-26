package announce

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// The test week is Monday 14 to Sunday 20 September 2026, ISO week 38.
const testMonday = 14

// scores is one player's week, Monday first: 0 is an X, and skip a day not
// played.
type scores struct {
	name string
	week [7]int
}

const skip = -1

// seedWeek creates the players and files their week, starting on the Monday
// start days into September.
func seedWeek(t *testing.T, db *sql.DB, start int, players ...scores) {
	t.Helper()
	for _, p := range players {
		slug := strings.ToLower(p.name)
		if _, err := store.PlayerBySlug(context.Background(), db, slug); err != nil {
			mustPlayer(t, db, p.name, slug)
		}
		for i, g := range p.week {
			if g == skip {
				continue
			}
			record(t, db, slug, day(start+i), g > 0, g)
		}
	}
}

// theWeek is a week with something in every core line: a podium and a clear
// last place.
func theWeek(t *testing.T, db *sql.DB) {
	seedWeek(t, db, testMonday,
		scores{"Alice", [7]int{3, 3, 3, 3, 3, 3, 3}},
		scores{"Bob", [7]int{3, 4, 3, 4, 3, 4, 3}},
		scores{"Carol", [7]int{2, 4, 4, 4, 4, 0, 4}},
		scores{"Dave", [7]int{4, 5, 5, 5, 5, 6, 5}},
	)
}

func weeklyCheck(t *testing.T, db *sql.DB, locale string, dailyGoesFirst bool, c *collector) func(context.Context, time.Time) error {
	t.Helper()
	return NewWeekly(db, loadCatalogues(t), locale, dailyGoesFirst, c.send)
}

// The headline case: Sunday's last result posts the day's recap and then the
// week's, in that order and on the spot.
func TestSundaysFullHousePostsTheDayThenTheWeek(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()
	theWeek(t, db)
	alreadyPosted(t, db, 2026, time.September, 19)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	weekly := weeklyCheck(t, db, "en", true, &c)
	now := day(20).Add(21 * time.Hour)
	for _, check := range []func(context.Context, time.Time) error{daily, weekly} {
		if err := check(ctx, now); err != nil {
			t.Fatal(err)
		}
	}

	if len(c.sent) != 2 {
		t.Fatalf("sent %d messages, want the day then the week: %q", len(c.sent), c.sent)
	}
	if !strings.HasPrefix(c.sent[0], "🏁") {
		t.Errorf("first message = %q, want Sunday's recap", c.sent[0])
	}
	want := "🗓️ Week 38 is done: 4 players, 28 results.\n" +
		"🥇 Alice 3.00 · 🥈 Bob 3.43 · 🥉 Carol 4.14\n" +
		"🥄 Dave brings up the rear at 5.00.\n" +
		"📊 The group averaged 3.89.\n" +
		"👑 Alice had the day's best on 6 of 7 days.\n" +
		"🎢 Rollercoaster week for Carol: a 2 and an X.\n" +
		"✅ Everyone played all seven days."
	if c.sent[1] != want {
		t.Errorf("week =\n%s\nwant\n%s", c.sent[1], want)
	}
}

// Until Sunday is a full house the week is still being played, and the run
// after midnight closes it anyway — the missing Sunday scored as a miss.
func TestTheWeekWaitsForSundayThenMidnight(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()
	theWeek(t, db)
	seedWeek(t, db, testMonday, scores{"Erin", [7]int{3, 3, 3, 3, 3, 3, skip}})

	var c collector
	weekly := weeklyCheck(t, db, "en", false, &c)
	if err := weekly(ctx, day(20).Add(21*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c.calls.Load() != 0 {
		t.Fatalf("posted before Erin filed Sunday: %q", c.sent)
	}

	if err := weekly(ctx, day(21).Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Erin's six 3s and a missed Sunday: 25/7.
	if got := lineWith(t, c.only(t), "🥇"); !strings.Contains(got, "🥈 Bob 3.43 · 🥉 Erin 3.57") {
		t.Errorf("podium = %q, want Erin's missed Sunday counted as 7", got)
	}
}

// With the day's recap on, the week does not go out ahead of Sunday's. Once
// Sunday's recap can no longer come — the day's check looks one day back —
// the week stops waiting for it.
func TestTheWeekWaitsForSundaysRecapWhileItCanStillCome(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		now  time.Time
		want int32
	}{
		{"Sunday evening", day(20).Add(21 * time.Hour), 0},
		{"Monday", day(21).Add(time.Minute), 0},
		{"Tuesday", day(22).Add(time.Minute), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := announceDB(t)
			theWeek(t, db)
			var c collector
			if err := weeklyCheck(t, db, "en", true, &c)(ctx, tc.now); err != nil {
				t.Fatal(err)
			}
			if c.calls.Load() != tc.want {
				t.Errorf("sent %d, want %d", c.calls.Load(), tc.want)
			}
		})
	}
}

// Posted once: a second check the same evening, and the run after midnight,
// find it done.
func TestTheWeekIsPostedOnce(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()
	theWeek(t, db)

	var c collector
	weekly := weeklyCheck(t, db, "en", false, &c)
	for _, now := range []time.Time{day(20).Add(21 * time.Hour), day(20).Add(22 * time.Hour), day(21).Add(time.Minute)} {
		if err := weekly(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	if c.calls.Load() != 1 {
		t.Errorf("sent %d times, want 1", c.calls.Load())
	}
}

func TestConcurrentWeeklyChecksSendOnce(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()
	theWeek(t, db)

	var c collector
	weekly := weeklyCheck(t, db, "en", false, &c)
	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- weekly(ctx, day(21).Add(time.Minute))
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent check: %v", err)
		}
	}
	if c.calls.Load() != 1 {
		t.Errorf("send called %d times, want 1", c.calls.Load())
	}
}

func TestFailedWeeklySendIsNotRecordedAndIsRetried(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()
	theWeek(t, db)
	first := wordle.PuzzleForDate(day(testMonday))

	c := collector{fail: errors.New("signal is unreachable")}
	weekly := weeklyCheck(t, db, "en", false, &c)
	if err := weekly(ctx, day(21).Add(time.Minute)); err == nil {
		t.Fatal("weekly() succeeded despite the send failing")
	}
	if done, _ := store.WeekAnnounced(ctx, db, first); done {
		t.Fatal("a failed send was recorded as announced")
	}

	c.fail = nil
	if err := weekly(ctx, day(21).Add(time.Hour)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if done, _ := store.WeekAnnounced(ctx, db, first); !done || len(c.sent) != 1 {
		t.Errorf("the retry was not sent and recorded: sent %q, recorded %v", c.sent, done)
	}
}

// A week nobody played, or only one player did, is not a competition and
// says nothing — and is left unrecorded, so a late result can still count.
func TestAnEmptyOrSoloWeekIsSilent(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()
	mustPlayer(t, db, "Alice", "alice")

	var c collector
	weekly := weeklyCheck(t, db, "en", false, &c)
	if err := weekly(ctx, day(21).Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	seedWeek(t, db, testMonday, scores{"Alice", [7]int{3, 3, 3, 3, 3, 3, 3}})
	if err := weekly(ctx, day(21).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c.calls.Load() != 0 {
		t.Errorf("sent %q for a week with nobody to compete with", c.sent)
	}
	if done, _ := store.WeekAnnounced(ctx, db, wordle.PuzzleForDate(day(testMonday))); done {
		t.Error("a silent week was recorded as announced")
	}
}

// A fresh deployment on a Monday does not open with last week, and later
// starts leave the record alone.
func TestSkipWeeklyBacklogOnlyEverRunsOnce(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()
	theWeek(t, db)

	if err := SkipWeeklyBacklog(ctx, db, day(21).Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var c collector
	if err := weeklyCheck(t, db, "en", false, &c)(ctx, day(21).Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c.calls.Load() != 0 {
		t.Errorf("a first start posted last week: %q", c.sent)
	}

	// A week later, with the table no longer empty, it does nothing.
	if err := SkipWeeklyBacklog(ctx, db, day(28).Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if done, _ := store.WeekAnnounced(ctx, db, wordle.PuzzleForDate(day(21))); done {
		t.Error("a later start marked another week done")
	}
}

// Last place goes to the last of those who played most of the week, never
// to somebody who is last because they were away.
func TestTheWoodenSpoonSkipsAbsentees(t *testing.T) {
	db := announceDB(t)
	theWeek(t, db)
	seedWeek(t, db, testMonday, scores{"Erin", [7]int{2, 2, skip, skip, skip, skip, skip}})

	got := weekPost(t, db, "en")
	if line := lineWith(t, got, "🥄"); line != "🥄 Dave brings up the rear at 5.00." {
		t.Errorf("spoon = %q, want Dave, the last who played", line)
	}
	if strings.Contains(got, "Erin") {
		t.Errorf("Erin, away five days, is named:\n%s", got)
	}
	if line := lineWith(t, got, "✅"); line != "" {
		t.Errorf("attendance = %q, want none when not everyone played every day", line)
	}
}

// In a group of four the last of the regulars can be on the podium, and a
// medal and a spoon for the same player is a contradiction.
func TestNoSpoonForSomebodyOnThePodium(t *testing.T) {
	db := announceDB(t)
	seedWeek(t, db, testMonday,
		scores{"Alice", [7]int{3, 3, 3, 3, 3, 3, 3}},
		scores{"Bob", [7]int{4, 4, 4, 4, 4, 4, 4}},
		scores{"Carol", [7]int{2, 2, skip, skip, skip, skip, skip}},
	)
	if line := lineWith(t, weekPost(t, db, "en"), "🥄"); line != "" {
		t.Errorf("spoon = %q, want none", line)
	}
}

// Against a previous week the group's average says which way it went.
func TestTheAverageComparesWithLastWeek(t *testing.T) {
	db := announceDB(t)
	seedWeek(t, db, testMonday-7,
		scores{"Alice", [7]int{4, 4, 4, 4, 4, 4, 4}},
		scores{"Bob", [7]int{4, 4, 4, 4, 4, 4, 4}},
	)
	seedWeek(t, db, testMonday,
		scores{"Alice", [7]int{3, 3, 3, 3, 3, 3, 4}},
		scores{"Bob", [7]int{4, 4, 4, 4, 4, 4, 4}},
	)
	if got, want := lineWith(t, weekPost(t, db, "en"), "📊"),
		"📊 The group averaged 3.57, 0.43 better than last week."; got != want {
		t.Errorf("average = %q, want %q", got, want)
	}
}

// Each extra, alone: what it says when it fires.
func TestTheWeeksExtras(t *testing.T) {
	for _, tc := range []struct {
		name    string
		players []scores
		prefix  string
		want    string
	}{
		{
			"a photo finish between neighbours",
			[]scores{
				{"Alice", [7]int{3, 3, 3, 3, 3, 3, 4}},
				{"Bob", [7]int{3, 3, 3, 3, 3, 4, 4}},
				{"Carol", [7]int{5, 5, 5, 5, 5, 5, 5}},
			},
			"📸", "📸 Photo finish: Alice ahead of Bob by 0.14.",
		},
		{
			"a metronome when nobody swung",
			[]scores{
				{"Alice", [7]int{3, 3, 3, 3, 3, 3, 3}},
				{"Bob", [7]int{5, 3, 6, 3, 5, 3, 5}},
			},
			"🎯", "🎯 Like clockwork: Alice, a 3 every single day.",
		},
		{
			"no metronome for a week of 3s and 4s",
			[]scores{
				{"Alice", [7]int{3, 4, 3, 4, 3, 4, 3}},
				{"Bob", [7]int{5, 3, 6, 3, 5, 3, 5}},
			},
			"🎯", "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := announceDB(t)
			seedWeek(t, db, testMonday, tc.players...)
			if got := lineWith(t, weekPost(t, db, "en"), tc.prefix); got != tc.want {
				t.Errorf("line = %q, want %q", got, tc.want)
			}
		})
	}
}

// A run of weeks at the top is said from the second, and its end from the
// third; a shared win counts for everyone who shares it.
func TestRunsOfWeeksAtTheTop(t *testing.T) {
	alice := func(w [7]int) scores { return scores{"Alice", w} }
	bob := func(w [7]int) scores { return scores{"Bob", w} }
	good := [7]int{3, 3, 3, 3, 3, 3, 3}
	bad := [7]int{4, 4, 4, 4, 4, 4, 4}
	for _, tc := range []struct {
		name  string
		weeks [][]scores // oldest first, the recapped week last
		want  string
	}{
		{"a second week running",
			[][]scores{{alice(bad), bob(good)}, {alice(good), bob(bad)}, {alice(good), bob(bad)}},
			"🔁 Alice: 2 weeks in a row at the top."},
		{"a shared run",
			[][]scores{{alice(good), bob(good)}, {alice(good), bob(good)}},
			"🔁 Alice and Bob: 2 weeks in a row at the top."},
		{"the end of a long run",
			[][]scores{{alice(good), bob(bad)}, {alice(good), bob(bad)}, {alice(good), bob(bad)}, {alice(bad), bob(good)}},
			"🔁 Bob ends the run: Alice had won 3 weeks in a row."},
		{"the end of a two-week run is just a new winner",
			[][]scores{{alice(bad), bob(good)}, {alice(good), bob(bad)}, {alice(good), bob(bad)}, {alice(bad), bob(good)}},
			""},
		{"a single win",
			[][]scores{{alice(good), bob(bad)}},
			""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := announceDB(t)
			for i, week := range tc.weeks {
				seedWeek(t, db, testMonday-7*(len(tc.weeks)-1-i), week...)
			}
			if got := lineWith(t, weekPost(t, db, "en"), "🔁"); got != tc.want {
				t.Errorf("run = %q, want %q", got, tc.want)
			}
		})
	}
}

// The early bird is whoever usually posts first, by the median of their
// posting times: one late night does not change who that is.
func TestTheEarlyBirdIsTheEarliestUsualTime(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for i := range 7 {
		hour := 7
		if i == 3 {
			hour = 23
		}
		post(t, db, "alice", day(testMonday+i), 4, hour, 5)
		post(t, db, "bob", day(testMonday+i), 4, 9, 30)
	}
	if got, want := weekLine(t, db, earlyLine), "⏰ Early bird: Alice, usually in by 07:05."; got != want {
		t.Errorf("early = %q, want %q", got, want)
	}
}

// A player well under their usual average has the week's turnaround.
func TestTheMoverBeatsTheirOwnAverage(t *testing.T) {
	db := announceDB(t)
	for w := 3; w > 0; w-- {
		seedWeek(t, db, testMonday-7*w,
			scores{"Alice", [7]int{5, 5, 5, 5, 5, 5, 5}},
			scores{"Bob", [7]int{3, 3, 3, 3, 3, 3, 3}},
		)
	}
	seedWeek(t, db, testMonday,
		scores{"Alice", [7]int{3, 4, 4, 4, 4, 4, 4}},
		scores{"Bob", [7]int{3, 3, 3, 3, 3, 3, 3}},
	)
	if got, want := lineWith(t, weekPost(t, db, "en"), "🚀"),
		"🚀 A week to remember for Alice: 3.86 on average, against the usual 5.00."; got != want {
		t.Errorf("mover = %q, want %q", got, want)
	}
}

func TestTheWeeklyPostUsesTheConfiguredLocale(t *testing.T) {
	db := announceDB(t)
	theWeek(t, db)
	got := weekPost(t, db, "sv")
	for _, want := range []string{
		"🗓️ Vecka 38 är klar: 4 spelare, 28 resultat.",
		"🥇 Alice 3,00 · 🥈 Bob 3,43 · 🥉 Carol 4,14",
		"🥄 Dave är veckans jumbo på 5,00.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message =\n%s\nwant a line %q", got, want)
		}
	}
}

// Ties share a medal, and the next place skips the ones they used.
func TestAPodiumTieSharesTheMedal(t *testing.T) {
	db := announceDB(t)
	seedWeek(t, db, testMonday,
		scores{"Alice", [7]int{3, 3, 3, 3, 3, 3, 3}},
		scores{"Bob", [7]int{3, 3, 3, 3, 3, 3, 3}},
		scores{"Carol", [7]int{4, 4, 4, 4, 4, 4, 4}},
	)
	if got, want := lineWith(t, weekPost(t, db, "en"), "🥇"), "🥇 Alice and Bob 3.00 · 🥉 Carol 4.00"; got != want {
		t.Errorf("podium = %q, want %q", got, want)
	}
}

// weekPost runs the week's check just after it closes and returns the one
// message it sent.
func weekPost(t *testing.T, db *sql.DB, locale string) string {
	t.Helper()
	var c collector
	if err := weeklyCheck(t, db, locale, false, &c)(context.Background(), day(21).Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return c.only(t)
}

// The test week really is a Monday-to-Sunday week, so the fixtures above
// mean what they say.
func TestTheTestWeekStartsOnAMonday(t *testing.T) {
	if first := wordle.PuzzleForDate(day(testMonday)); stats.WeekOf(first) != first {
		t.Fatalf("14 September 2026 is not a week's Monday")
	}
}

// weekLine builds one line of the test week on its own, for a line the cap
// would otherwise leave out.
func weekLine(t *testing.T, db *sql.DB, line func(i18n.Translator, weekContext) string) string {
	t.Helper()
	ctx := context.Background()
	players, err := store.ListPlayers(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.ResultsForBoard(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	w, err := newWeekContext(players, results, wordle.PuzzleForDate(day(testMonday)))
	if err != nil {
		t.Fatal(err)
	}
	return line(i18n.NewTranslator(loadCatalogues(t), "en"), w)
}
