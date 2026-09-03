package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// demoCLI is newCLI plus an admin, since every demo verb needs --as.
func demoCLI(t *testing.T) *cli {
	t.Helper()
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	return c
}

func TestDemoRequiresDemoMode(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "")

	for _, verb := range []string{"seed", "tick", "clear"} {
		_, err := c.run("", "--as", "admin@example.tld", "demo", verb)
		if err == nil {
			t.Errorf("demo %s succeeded with DEMO_MODE unset, want it refused", verb)
			continue
		}
		if !strings.Contains(err.Error(), "DEMO_MODE") {
			t.Errorf("demo %s error = %v, want it to name DEMO_MODE", verb, err)
		}
	}
}

func TestDemoSeedCreatesPlayersAndHistory(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")

	out := c.mustRun("", "--as", "admin@example.tld", "demo", "seed",
		"--players", "6", "--days", "30", "--seed", "1")
	if !strings.Contains(out, "Seeded 6 player(s) over 30 day(s)") {
		t.Errorf("output does not summarize the seed run:\n%s", out)
	}

	db := c.db()
	ctx := context.Background()

	players, err := store.ListPlayers(ctx, db)
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	if len(players) != 6 {
		t.Fatalf("player count = %d, want 6", len(players))
	}

	var results int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&results); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if results == 0 {
		t.Error("no results were backfilled")
	}

	var pending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_results`).Scan(&pending); err != nil {
		t.Fatalf("count pending_results: %v", err)
	}
	if pending != 3 {
		t.Errorf("pending_results count = %d, want 3 unclaimed senders", pending)
	}

	var retired int
	for _, p := range players {
		if !p.Active {
			retired++
		}
	}
	if retired != 1 {
		t.Errorf("retired player count = %d, want exactly 1", retired)
	}

	// Names must not carry a "demo-" prefix: they need to read as an
	// ordinary roster, per CLAUDE.md's rule against fabricating anything
	// that looks like it names a real identifiable group's data pattern.
	for _, p := range players {
		if strings.HasPrefix(strings.ToLower(p.Name), "demo") || strings.HasPrefix(p.Slug, "demo") {
			t.Errorf("player %q (slug %q) carries a demo- style prefix, want an ordinary-looking name", p.Name, p.Slug)
		}
	}
}

// TestDemoSeedIsReproducibleForSameSeed is the CLI-level half of --seed:
// internal/demo already proves NewRoster is deterministic, this proves the
// seed verb passes it through end to end, into names actually written to
// two independent databases.
func TestDemoSeedIsReproducibleForSameSeed(t *testing.T) {
	a := demoCLI(t)
	b := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")

	a.mustRun("", "--as", "admin@example.tld", "demo", "seed", "--players", "5", "--days", "20", "--seed", "99")
	b.mustRun("", "--as", "admin@example.tld", "demo", "seed", "--players", "5", "--days", "20", "--seed", "99")

	ctx := context.Background()
	playersA, err := store.ListPlayers(ctx, a.db())
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	playersB, err := store.ListPlayers(ctx, b.db())
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	if len(playersA) != len(playersB) {
		t.Fatalf("player counts differ: %d vs %d", len(playersA), len(playersB))
	}
	for i := range playersA {
		if playersA[i].Name != playersB[i].Name || playersA[i].Active != playersB[i].Active {
			t.Errorf("player %d differs between runs with the same seed: %+v vs %+v", i, playersA[i], playersB[i])
		}
	}

	var resultsA, resultsB int
	if err := a.db().QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&resultsA); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if err := b.db().QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&resultsB); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if resultsA != resultsB {
		t.Errorf("result counts differ between runs with the same seed: %d vs %d", resultsA, resultsB)
	}
}

func TestDemoSeedRejectsTooManyPlayersForTheNamePool(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")

	if _, err := c.run("", "--as", "admin@example.tld", "demo", "seed", "--players", "10000"); err == nil {
		t.Error("demo seed with an unreasonable --players succeeded, want an error")
	}
}

// TestDemoSeedRejectsTooFewDaysForTheMissingPersona pins the floor below
// which the reserved "Missing" persona cannot possibly leave
// stats.AbsentDays of trailing absence, whatever Played's cutoff formula
// does — --days must be rejected outright rather than silently seeding a
// persona that never triggers the callout it exists to demonstrate.
func TestDemoSeedRejectsTooFewDaysForTheMissingPersona(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")

	_, err := c.run("", "--as", "admin@example.tld", "demo", "seed", "--days", "7")
	if err == nil {
		t.Fatal("demo seed --days 7 succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "--days") {
		t.Errorf("error = %v, want it to name --days", err)
	}
}

// TestDemoTickIsIdempotent is the requirement that running tick twice for
// the same puzzle does not double-file, error, or quietly re-roll an
// existing result with a new random outcome. Seeding's backfill already
// reaches today's puzzle for most players, so a row count alone would not
// catch a second tick overwriting one of them with different guesses; this
// snapshots every row's content instead. Neither run passes --seed: a real
// deployment never does, and the default must be safe to repeat exactly
// because of that.
func TestDemoTickIsIdempotent(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")
	c.mustRun("", "--as", "admin@example.tld", "demo", "seed", "--players", "6", "--days", "10", "--seed", "1")

	c.mustRun("", "--as", "admin@example.tld", "demo", "tick")

	ctx := context.Background()
	before := snapshotResults(t, c.db(), ctx)

	out := c.mustRun("", "--as", "admin@example.tld", "demo", "tick")
	if !strings.Contains(out, "filed 0 result") {
		t.Errorf("second tick output = %q, want it to file nothing", out)
	}

	after := snapshotResults(t, c.db(), ctx)
	if len(before) != len(after) {
		t.Fatalf("result count changed from %d to %d on a repeat tick, want no change", len(before), len(after))
	}
	for key, want := range before {
		if got := after[key]; got != want {
			t.Errorf("result %v changed from %q to %q on a repeat tick, want it untouched", key, want, got)
		}
	}
}

// TestDemoTickRerollsIdenticallyForAPlayerNotYetDecided is the sharper
// failure a bare "already filed" check misses: a player tick decides should
// sit a day out gets no row, so a second run has nothing to compare against
// and must reroll — the fix is that the reroll reproduces the exact same
// decision, for every player, not a fresh one. Forces every active player
// to lack today's result first, so this exercises tick's own roll for the
// whole roster rather than whatever backfill happened to decide for it.
func TestDemoTickRerollsIdenticallyForAPlayerNotYetDecided(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")
	c.mustRun("", "--as", "admin@example.tld", "demo", "seed", "--players", "10", "--days", "10", "--seed", "1")

	ctx := context.Background()
	db := c.db()
	today := wordle.PuzzleForDate(time.Now())
	if _, err := db.ExecContext(ctx, `DELETE FROM results WHERE puzzle_no = ?`, today); err != nil {
		t.Fatalf("clear today's results: %v", err)
	}

	c.mustRun("", "--as", "admin@example.tld", "demo", "tick")
	first := snapshotResults(t, db, ctx)

	c.mustRun("", "--as", "admin@example.tld", "demo", "tick")
	second := snapshotResults(t, db, ctx)

	if len(first) != len(second) {
		t.Fatalf("result count for today changed from %d to %d on a repeat tick with nothing decided yet, want identical",
			len(first), len(second))
	}
	for key, want := range first {
		if got := second[key]; got != want {
			t.Errorf("result %v changed from %q to %q on a repeat tick, want the same roll both times", key, want, got)
		}
	}
}

// snapshotResults captures every row's content, keyed by (player, puzzle),
// so a repeat tick's effect on existing rows can be checked precisely
// rather than by row count alone.
func snapshotResults(t *testing.T, db *sql.DB, ctx context.Context) map[[2]int64]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT player_id, puzzle_no, solved, guesses, hard_mode FROM results`)
	if err != nil {
		t.Fatalf("query results: %v", err)
	}
	defer rows.Close()

	snapshot := make(map[[2]int64]string)
	for rows.Next() {
		var playerID, puzzleNo int64
		var solved, hardMode bool
		var guesses sql.NullInt64
		if err := rows.Scan(&playerID, &puzzleNo, &solved, &guesses, &hardMode); err != nil {
			t.Fatalf("scan result: %v", err)
		}
		snapshot[[2]int64{playerID, puzzleNo}] = fmt.Sprintf("solved=%v guesses=%v hard=%v", solved, guesses, hardMode)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate results: %v", err)
	}
	return snapshot
}

// TestDemoTickNeverReactivatesRetiredPlayer is the one invariant the whole
// verb cannot violate without silently contradicting the point of
// retirement: tick reads Active from the database rather than tracking who
// it made, and must skip a retired player rather than resurrect them.
func TestDemoTickNeverReactivatesRetiredPlayer(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")
	c.mustRun("", "--as", "admin@example.tld", "demo", "seed", "--players", "6", "--days", "10", "--seed", "1")

	ctx := context.Background()
	players, err := store.ListPlayers(ctx, c.db())
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	var retiredID int64
	found := false
	for _, p := range players {
		if !p.Active {
			retiredID, found = p.ID, true
		}
	}
	if !found {
		t.Fatal("seed produced no retired player to test against")
	}

	// Backfill may or may not have reached today for this player before
	// they were retired; forced to zero here so the assertion below is
	// deterministic rather than depending on that roll.
	if _, err := c.db().ExecContext(ctx, `DELETE FROM results WHERE player_id = ?`, retiredID); err != nil {
		t.Fatalf("clear retired player's results: %v", err)
	}

	c.mustRun("", "--as", "admin@example.tld", "demo", "tick", "--seed", "2")

	player, err := store.PlayerByID(ctx, c.db(), retiredID)
	if err != nil {
		t.Fatalf("PlayerByID() failed: %v", err)
	}
	if player.Active {
		t.Error("tick reactivated a retired player")
	}

	// The sharper failure this guards against: tick filing a fresh result
	// for someone who left, which would make them look like they are still
	// playing without ever flipping Active back to true.
	var after int
	if err := c.db().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM results WHERE player_id = ?`, retiredID).Scan(&after); err != nil {
		t.Fatalf("count results for retired player: %v", err)
	}
	if after != 0 {
		t.Errorf("retired player has %d result(s) after tick, want 0; tick must not play on their behalf", after)
	}
}

func TestDemoClearDryRunThenApply(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")
	c.mustRun("", "--as", "admin@example.tld", "demo", "seed", "--players", "4", "--days", "10", "--seed", "1")

	ctx := context.Background()

	dry := c.mustRun("", "--as", "admin@example.tld", "demo", "clear")
	if !strings.Contains(dry, "Would have deleted 4 player") {
		t.Errorf("dry run output = %q, want it to say what it would delete", dry)
	}
	if !strings.Contains(dry, "Nothing was written") {
		t.Errorf("dry run output = %q, want it to say nothing was written", dry)
	}

	players, err := store.ListPlayers(ctx, c.db())
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	if len(players) != 4 {
		t.Fatalf("player count after a dry run = %d, want 4 (unchanged)", len(players))
	}

	applied := c.mustRun("", "--as", "admin@example.tld", "demo", "clear", "--apply")
	if strings.Contains(applied, "Would have") {
		t.Errorf("--apply output still hedges: %q", applied)
	}

	players, err = store.ListPlayers(ctx, c.db())
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	if len(players) != 0 {
		t.Errorf("player count after --apply = %d, want 0", len(players))
	}

	// The one hard constraint on the whole verb: it must not be able to
	// take the operator's own account down with the players it deletes.
	if _, err := store.UserByEmail(ctx, c.db(), "admin@example.tld"); err != nil {
		t.Errorf("admin account did not survive demo clear: %v", err)
	}
}

// TestDemoClearReportsBlockedInvitation exercises the RESTRICT case
// clear must report rather than crash on: a player still named by an
// invitation row, which invitations.player_id refuses to let go of.
func TestDemoClearReportsBlockedInvitation(t *testing.T) {
	c := demoCLI(t)
	t.Setenv("DEMO_MODE", "true")
	c.mustRun("", "--as", "admin@example.tld", "demo", "seed", "--players", "1", "--days", "10", "--seed", "1")

	ctx := context.Background()
	db := c.db()

	players, err := store.ListPlayers(ctx, db)
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	if len(players) != 1 {
		t.Fatalf("player count = %d, want 1", len(players))
	}
	blocked := players[0]

	admin, err := store.UserByEmail(ctx, db, "admin@example.tld")
	if err != nil {
		t.Fatalf("UserByEmail() failed: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO invitations (token_hash, email, player_id, invited_by, expires_at, locale)
		 VALUES ('hash', 'invitee@example.tld', ?, ?, '2099-01-01', 'en')`,
		blocked.ID, admin.ID); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	out, err := c.run("", "--as", "admin@example.tld", "demo", "clear", "--apply")
	if err != nil {
		t.Fatalf("demo clear --apply failed instead of reporting the block: %v\n%s", err, out)
	}
	if !strings.Contains(out, blocked.Slug) {
		t.Errorf("output = %q, want it to name the blocked player %q", out, blocked.Slug)
	}

	remaining, err := store.ListPlayers(ctx, db)
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != blocked.ID {
		t.Errorf("players remaining = %v, want only the blocked player left behind", remaining)
	}
}
