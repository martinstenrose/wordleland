package announce

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/stats"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

func wordlePuzzleForDate(t *testing.T, date time.Time) int {
	t.Helper()
	return wordle.PuzzleForDate(date)
}

// announceDB is a migrated database, seeded the way the real app would seed
// one: through store.CreatePlayer and ingest.Apply, not hand-built rows —
// the rules that turn results into a month's winner belong to internal/stats
// and are exercised there; what this package needs is a realistic store to
// drive its own plumbing against.
func announceDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(ctx, db, store.Migrations()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return db
}

// fill posts guesses solved results for slug across n consecutive days
// starting at year-month-day, so the whole run lands inside one calendar
// month.
func fill(t *testing.T, db *sql.DB, slug string, year int, month time.Month, day, n, guesses int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		date := time.Date(year, month, day+i, 0, 0, 0, 0, time.Local)
		puzzle := wordlePuzzleForDate(t, date)
		g := guesses
		sub := ingest.Submission{
			Slug: slug, PuzzleNo: puzzle, Solved: true, Guesses: &g,
		}
		if _, err := ingest.Apply(ctx, db, store.SystemActor(), sub, false); err != nil {
			t.Fatalf("seed result for %s on %s: %v", slug, date, err)
		}
	}
}

func loadCatalogues(t *testing.T) i18n.Catalogues {
	t.Helper()
	cats, err := i18n.Load()
	if err != nil {
		t.Fatalf("i18n.Load: %v", err)
	}
	return cats
}

// A clear win: Alice averages better than Bob by a margin worth stating,
// both past the minimum games. The exact wording mirrors what the board
// itself would render for the same month — see internal/web/months.go's
// WinnerLine — so this also pins that the two have not drifted apart.
func TestAnnouncesTheClearWinnerOnce(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.March, 1, 12, 2)
	fill(t, db, "bob", 2026, time.March, 1, 12, 4)

	var sent []string
	var calls atomic.Int32
	send := func(_ context.Context, text string) error {
		calls.Add(1)
		sent = append(sent, text)
		return nil
	}

	announce := New(db, loadCatalogues(t), "en", send)
	now := time.Date(2026, time.April, 5, 9, 0, 0, 0, time.Local)

	if err := announce(ctx, now); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("send called %d times, want 1", calls.Load())
	}
	want := "Alice took March 2026 by 2.00 of a guess, over 12 puzzles."
	if sent[0] != want {
		t.Errorf("message = %q, want %q", sent[0], want)
	}

	done, err := store.MonthAnnounced(ctx, db, 2026, time.March)
	if err != nil {
		t.Fatalf("MonthAnnounced: %v", err)
	}
	if !done {
		t.Error("the month was not recorded as announced after a successful send")
	}

	// A restart, or a replayed live message, calls this again with the same
	// "now": nothing must go out a second time.
	if err := announce(ctx, now); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("send called %d times after a repeat call, want still 1", calls.Load())
	}
}

// The scheduler and a live result can both check at noon. They share one
// Announcer in the running app, which must serialize the check/send/record
// sequence rather than letting both observe an unannounced month.
func TestConcurrentChecksSendOnce(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.March, 1, 12, 2)

	var calls atomic.Int32
	announce := New(db, loadCatalogues(t), "en", func(context.Context, string) error {
		calls.Add(1)
		return nil
	})
	now := time.Date(2026, time.April, 1, 12, 0, 0, 0, time.Local)

	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- announce(ctx, now)
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
	if calls.Load() != 1 {
		t.Errorf("send called %d times, want 1", calls.Load())
	}
}

// Below the minimum games, nobody is a winner. The board says so on the
// page; the group is not told anything, which is the point — see New's
// comment on why silence is the answer here rather than the "nobody
// reached %d games" line the board renders.
func TestSaysNothingWhenNobodyQualified(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.March, 1, 3, 2) // 3 games, short of MinGames

	var calls atomic.Int32
	send := func(context.Context, string) error {
		calls.Add(1)
		return nil
	}

	announce := New(db, loadCatalogues(t), "en", send)
	now := time.Date(2026, time.April, 5, 9, 0, 0, 0, time.Local)

	if err := announce(ctx, now); err != nil {
		t.Fatalf("announce: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("send was called for a month with no qualifying player")
	}

	done, err := store.MonthAnnounced(ctx, db, 2026, time.March)
	if err != nil {
		t.Fatalf("MonthAnnounced: %v", err)
	}
	if done {
		t.Error("a month with nothing to say was recorded as announced")
	}
}

// Nothing played at all that month — the bridge only just started
// watching, say — must be as quiet as nobody qualifying, and for the same
// reason: there is nothing to tell the group.
func TestSaysNothingForAMonthWithNoResultsAtAll(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	var calls atomic.Int32
	send := func(context.Context, string) error {
		calls.Add(1)
		return nil
	}

	announce := New(db, loadCatalogues(t), "en", send)
	now := time.Date(2026, time.April, 5, 9, 0, 0, 0, time.Local)

	if err := announce(ctx, now); err != nil {
		t.Fatalf("announce: %v", err)
	}
	if calls.Load() != 0 {
		t.Error("send was called for a month with no results")
	}
}

// Midnight starts the new month, but the group gets the whole morning to
// file late closing-day results before the announcement becomes eligible.
func TestWaitsUntilNoonOnTheFirstDay(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.March, 1, 12, 2)
	fill(t, db, "bob", 2026, time.March, 1, 12, 4)

	var calls atomic.Int32
	announce := New(db, loadCatalogues(t), "en", func(context.Context, string) error {
		calls.Add(1)
		return nil
	})
	beforeNoon := time.Date(2026, time.April, 1, 11, 59, 59, 0, time.Local)

	if err := announce(ctx, beforeNoon); err != nil {
		t.Fatalf("before noon: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("announced before noon on the first")
	}

	noon := time.Date(2026, time.April, 1, 12, 0, 0, 0, time.Local)
	if err := announce(ctx, noon); err != nil {
		t.Fatalf("at noon: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("send called %d times at noon, want 1", calls.Load())
	}
}

// If the app was offline at noon, a later live result calls the same closure
// and catches up the missed post.
func TestLaterLiveResultCatchesUpAMissedNoon(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	fill(t, db, "alice", 2026, time.March, 1, 12, 2)

	var calls atomic.Int32
	announce := New(db, loadCatalogues(t), "en", func(context.Context, string) error {
		calls.Add(1)
		return nil
	})
	later := time.Date(2026, time.April, 3, 8, 0, 0, 0, time.Local)
	if err := announce(ctx, later); err != nil {
		t.Fatalf("later live result: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("send called %d times during catch-up, want 1", calls.Load())
	}
}

// A failed send must leave the month unrecorded, so the very next live
// message tries again rather than the month going quietly unannounced
// forever.
func TestFailedSendIsNotRecordedAndIsRetried(t *testing.T) {
	db := announceDB(t)
	ctx := context.Background()

	mustPlayer(t, db, "Alice", "alice")
	mustPlayer(t, db, "Bob", "bob")
	fill(t, db, "alice", 2026, time.March, 1, 12, 2)
	fill(t, db, "bob", 2026, time.March, 1, 12, 4)

	var calls atomic.Int32
	failing := true
	send := func(context.Context, string) error {
		calls.Add(1)
		if failing {
			return errors.New("signal is unreachable")
		}
		return nil
	}

	announce := New(db, loadCatalogues(t), "en", send)
	now := time.Date(2026, time.April, 5, 9, 0, 0, 0, time.Local)

	if err := announce(ctx, now); err == nil {
		t.Fatal("announce() succeeded despite the send failing")
	}
	if done, _ := store.MonthAnnounced(ctx, db, 2026, time.March); done {
		t.Fatal("a failed send was recorded as announced")
	}

	failing = false
	if err := announce(ctx, now); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("send called %d times across the failure and the retry, want 2", calls.Load())
	}
	if done, _ := store.MonthAnnounced(ctx, db, 2026, time.March); !done {
		t.Error("the retry's successful send was not recorded")
	}
}

// December's winner belongs to the year that just ended, not the one that
// just began.
func TestPreviousMonthCrossesTheYearBoundary(t *testing.T) {
	year, month := previousMonth(time.Date(2027, time.January, 3, 0, 0, 0, 0, time.Local))
	if year != 2026 || month != time.December {
		t.Errorf("previousMonth(Jan 2027) = %d-%s, want 2026-December", year, month)
	}
}

func TestTieAnnouncementNamesEveryWinner(t *testing.T) {
	avg := 2.5
	m := stats.Month{Winners: []stats.MonthPlayer{
		{Player: store.Player{Name: "Alice"}, Average: &avg},
		{Player: store.Player{Name: "Bob"}, Average: &avg},
		{Player: store.Player{Name: "Charlie"}, Average: &avg},
	}}
	tr := i18n.NewTranslator(loadCatalogues(t), "en")

	got, ok := winnerLine(tr, m)
	if !ok {
		t.Fatal("winnerLine() reported no winner for a three-way tie")
	}
	want := "Alice, Bob & Charlie: A tie at 2.50. They take the month."
	if got != want {
		t.Errorf("winnerLine() = %q, want %q", got, want)
	}
}

func TestWinnerLineUsesTheConfiguredLocale(t *testing.T) {
	avg, margin := 3.0, 1.25
	m := stats.Month{
		Year: 2026, Month: time.March,
		Winners: []stats.MonthPlayer{{
			Player: store.Player{Name: "Alice"}, Average: &avg, Games: 12,
		}},
		Margin: &margin,
	}
	tr := i18n.NewTranslator(loadCatalogues(t), "sv")

	got, ok := winnerLine(tr, m)
	if !ok {
		t.Fatal("winnerLine() reported no winner")
	}
	want := "Alice tog mars 2026 med 1.25 gissning, över 12 pussel."
	if got != want {
		t.Errorf("winnerLine() = %q, want %q", got, want)
	}
}

func mustPlayer(t *testing.T, db *sql.DB, name, slug string) {
	t.Helper()
	if _, err := store.CreatePlayer(context.Background(), db, store.SystemActor(), name, slug); err != nil {
		t.Fatalf("CreatePlayer(%s): %v", slug, err)
	}
}
