package announce

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// post files one result the way the bridge does: with the time it was
// posted in the group. guesses of 0 is a failure.
func post(t *testing.T, db *sql.DB, slug string, date time.Time, guesses, hour, minute int) {
	t.Helper()
	at := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, time.Local)
	sub := ingest.Submission{
		Slug: slug, PuzzleNo: wordle.PuzzleForDate(date), Solved: guesses > 0, PostedAt: &at,
	}
	if guesses > 0 {
		g := guesses
		sub.Guesses = &g
	}
	if _, err := ingest.Apply(context.Background(), db, store.SystemActor(), sub, false); err != nil {
		t.Fatalf("post result for %s on %s: %v", slug, date, err)
	}
}

func day(d int) time.Time { return time.Date(2026, time.September, d, 0, 0, 0, 0, time.Local) }

// recap runs the daily check at now and returns the one message it sent.
func recap(t *testing.T, db *sql.DB, now time.Time, monthResultFollows bool) string {
	t.Helper()
	var c collector
	daily := NewDaily(db, loadCatalogues(t), "en", monthResultFollows, c.send)
	if err := daily(context.Background(), now); err != nil {
		t.Fatalf("daily: %v", err)
	}
	return c.only(t)
}

func lineWith(t *testing.T, got, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// Everyone on the same score is a fact about the puzzle, said once.
func TestEveryoneOnTheSameScoreIsSaidAsOne(t *testing.T) {
	db := announceDB(t)
	for _, p := range []string{"Alice", "Bob", "Carol"} {
		mustPlayer(t, db, p, strings.ToLower(p))
		record(t, db, strings.ToLower(p), day(2), true, 3)
	}

	got := recap(t, db, day(2).Add(15*time.Hour), true)
	if want := "🥇 Everyone took it in 3."; lineWith(t, got, "🥇") != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// From four sharing the best, the count replaces the list: seven names in a
// row are not read.
func TestACrowdOnTheBestIsCounted(t *testing.T) {
	db := announceDB(t)
	for _, p := range []string{"Alice", "Bob", "Carol", "Dana", "Erik"} {
		mustPlayer(t, db, p, strings.ToLower(p))
		record(t, db, strings.ToLower(p), day(2), true, 3)
	}
	record(t, db, "erik", day(2), true, 4)

	got := recap(t, db, day(2).Add(15*time.Hour), true)
	if want := "🥇 4 of 5 took it in 3."; lineWith(t, got, "🥇") != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// A first-guess solve is the one score the group will ask about.
func TestAFirstGuessSolveIsAnAce(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	record(t, db, "alice", day(2), true, 1)
	record(t, db, "bob", day(2), true, 4)

	got := recap(t, db, day(2).Add(15*time.Hour), true)
	if want := "🥇 Alice: first guess!"; lineWith(t, got, "🥇") != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// Who opened the day and who closed it, from the posting times. A result
// without one — filed by hand — is neither, and with fewer than two times
// there is no order to report.
func TestSaysWhoOpenedAndClosedTheDay(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	mustPlayer(t, db, "Carol", "carol")
	post(t, db, "alice", day(2), 4, 6, 12)
	post(t, db, "bob", day(2), 3, 23, 41)
	record(t, db, "carol", day(2), true, 5)

	got := recap(t, db, day(3).Add(time.Minute), true)
	if want := "⏰ Alice opened the day at 06:12. Bob closed it at 23:41."; lineWith(t, got, "⏰") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	db = announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	post(t, db, "alice", day(2), 4, 6, 12)
	record(t, db, "bob", day(2), true, 3)

	got = recap(t, db, day(2).Add(15*time.Hour), true)
	if strings.Contains(got, "⏰") {
		t.Errorf("message = %q has an opening line with only one posting time", got)
	}
}

// "As usual" needs a habit: half the window's days, and enough days for
// half to mean something. Yesterday's swap keeps this from being a run.
func TestOpeningAndClosingAsUsual(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 1; d <= 10; d++ {
		post(t, db, "alice", day(d), 3, 6, 0)
		post(t, db, "bob", day(d), 4, 9, 0)
	}
	post(t, db, "bob", day(11), 4, 6, 0)
	post(t, db, "alice", day(11), 3, 9, 0)
	post(t, db, "alice", day(12), 3, 6, 12)
	post(t, db, "bob", day(12), 4, 23, 41)
	alreadyPosted(t, db, 2026, time.September, 11)

	got := recap(t, db, day(12).Add(23*time.Hour+50*time.Minute), true)
	want := "⏰ Alice opened the day at 06:12, as usual. Bob closed it at 23:41, as usual."
	if lineWith(t, got, "⏰") != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// A run is counted out loud from three days, and is the more specific claim
// when both would hold. Five days is too few for "as usual" anyway.
func TestOpeningAndClosingDaysRunning(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 1; d <= 5; d++ {
		post(t, db, "alice", day(d), 3, 6, 0)
		post(t, db, "bob", day(d), 4, 9, 0)
	}
	alreadyPosted(t, db, 2026, time.September, 4)

	got := recap(t, db, day(5).Add(15*time.Hour), true)
	want := "⏰ Alice opened the day at 06:00, 5 days running. Bob closed it at 09:00, 5 days running."
	if lineWith(t, got, "⏰") != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// The day handed the lead to Bob with his first ever 2, and Alice failed.
// Both events are told — each is rare, and dropping one for the other
// would lose news — and the failure is the day's one remark beside them.
func TestEveryEventIsToldAndOneRemark(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 1; d <= 4; d++ {
		record(t, db, "alice", day(d), true, 3)
		record(t, db, "bob", day(d), true, 4)
	}
	post(t, db, "alice", day(5), 0, 7, 0)
	post(t, db, "bob", day(5), 2, 12, 0)
	alreadyPosted(t, db, 2026, time.September, 4)

	got := recap(t, db, day(5).Add(15*time.Hour), true)
	want := strings.Join([]string{
		"🏁 Wordle " + strconv.Itoa(wordle.PuzzleForDate(day(5))) + " — everyone's in.",
		"🥇 Bob took it in 2.",
		"⏰ Alice opened the day at 07:00. Bob closed it at 12:00.",
		"👑 New leader in September: Bob takes over from Alice.",
		"🎉 Bob in 2, for the first time!",
		"💀 Alice didn't get it.",
		"📊 September: Bob leads on 3.60 on average, 20 points clear of Alice.",
	}, "\n")
	if got != want {
		t.Errorf("message =\n%s\nwant\n%s", got, want)
	}
}

// The frequent lines are still one a day: a hard day with a failure in it
// explains the day rather than also naming who it beat.
func TestOnlyOneRemarkADay(t *testing.T) {
	db := announceDB(t)
	for _, p := range []string{"Alice", "Bob", "Carol"} {
		mustPlayer(t, db, p, strings.ToLower(p))
		for d := 1; d <= 11; d++ {
			record(t, db, strings.ToLower(p), day(d), true, 3)
		}
	}
	alreadyPosted(t, db, 2026, time.September, 11)
	record(t, db, "alice", day(12), true, 6)
	record(t, db, "bob", day(12), true, 6)
	record(t, db, "carol", day(12), false, 0)

	got := recap(t, db, day(12).Add(15*time.Hour), true)
	if lineWith(t, got, "🧱") == "" {
		t.Errorf("message = %q, want the hard day called", got)
	}
	if strings.Contains(got, "💀") {
		t.Errorf("message = %q names the failure beside the hard day; one remark a day", got)
	}
}

// In a month's first days the lead changes with every result, so it is not
// news. The same swap on the third is passed over — and with nothing to
// outrank it, the failure is what gets said.
func TestNoChangeOfLeaderInTheFirstDays(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 1; d <= 2; d++ {
		record(t, db, "alice", day(d), true, 3)
		record(t, db, "bob", day(d), true, 4)
	}
	record(t, db, "alice", day(3), false, 0)
	record(t, db, "bob", day(3), true, 3)
	alreadyPosted(t, db, 2026, time.September, 2)

	got := recap(t, db, day(3).Add(15*time.Hour), true)
	if strings.Contains(got, "👑") {
		t.Errorf("message = %q announces a leader on the 3rd", got)
	}
	if want := "💀 Alice didn't get it."; lineWith(t, got, "💀") != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// On the month's last day the standing is withheld for the 🏆 message, and
// so is who took the lead on it.
func TestNoChangeOfLeaderOnAWrappedLastDay(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 26; d <= 29; d++ {
		record(t, db, "alice", day(d), true, 3)
		record(t, db, "bob", day(d), true, 4)
	}
	record(t, db, "alice", day(30), false, 0)
	record(t, db, "bob", day(30), true, 2)
	alreadyPosted(t, db, 2026, time.September, 29)

	got := recap(t, db, day(30).Add(15*time.Hour), true)
	if strings.Contains(got, "👑") {
		t.Errorf("message = %q gives the month away on its last day", got)
	}
	if strings.Contains(got, "📊") {
		t.Errorf("message = %q, want the standing withheld", got)
	}
}

// A solved streak arriving at a milestone is remarked on with the board's
// own number. A player who sits the next day out still shows a streak of
// ten on the board, and that day's recap must not repeat it.
func TestAStreakMilestone(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 1; d <= 10; d++ {
		record(t, db, "alice", day(d), true, 3)
		record(t, db, "bob", day(d), true, 4)
	}
	alreadyPosted(t, db, 2026, time.September, 9)

	got := recap(t, db, day(10).Add(15*time.Hour), true)
	if want := "🔥 Alice: 10 days in a row without a miss."; lineWith(t, got, "🔥") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	record(t, db, "bob", day(11), true, 4)
	got = recap(t, db, day(12).Add(time.Minute), true)
	if strings.Contains(got, "🔥") {
		t.Errorf("message = %q credits a milestone to a player who sat the day out", got)
	}
}

// The 00:01 run recaps yesterday, by which time somebody may have posted
// today. Bob's 6 for the 6th would cost him the lead he took on the 5th if
// it were let into the 5th's figures.
func TestTheNextDaysResultDoesNotLeakIntoTheRecap(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 1; d <= 4; d++ {
		record(t, db, "alice", day(d), true, 3)
		record(t, db, "bob", day(d), true, 4)
	}
	record(t, db, "alice", day(5), false, 0)
	record(t, db, "bob", day(5), true, 2)
	record(t, db, "bob", day(6), true, 6)
	alreadyPosted(t, db, 2026, time.September, 4)

	got := recap(t, db, day(6).Add(time.Minute), true)
	if want := "👑 New leader in September: Bob takes over from Alice."; lineWith(t, got, "👑") != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// A puzzle is called hard or easy against what the group usually scores,
// once there is enough history to have a "usually".
func TestAHardDayAndAnEasyOne(t *testing.T) {
	seed := func(t *testing.T, usual int) *sql.DB {
		db := announceDB(t)
		for _, p := range []string{"Alice", "Bob", "Carol"} {
			mustPlayer(t, db, p, strings.ToLower(p))
			for d := 1; d <= 11; d++ {
				record(t, db, strings.ToLower(p), day(d), true, usual)
			}
		}
		alreadyPosted(t, db, 2026, time.September, 11)
		return db
	}

	db := seed(t, 3)
	for _, p := range []string{"alice", "bob", "carol"} {
		record(t, db, p, day(12), true, 6)
	}
	got := recap(t, db, day(12).Add(15*time.Hour), true)
	if want := "🧱 A hard one: 6.0 on average today, against the usual 3.0."; lineWith(t, got, "🧱") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	db = seed(t, 5)
	for _, p := range []string{"alice", "bob", "carol"} {
		record(t, db, p, day(12), true, 3)
	}
	got = recap(t, db, day(12).Add(15*time.Hour), true)
	if want := "🪶 An easy one: 3.0 on average today, against the usual 5.0."; lineWith(t, got, "🪶") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	// Half a guess off is an ordinary day.
	db = seed(t, 4)
	record(t, db, "alice", day(12), true, 4)
	record(t, db, "bob", day(12), true, 4)
	record(t, db, "carol", day(12), true, 5)
	got = recap(t, db, day(12).Add(15*time.Hour), true)
	if strings.Contains(got, "🧱") || strings.Contains(got, "🪶") {
		t.Errorf("message = %q calls an ordinary day hard or easy", got)
	}

	// Ten days of history is not a "usually" yet.
	db = announceDB(t)
	for _, p := range []string{"Alice", "Bob", "Carol"} {
		mustPlayer(t, db, p, strings.ToLower(p))
		for d := 1; d <= 3; d++ {
			record(t, db, strings.ToLower(p), day(d), true, 3)
		}
		record(t, db, strings.ToLower(p), day(4), true, 6)
	}
	alreadyPosted(t, db, 2026, time.September, 3)
	got = recap(t, db, day(4).Add(15*time.Hour), true)
	if strings.Contains(got, "🧱") {
		t.Errorf("message = %q compares the day with a history too short to have a usual", got)
	}
}

// Failures are named — they are results the players posted themselves —
// unless nobody solved it, which the 🥇 line has already said.
func TestFailuresAreNamed(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	mustPlayer(t, db, "Carol", "carol")
	record(t, db, "alice", day(2), true, 3)
	record(t, db, "bob", day(2), false, 0)
	record(t, db, "carol", day(2), false, 0)

	got := recap(t, db, day(2).Add(15*time.Hour), true)
	if want := "💀 Bob and Carol didn't get it."; lineWith(t, got, "💀") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	db = announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	record(t, db, "alice", day(2), false, 0)
	record(t, db, "bob", day(2), false, 0)
	got = recap(t, db, day(2).Add(15*time.Hour), true)
	if strings.Contains(got, "💀") {
		t.Errorf("message = %q names the failures under \"Nobody solved it\"", got)
	}
}

// The day's surprise is whoever landed furthest under their own average
// going into the day — not the day's best, whose line it already is, and
// not by less than the margin.
func TestTheDaysSurprise(t *testing.T) {
	seed := func(t *testing.T, aliceToday int) string {
		db := announceDB(t)
		mustPlayer(t, db, "Alice", "alice")
		mustPlayer(t, db, "Bob", "bob")
		for d := 1; d <= 10; d++ {
			record(t, db, "alice", day(d), true, 5)
			record(t, db, "bob", day(d), true, 3)
		}
		record(t, db, "bob", day(1), true, 2) // a 2 already, so today's is no first
		record(t, db, "alice", day(11), true, aliceToday)
		record(t, db, "bob", day(11), true, 2)
		alreadyPosted(t, db, 2026, time.September, 10)
		return recap(t, db, day(11).Add(15*time.Hour), true)
	}

	got := seed(t, 3)
	if want := "📈 Today's surprise: Alice in 3, against an average of 5.0."; lineWith(t, got, "📈") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	if got := seed(t, 4); strings.Contains(got, "📈") {
		t.Errorf("message = %q calls one guess under average a surprise", got)
	}
}

// A first ever 2 is celebrated however short the history — but not on a
// first day, when "first ever" says nothing, and not when an earlier 2 or 1
// exists.
func TestAFirstEverTwo(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d := 1; d <= 2; d++ {
		record(t, db, "alice", day(d), true, 4)
		record(t, db, "bob", day(d), true, 4)
	}
	record(t, db, "alice", day(3), true, 2)
	record(t, db, "bob", day(3), true, 4)
	alreadyPosted(t, db, 2026, time.September, 2)

	got := recap(t, db, day(3).Add(15*time.Hour), true)
	if want := "🎉 Alice in 2, for the first time!"; lineWith(t, got, "🎉") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	// Alice's second 2, three days later: nothing.
	record(t, db, "alice", day(4), true, 2)
	record(t, db, "bob", day(4), true, 4)
	got = recap(t, db, day(4).Add(15*time.Hour), true)
	if strings.Contains(got, "🎉") {
		t.Errorf("message = %q celebrates a second 2 as a first", got)
	}

	// Carol's first day is a 2. Impressive, but not a first ever.
	db = announceDB(t)
	mustPlayer(t, db, "Carol", "carol")
	mustPlayer(t, db, "Dana", "dana")
	record(t, db, "carol", day(3), true, 2)
	record(t, db, "dana", day(3), true, 4)
	got = recap(t, db, day(3).Add(15*time.Hour), true)
	if strings.Contains(got, "🎉") {
		t.Errorf("message = %q calls a first day's 2 a first ever", got)
	}
}

// A first ever first-guess solve gets the ace line's own variant.
func TestAFirstEverAce(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	record(t, db, "alice", day(1), true, 4)
	record(t, db, "bob", day(1), true, 4)
	record(t, db, "alice", day(2), true, 1)
	record(t, db, "bob", day(2), true, 4)
	alreadyPosted(t, db, 2026, time.September, 1)

	got := recap(t, db, day(2).Add(15*time.Hour), true)
	if want := "🥇 Alice: first guess, for the first time!"; lineWith(t, got, "🥇") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	record(t, db, "alice", day(3), true, 1)
	record(t, db, "bob", day(3), true, 4)
	got = recap(t, db, day(3).Add(15*time.Hour), true)
	if want := "🥇 Alice: first guess!"; lineWith(t, got, "🥇") != want {
		t.Errorf("message = %q, want the plain ace %q for a second one", got, want)
	}
}

// A run at 3 or better is remarked on the day it draws level with the
// group's record and the day it passes it, then not again. Bob's four
// threes are the record; Alice's 2 extends her run rather than breaking it.
// The failures keep both solved streaks short of a milestone, which would
// outrank this line.
func TestARunAtThreeOrBetterUpToTheRecord(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	bob := []int{3, 3, 3, 3, 0, 5, 5, 5, 5, 5, 5, 5}
	alice := []int{4, 4, 0, 4, 4, 4, 3, 2, 3, 3, 3, 3}
	for d := 1; d <= 9; d++ {
		record(t, db, "alice", day(d), alice[d-1] > 0, alice[d-1])
		record(t, db, "bob", day(d), bob[d-1] > 0, bob[d-1])
	}
	alreadyPosted(t, db, 2026, time.September, 9)

	for _, tc := range []struct {
		d    int
		want string
	}{
		{10, "🔁 Alice: 4 days in a row at 3 or better, now sharing the record with Bob."},
		{11, "🔁 Alice: 5 days in a row at 3 or better, a new group record."},
		{12, ""},
	} {
		record(t, db, "alice", day(tc.d), true, alice[tc.d-1])
		record(t, db, "bob", day(tc.d), true, bob[tc.d-1])
		got := recap(t, db, day(tc.d).Add(15*time.Hour), true)
		if lineWith(t, got, "🔁") != tc.want {
			t.Errorf("day %d: message = %q, want record line %q", tc.d, got, tc.want)
		}
	}
}

// Drawing level with a record one holds oneself is said as such; and a
// record of two is not a record yet.
func TestARunUpToOnesOwnRecord(t *testing.T) {
	db := announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	alice := []int{3, 3, 3, 3, 5, 3, 3, 3, 3}
	for d := 1; d <= 8; d++ {
		record(t, db, "alice", day(d), true, alice[d-1])
		record(t, db, "bob", day(d), true, 5)
	}
	alreadyPosted(t, db, 2026, time.September, 8)
	record(t, db, "alice", day(9), true, 3)
	record(t, db, "bob", day(9), true, 5)

	got := recap(t, db, day(9).Add(15*time.Hour), true)
	if want := "🔁 Alice: 4 days in a row at 3 or better, back up to their own record."; lineWith(t, got, "🔁") != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	db = announceDB(t)
	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	for d, g := range []int{3, 3, 5, 3} {
		record(t, db, "alice", day(d+1), true, g)
		record(t, db, "bob", day(d+1), true, 5)
	}
	alreadyPosted(t, db, 2026, time.September, 4)
	record(t, db, "alice", day(5), true, 3)
	record(t, db, "bob", day(5), true, 5)
	got = recap(t, db, day(5).Add(15*time.Hour), true)
	if strings.Contains(got, "🔁") {
		t.Errorf("message = %q calls a run of two a record", got)
	}
}
