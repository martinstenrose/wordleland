package announce

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// record files one result on one date, solved or not, which fill cannot do:
// the day's recap has to say something about a day nobody cracked.
func record(t *testing.T, db *sql.DB, slug string, date time.Time, solved bool, guesses int) {
	t.Helper()
	sub := ingest.Submission{
		Slug: slug, PuzzleNo: wordle.PuzzleForDate(date), Solved: solved,
	}
	if solved {
		g := guesses
		sub.Guesses = &g
	}
	if _, err := ingest.Apply(context.Background(), db, store.SystemActor(), sub, false); err != nil {
		t.Fatalf("seed result for %s on %s: %v", slug, date, err)
	}
}

// alreadyPosted marks a day announced without sending anything, which is
// what steady state looks like: by the time today can go out early,
// yesterday's recap has long since gone. Tests that only care about today
// say so here rather than absorbing yesterday's catch-up post.
func alreadyPosted(t *testing.T, db *sql.DB, year int, month time.Month, day int) {
	t.Helper()
	puzzle := wordle.PuzzleForDate(time.Date(year, month, day, 0, 0, 0, 0, time.Local))
	if err := store.RecordDayAnnouncement(context.Background(), db, puzzle); err != nil {
		t.Fatalf("RecordDayAnnouncement(%d): %v", puzzle, err)
	}
}

func retire(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	ctx := context.Background()
	p, err := store.PlayerBySlug(ctx, db, slug)
	if err != nil {
		t.Fatalf("PlayerBySlug(%s): %v", slug, err)
	}
	inactive := false
	if _, err := store.UpdatePlayer(ctx, db, store.SystemActor(), p.ID,
		store.PlayerUpdate{Active: &inactive}); err != nil {
		t.Fatalf("retire %s: %v", slug, err)
	}
}

// collector is the send side of every test here: what went out, and how
// often.
type collector struct {
	mu    sync.Mutex
	sent  []string
	calls atomic.Int32
	fail  error
}

func (c *collector) send(_ context.Context, text string) error {
	c.calls.Add(1)
	if c.fail != nil {
		return c.fail
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, text)
	return nil
}

func (c *collector) only(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) != 1 {
		t.Fatalf("sent %d messages, want 1: %q", len(c.sent), c.sent)
	}
	return c.sent[0]
}

// The headline case: the last active player files, and the recap goes out on
// the spot rather than waiting for midnight. It names the day's best and
// where the month stands, with the margin over whoever is second.
func TestPostsAsSoonAsEveryActivePlayerHasFiled(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	// Two days of the month, so the month line has an average to report and
	// a gap between the two of them.
	fill(t, db, "alice", 2026, time.September, 1, 2, 2)
	fill(t, db, "bob", 2026, time.September, 1, 2, 4)
	alreadyPosted(t, db, 2026, time.September, 1)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	// Mid-afternoon on the second day: both have filed, nobody is missing.
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	puzzle := wordle.PuzzleForDate(time.Date(2026, time.September, 2, 0, 0, 0, 0, time.Local))
	want := "🏁 Wordle " + strconv.Itoa(puzzle) + " — everyone's in.\n" +
		"🥇 Alice took it in 2.\n" +
		"📊 September: Alice leads on 2.00 on average, 200 points clear of Bob."
	// Alice's two 2s average 2.00; Bob's two 4s average 4.00. Both played
	// every concluded day, so no sevens enter either average.
	if got := c.only(t); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}

	done, err := store.DayAnnounced(ctx, db, puzzle)
	if err != nil {
		t.Fatalf("DayAnnounced: %v", err)
	}
	if !done {
		t.Error("the day was not recorded as announced after a successful send")
	}

	// A second live message that same afternoon must not repeat it.
	if err := daily(ctx, now); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if c.calls.Load() != 1 {
		t.Errorf("send called %d times after a repeat call, want still 1", c.calls.Load())
	}
}

// One check posts one day, closed day first. Coming up with yesterday
// unannounced and today already complete, the first check catches yesterday
// up and the next posts today — rather than skipping yesterday to report the
// fresher day, or posting both in one message.
func TestTheClosedDayIsPostedBeforeToday(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.September, 1, 2, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	first := wordle.PuzzleForDate(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.Local))
	second := wordle.PuzzleForDate(time.Date(2026, time.September, 2, 0, 0, 0, 0, time.Local))

	if err := daily(ctx, now); err != nil {
		t.Fatalf("first check: %v", err)
	}
	if got := c.only(t); !strings.Contains(got, "Wordle "+strconv.Itoa(first)) {
		t.Fatalf("first message = %q, want the closed day %d", got, first)
	}

	if err := daily(ctx, now); err != nil {
		t.Fatalf("second check: %v", err)
	}
	c.mu.Lock()
	sent := append([]string(nil), c.sent...)
	c.mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent %d messages across two checks, want 2: %q", len(sent), sent)
	}
	if !strings.Contains(sent[1], "Wordle "+strconv.Itoa(second)) {
		t.Errorf("second message = %q, want today's puzzle %d", sent[1], second)
	}

	// And a third finds nothing left: both days are recorded.
	if err := daily(ctx, now); err != nil {
		t.Fatalf("third check: %v", err)
	}
	if c.calls.Load() != 2 {
		t.Errorf("send called %d times across three checks, want 2", c.calls.Load())
	}
}

// The deployment case: the feature goes live at 12:18 on a day the group is
// part-way through, with a database full of played days. The bot's first word
// must be about today's puzzle, not a recap of yesterday.
func TestTheFirstRunOpensWithTodayNotYesterday(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	// A week of history, every day played, none of it ever announced —
	// exactly what the table looks like the moment this ships.
	fill(t, db, "alice", 2026, time.September, 20, 7, 3)
	fill(t, db, "bob", 2026, time.September, 20, 7, 4)
	// Today, 27 September: Alice has filed, Bob has not.
	fill(t, db, "alice", 2026, time.September, 27, 1, 2)

	boot := time.Date(2026, time.September, 27, 12, 18, 0, 0, time.Local)
	if err := SkipDailyBacklog(ctx, db, boot); err != nil {
		t.Fatalf("SkipDailyBacklog: %v", err)
	}

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)

	// The check on start finds nothing: yesterday is marked done, and today
	// is still waiting for Bob.
	if err := daily(ctx, boot); err != nil {
		t.Fatalf("start check: %v", err)
	}
	if c.calls.Load() != 0 {
		c.mu.Lock()
		defer c.mu.Unlock()
		t.Fatalf("the first run posted %d message(s), want silence until today is done: %q",
			c.calls.Load(), c.sent)
	}

	// Bob files that afternoon, and the first thing the group ever hears is
	// today's recap.
	fill(t, db, "bob", 2026, time.September, 27, 1, 5)
	if err := daily(ctx, time.Date(2026, time.September, 27, 16, 0, 0, 0, time.Local)); err != nil {
		t.Fatalf("after Bob files: %v", err)
	}
	today := wordle.PuzzleForDate(time.Date(2026, time.September, 27, 0, 0, 0, 0, time.Local))
	if got := c.only(t); !strings.Contains(got, "Wordle "+strconv.Itoa(today)) {
		t.Errorf("first message = %q, want today's puzzle %d", got, today)
	}
}

// The marker is set once. A later restart must not move it forward and swallow
// a day that genuinely has not been posted.
func TestSkipDailyBacklogOnlyEverRunsOnce(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	first := time.Date(2026, time.September, 27, 12, 18, 0, 0, time.Local)
	if err := SkipDailyBacklog(ctx, db, first); err != nil {
		t.Fatalf("first: %v", err)
	}
	marked := wordle.PuzzleForDate(first) - 1

	// A restart two days later must leave the marker where it is.
	later := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.Local)
	if err := SkipDailyBacklog(ctx, db, later); err != nil {
		t.Fatalf("second: %v", err)
	}
	if done, _ := store.DayAnnounced(ctx, db, wordle.PuzzleForDate(later)-1); done {
		t.Error("a restart marked a second day done; the backlog skip must happen once")
	}
	if done, _ := store.DayAnnounced(ctx, db, marked); !done {
		t.Error("the original marker was lost")
	}
}

// Once the feature has posted at all, a boot that finds an unannounced day one
// back is a genuinely missed midnight and must be caught up.
func TestABootAfterAMissedMidnightPostsTheMissedDay(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 25, 2, 3)
	fill(t, db, "bob", 2026, time.September, 25, 2, 4)
	// The 25th went out; the 26th did not, because the app was down at 00:01.
	alreadyPosted(t, db, 2026, time.September, 25)

	boot := time.Date(2026, time.September, 27, 8, 0, 0, 0, time.Local)
	// Not a first run: a day is already announced, so nothing is skipped.
	if err := SkipDailyBacklog(ctx, db, boot); err != nil {
		t.Fatalf("SkipDailyBacklog: %v", err)
	}

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	if err := daily(ctx, boot); err != nil {
		t.Fatalf("start check: %v", err)
	}
	missed := wordle.PuzzleForDate(time.Date(2026, time.September, 26, 0, 0, 0, 0, time.Local))
	if got := c.only(t); !strings.Contains(got, "Wordle "+strconv.Itoa(missed)) {
		t.Errorf("message = %q, want the missed day %d caught up at boot", got, missed)
	}
}

// While anybody active is still out, the day is not over and nothing is
// posted: that is the whole point of the "or midnight" half of the rule.
func TestSaysNothingWhileAnActivePlayerIsStillMissing(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if c.calls.Load() != 0 {
		t.Errorf("send called %d times with Bob still out, want 0", c.calls.Load())
	}
}

// A retired player is not expected, so they must not hold the day open for
// ever. Without this the recap would never fire early again after anybody
// left the group.
func TestARetiredPlayerDoesNotHoldTheDayOpen(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	retire(t, db, "bob")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if got := c.only(t); !strings.Contains(got, "everyone's in") {
		t.Errorf("message = %q, want the full-house headline", got)
	}
}

// Midnight is the backstop. The day that just closed is posted with the
// count of who actually turned up, and the absentees are counted rather
// than named.
func TestMidnightPostsTheClosedDayWithTheCount(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	mustPlayer(t, db, "Carol", "carol")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)
	fill(t, db, "bob", 2026, time.September, 2, 1, 5)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	// The run just after midnight, so "now" is already the next day.
	now := time.Date(2026, time.September, 3, 0, 1, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	puzzle := wordle.PuzzleForDate(time.Date(2026, time.September, 2, 0, 0, 0, 0, time.Local))
	got := c.only(t)
	wantHead := "🏁 Wordle " + strconv.Itoa(puzzle) + " — that's the day. 2 of 3 posted."
	if !strings.HasPrefix(got, wantHead) {
		t.Errorf("message =\n%q\nwant it to start with\n%q", got, wantHead)
	}
	if !strings.Contains(got, "🥇 Alice took it in 3.") {
		t.Errorf("message = %q, want Alice named as the day's best", got)
	}
	if strings.Contains(got, "Carol") {
		t.Errorf("message = %q names the absent player; absentees are counted, not named", got)
	}
}

// A day nobody solved is still a day, and saying so is better than silence
// or than crowning the least-bad failure.
func TestSaysNobodySolvedItWhenEveryoneFailed(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	date := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.Local)
	record(t, db, "alice", date, false, 0)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if got := c.only(t); !strings.Contains(got, "🥇 Nobody solved it.") {
		t.Errorf("message = %q, want the nobody-solved line", got)
	}
}

// A shared best is the day's result. Naming one of them would pick a winner
// the day does not have.
func TestATiedBestNamesEveryone(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)
	fill(t, db, "bob", 2026, time.September, 2, 1, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if got := c.only(t); !strings.Contains(got, "🥇 Alice and Bob both took it in 3.") {
		t.Errorf("message = %q, want both names on the day's best", got)
	}
}

// Three is where "both" would be wrong, which is why the pair has a sentence
// of its own rather than the catalogue's singular/plural pair.
func TestAThreeWayTiedBestSaysAll(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	mustPlayer(t, db, "Carol", "carol")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)
	fill(t, db, "bob", 2026, time.September, 2, 1, 3)
	fill(t, db, "carol", 2026, time.September, 2, 1, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	got := c.only(t)
	if !strings.Contains(got, "🥇 Alice, Bob and Carol all took it in 3.") {
		t.Errorf("message = %q, want all three names and \"all\"", got)
	}
	if strings.Contains(got, "both") {
		t.Errorf("message = %q says \"both\" of three people", got)
	}
}

// The recap that runs just after midnight on the first belongs to the month
// that just ended, not the one "now" is in. Reading the month off "now"
// would report a brand-new, empty October beside a day played in September.
//
// Driven with no monthly announcement configured, so the standing itself is
// printed and the month it names can be read off it.
func TestTheMonthLineFollowsTheDayNotTheClock(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.September, 1, 30, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", false, c.send)
	now := time.Date(2026, time.October, 1, 0, 1, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	got := c.only(t)
	if !strings.Contains(got, "📊 September:") {
		t.Errorf("message = %q, want the month line to name September", got)
	}
	if strings.Contains(got, "October") {
		t.Errorf("message = %q names October, the month the clock is in rather than the day's", got)
	}
}

// The 00:01 run on the first would otherwise print the closed month's result
// twelve hours before the 🏆 message exists to deliver it. It points at noon
// instead.
func TestAClosedMonthLeavesTheResultToTheTrophyMessage(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 1, 30, 3)
	fill(t, db, "bob", 2026, time.September, 1, 30, 4)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.October, 1, 0, 1, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	got := c.only(t)
	if !strings.Contains(got, "📊 September wrapped — the result at noon.") {
		t.Errorf("message = %q, want the month line to defer to noon", got)
	}
	// The giveaway is the winner's name or their average appearing on the
	// month line; the day's own lines may legitimately name whoever won it.
	if strings.Contains(got, "leads on") || strings.Contains(got, "clear of") {
		t.Errorf("message = %q gives away the month's result", got)
	}
}

// With no monthly announcement configured, nothing else will ever say where
// the month finished, so the recap says it rather than pointing at a message
// that never comes.
func TestAClosedMonthStillReportsWhenNoTrophyMessageFollows(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 1, 30, 3)
	fill(t, db, "bob", 2026, time.September, 1, 30, 4)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", false, c.send)
	now := time.Date(2026, time.October, 1, 0, 1, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if got := c.only(t); !strings.Contains(got, "📊 September: Alice leads on 3.00 on average, 100 points clear of Bob.") {
		t.Errorf("message = %q, want the standing printed in full", got)
	}
}

// The other way a last day gets recapped: everybody files on the 30th and it
// goes out that evening, hours before the month technically closes. By then
// nobody is left to change the figures, so this gives the result away just as
// surely as the 00:01 run does and must defer to noon too.
func TestTheLastDayPostedEarlyAlsoDefersToNoon(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 1, 30, 3)
	fill(t, db, "bob", 2026, time.September, 1, 30, 4)
	alreadyPosted(t, db, 2026, time.September, 29)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	// The evening of 30 September, everybody in.
	now := time.Date(2026, time.September, 30, 20, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	got := c.only(t)
	if !strings.Contains(got, "📊 September wrapped — the result at noon.") {
		t.Errorf("message = %q, want the month line to defer to noon", got)
	}
	if strings.Contains(got, "leads on") || strings.Contains(got, "clear of") {
		t.Errorf("message = %q gives away the month's result", got)
	}
}

// An ordinary day inside the month is unaffected — the standing is a
// standing, and the group is told it every day.
func TestAnOrdinaryDayStillShowsTheStanding(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 1, 15, 3)
	fill(t, db, "bob", 2026, time.September, 1, 15, 4)
	alreadyPosted(t, db, 2026, time.September, 14)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 15, 20, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if got := c.only(t); !strings.Contains(got, "📊 September: Alice leads on 3.00 on average, 100 points clear of Bob.") {
		t.Errorf("message = %q, want the standing on an ordinary day", got)
	}
}

// February is the month a hand-rolled length table gets wrong, leap years
// doubly so.
func TestTheLastDayRuleHandlesFebruary(t *testing.T) {
	for _, tt := range []struct {
		date time.Time
		want bool
	}{
		{time.Date(2026, time.February, 28, 0, 0, 0, 0, time.Local), true},
		{time.Date(2026, time.February, 27, 0, 0, 0, 0, time.Local), false},
		// 2028 is a leap year, so the 28th is no longer the last day.
		{time.Date(2028, time.February, 28, 0, 0, 0, 0, time.Local), false},
		{time.Date(2028, time.February, 29, 0, 0, 0, 0, time.Local), true},
		{time.Date(2026, time.December, 31, 0, 0, 0, 0, time.Local), true},
	} {
		if got := lastDayOfMonth(tt.date); got != tt.want {
			t.Errorf("lastDayOfMonth(%s) = %v, want %v", tt.date.Format("2006-01-02"), got, tt.want)
		}
	}
}

// Only ever one day back. An app that was down for a week must not come back
// and post a burst of recaps into the group.
func TestOnlyTheDayJustClosedIsCaughtUp(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.September, 1, 5, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	// A live message on the 6th, having missed the 1st through the 4th.
	now := time.Date(2026, time.September, 6, 9, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if c.calls.Load() != 1 {
		t.Fatalf("send called %d times catching up four missed days, want 1", c.calls.Load())
	}
	fifth := wordle.PuzzleForDate(time.Date(2026, time.September, 5, 0, 0, 0, 0, time.Local))
	if got := c.only(t); !strings.Contains(got, "Wordle "+strconv.Itoa(fifth)) {
		t.Errorf("message = %q, want only the 5th — the day that just closed", got)
	}
	for _, day := range []int{1, 2, 3, 4} {
		puzzle := wordle.PuzzleForDate(time.Date(2026, time.September, day, 0, 0, 0, 0, time.Local))
		done, err := store.DayAnnounced(ctx, db, puzzle)
		if err != nil {
			t.Fatalf("DayAnnounced(%d): %v", puzzle, err)
		}
		if done {
			t.Errorf("September %d was recorded as announced; only the day just closed should be", day)
		}
	}
}

// A day on which nobody posted anything has nothing to report, and is left
// unrecorded so a late result still gets its recap.
func TestSaysNothingForADayNobodyPlayed(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if c.calls.Load() != 0 {
		t.Errorf("send called %d times for a day nobody played, want 0", c.calls.Load())
	}
}

// A failed send must leave the day unrecorded, so the next live message
// tries again rather than the day going quietly unannounced.
func TestFailedDailySendIsNotRecordedAndIsRetried(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)

	c := collector{fail: errors.New("signal is unreachable")}
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)
	puzzle := wordle.PuzzleForDate(time.Date(2026, time.September, 2, 0, 0, 0, 0, time.Local))

	if err := daily(ctx, now); err == nil {
		t.Fatal("daily() succeeded despite the send failing")
	}
	if done, _ := store.DayAnnounced(ctx, db, puzzle); done {
		t.Fatal("a failed send was recorded as announced")
	}

	c.fail = nil
	if err := daily(ctx, now); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if c.calls.Load() != 2 {
		t.Errorf("send called %d times across the failure and the retry, want 2", c.calls.Load())
	}
	if done, _ := store.DayAnnounced(ctx, db, puzzle); !done {
		t.Error("the retry's successful send was not recorded")
	}
}

// The midnight run and the last player's live result can land together —
// somebody filing at 23:59:59 is exactly when both fire — so the whole
// check/send/record sequence has to be serialized.
func TestConcurrentDailyChecksSendOnce(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- daily(ctx, now)
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

// A shared second place is not one person, so the margin names all of them.
func TestTheMarginNamesEveryRunnerUp(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	mustPlayer(t, db, "Carol", "carol")
	fill(t, db, "alice", 2026, time.September, 1, 2, 2)
	fill(t, db, "bob", 2026, time.September, 1, 2, 4)
	fill(t, db, "carol", 2026, time.September, 1, 2, 4)
	alreadyPosted(t, db, 2026, time.September, 1)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	if got := c.only(t); !strings.Contains(got, "200 points clear of Bob and Carol.") {
		t.Errorf("message = %q, want both runners-up named", got)
	}
}

// The configured locale carries the whole message, decimal comma included.
func TestTheDailyPostUsesTheConfiguredLocale(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.September, 1, 2, 2)
	fill(t, db, "bob", 2026, time.September, 1, 2, 4)
	alreadyPosted(t, db, 2026, time.September, 1)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "sv", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	puzzle := wordle.PuzzleForDate(time.Date(2026, time.September, 2, 0, 0, 0, 0, time.Local))
	want := "🏁 Wordle " + strconv.Itoa(puzzle) + ": alla har lämnat in.\n" +
		"🥇 Alice klarade den på 2.\n" +
		"📊 September: Alice leder på 2,00 i snitt, 200 punkter före Bob."
	if got := c.only(t); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
}

// A puzzle number names a day; it is not a quantity, so it is never grouped
// — the same rule the board follows, and the reason it goes through %s.
func TestThePuzzleNumberIsNeverGrouped(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.September, 2, 1, 3)

	var c collector
	daily := NewDaily(db, loadCatalogues(t), "sv", true, c.send)
	now := time.Date(2026, time.September, 2, 15, 0, 0, 0, time.Local)

	if err := daily(ctx, now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	puzzle := wordle.PuzzleForDate(time.Date(2026, time.September, 2, 0, 0, 0, 0, time.Local))
	if puzzle < 1000 {
		t.Fatalf("puzzle %d is too small to show grouping either way", puzzle)
	}
	if got := c.only(t); !strings.Contains(got, "Wordle "+strconv.Itoa(puzzle)) {
		t.Errorf("message = %q, want the bare digits %d under a grouping locale", got, puzzle)
	}
}
