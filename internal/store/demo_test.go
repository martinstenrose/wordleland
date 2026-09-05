package store

import (
	"context"
	"testing"
)

func intPtr(n int) *int { return &n }

func TestClearDemoDataDeletesPlayersAndCascades(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	p, err := CreatePlayer(ctx, db, actor, "Test Player", "")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if _, err := LinkIdentity(ctx, db, actor, p.ID, "signal", "sender-1", ActionIdentityAdded, false); err != nil {
		t.Fatalf("LinkIdentity() failed: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO results (puzzle_no, date, player_id, guesses, solved) VALUES (1200, '2025-01-01', ?, 4, 1)`,
		p.ID); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	if err := HoldPendingResult(ctx, db, "signal", "unclaimed-1", "Someone", PendingResult{PuzzleNo: 1200, Solved: true, Guesses: intPtr(4)}); err != nil {
		t.Fatalf("HoldPendingResult() failed: %v", err)
	}

	summary, err := ClearDemoData(ctx, db, actor, false)
	if err != nil {
		t.Fatalf("ClearDemoData() failed: %v", err)
	}
	if summary.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", summary.Deleted)
	}
	if summary.PendingCleared != 1 {
		t.Errorf("PendingCleared = %d, want 1", summary.PendingCleared)
	}
	if len(summary.Blocked) != 0 {
		t.Errorf("Blocked = %v, want none", summary.Blocked)
	}

	var players, identities, results, pending int
	for table, dst := range map[string]*int{
		"players":           &players,
		"player_identities": &identities,
		"results":           &results,
		"pending_results":   &pending,
	} {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(dst); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
	}
	if players != 0 {
		t.Errorf("players remaining = %d, want 0", players)
	}
	if identities != 0 {
		t.Errorf("player_identities remaining = %d, want 0 (should cascade)", identities)
	}
	if results != 0 {
		t.Errorf("results remaining = %d, want 0 (should cascade)", results)
	}
	if pending != 0 {
		t.Errorf("pending_results remaining = %d, want 0", pending)
	}
}

// TestClearDemoDataLeavesUsersAndActivityUntouched pins the one hard
// requirement on the whole feature: a verb that deletes players must never
// be able to take an administrator, or their history in the log, with it.
func TestClearDemoDataLeavesUsersAndActivityUntouched(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	adminID, actor := adminFixture(t, db)

	p, err := CreatePlayer(ctx, db, actor, "Test Player", "")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO results (puzzle_no, date, player_id, guesses, solved, entered_by) VALUES (1200, '2025-01-01', ?, 4, 1, ?)`,
		p.ID, adminID); err != nil {
		t.Fatalf("seed result: %v", err)
	}

	var activityBefore int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_log`).Scan(&activityBefore); err != nil {
		t.Fatalf("count activity_log: %v", err)
	}

	// The result stays in place through the clear: results.player_id is ON
	// DELETE CASCADE, so the player delete removes it regardless of who
	// entered it. entered_by's own ON DELETE RESTRICT protects the users
	// row it names, not the player — and ClearDemoData never deletes users,
	// so it never comes into play here at all.
	if _, err := ClearDemoData(ctx, db, actor, false); err != nil {
		t.Fatalf("ClearDemoData() failed: %v", err)
	}

	var userCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, adminID).Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Errorf("admin user survived = %v, want the account untouched", userCount == 1)
	}

	var activityAfter int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_log`).Scan(&activityAfter); err != nil {
		t.Fatalf("count activity_log: %v", err)
	}
	// >= not ==: ClearDemoData itself appends a player.deleted entry, so the
	// log must have grown, not been wiped.
	if activityAfter <= activityBefore {
		t.Errorf("activity_log count = %d after clear, want more than the %d before (log must survive, not shrink)", activityAfter, activityBefore)
	}
}

// TestClearDemoDataReportsBlockedInvitation is the case the naive query
// (filtering to only currently-pending invitations) would miss: an
// invitation that has already been used still blocks the delete, because
// ON DELETE RESTRICT fires on the presence of the row, not its used_at.
// Without that fix this test fails with a foreign key constraint error
// instead of a reported, non-fatal Blocked entry.
func TestClearDemoDataReportsBlockedInvitation(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	blocked, err := CreatePlayer(ctx, db, actor, "Blocked Player", "")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	free, err := CreatePlayer(ctx, db, actor, "Free Player", "")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	if _, err := CreateInvitation(ctx, db, actor, blocked.ID, "invitee@example.tld", "en"); err != nil {
		t.Fatalf("CreateInvitation() failed: %v", err)
	}
	// Spend it, to prove a used invitation still blocks: RESTRICT does not
	// care what used_at says.
	if _, err := db.ExecContext(ctx,
		`UPDATE invitations SET used_at = CURRENT_TIMESTAMP WHERE player_id = ?`, blocked.ID); err != nil {
		t.Fatalf("mark invitation used: %v", err)
	}

	summary, err := ClearDemoData(ctx, db, actor, false)
	if err != nil {
		t.Fatalf("ClearDemoData() failed: %v", err)
	}
	if len(summary.Blocked) != 1 || summary.Blocked[0] != blocked.Slug {
		t.Errorf("Blocked = %v, want [%q]", summary.Blocked, blocked.Slug)
	}
	if summary.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 (only the free player)", summary.Deleted)
	}

	var remaining []string
	rows, err := db.QueryContext(ctx, `SELECT slug FROM players`)
	if err != nil {
		t.Fatalf("query players: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			t.Fatalf("scan slug: %v", err)
		}
		remaining = append(remaining, slug)
	}
	if len(remaining) != 1 || remaining[0] != blocked.Slug {
		t.Errorf("players remaining = %v, want only %q left behind", remaining, blocked.Slug)
	}
	_ = free
}

func TestClearDemoDataDryRunWritesNothing(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	p, err := CreatePlayer(ctx, db, actor, "Test Player", "")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if err := HoldPendingResult(ctx, db, "signal", "unclaimed-1", "Someone", PendingResult{PuzzleNo: 1200, Solved: true, Guesses: intPtr(4)}); err != nil {
		t.Fatalf("HoldPendingResult() failed: %v", err)
	}

	var activityBefore int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_log`).Scan(&activityBefore); err != nil {
		t.Fatalf("count activity_log: %v", err)
	}

	summary, err := ClearDemoData(ctx, db, actor, true)
	if err != nil {
		t.Fatalf("ClearDemoData(dryRun) failed: %v", err)
	}
	if summary.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 reported even under a dry run", summary.Deleted)
	}
	if summary.PendingCleared != 1 {
		t.Errorf("PendingCleared = %d, want 1 reported even under a dry run", summary.PendingCleared)
	}

	var playerCount, pendingCount, activityAfter int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM players WHERE id = ?`, p.ID).Scan(&playerCount); err != nil {
		t.Fatalf("count players: %v", err)
	}
	if playerCount != 1 {
		t.Errorf("player survived dry run = %v, want the row untouched", playerCount == 1)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_results`).Scan(&pendingCount); err != nil {
		t.Fatalf("count pending_results: %v", err)
	}
	if pendingCount != 1 {
		t.Errorf("pending_results survived dry run = %v, want the row untouched", pendingCount == 1)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_log`).Scan(&activityAfter); err != nil {
		t.Fatalf("count activity_log: %v", err)
	}
	if activityAfter != activityBefore {
		t.Errorf("activity_log count changed from %d to %d, want no entry written on a dry run", activityBefore, activityAfter)
	}
}
