package store

import (
	"context"
	"errors"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Martin":            "martin",
		"martin":            "martin",
		"  Martin  ":        "martin",
		"Anna Karin":        "anna-karin",
		"Jean-Luc":          "jean-luc",
		"O'Brien":           "o-brien",
		"Player 2":          "player-2",
		"multiple   spaces": "multiple-spaces",

		// Folded, not dropped: the roster is Swedish, and losing a letter
		// from a stable identifier is worse than transliterating it.
		"Åsa":   "asa",
		"Örjan": "orjan",
		"Renée": "renee",

		// Transliterated by the map, since NFD leaves these intact.
		"Ærlig":   "aerlig",
		"Straße":  "strasse",
		"Øystein": "oystein",
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			got, err := Slugify(in)
			if err != nil {
				t.Fatalf("Slugify(%q) failed: %v", in, err)
			}
			if got != want {
				t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// A slug ends up in URLs and in every CLI invocation, so a name that cannot
// be represented must be reported rather than quietly reduced. --slug exists
// for exactly these cases.
func TestSlugifyRejectsLossyNames(t *testing.T) {
	tests := map[string]string{
		"empty":            "",
		"only punctuation": "!!!",
		"only spaces":      "   ",
		"cyrillic":         "Мартин",
		"chinese":          "马丁",
		"greek":            "Δημήτρης",
		"japanese":         "たろう",
		"emoji only":       "🙂",

		// The dangerous case: enough ASCII to produce a plausible-looking
		// slug, with a letter silently missing from it.
		"mixed script":    "Martin Мартин",
		"unmapped letter": "Ħamed",
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Slugify(in)
			if err == nil {
				t.Fatalf("Slugify(%q) = %q, want ErrUnslugifiable", in, got)
			}
			if !errors.Is(err, ErrUnslugifiable) {
				t.Errorf("error = %v, want ErrUnslugifiable", err)
			}
		})
	}
}

func TestValidSlug(t *testing.T) {
	valid := []string{"martin", "anna-karin", "player-2", "a", "x1"}
	invalid := []string{"", "Martin", "anna karin", "trailing-", "-leading", "double--hyphen", "emoji-🙂", "under_score"}

	for _, s := range valid {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}

func TestCreatePlayerDerivesSlug(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if player.Slug != "martin" {
		t.Errorf("Slug = %q, want %q", player.Slug, "martin")
	}
	if !player.Active {
		t.Error("a new player is inactive, want active")
	}
	if player.UserID != nil {
		t.Error("a new player already has a login linked")
	}
}

// Display names are neither unique nor stable — the roster contains two
// members whose names differ by one letter — so a derived slug must not
// collide silently.
func TestCreatePlayerSuffixesCollidingSlugs(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	want := []string{"martin", "martin-2", "martin-3"}
	for i, expected := range want {
		player, err := CreatePlayer(ctx, db, actor, "Martin", "")
		if err != nil {
			t.Fatalf("CreatePlayer() %d failed: %v", i, err)
		}
		if player.Slug != expected {
			t.Errorf("player %d slug = %q, want %q", i, player.Slug, expected)
		}
	}
}

func TestCreatePlayerExplicitSlug(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "mrt")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if player.Slug != "mrt" {
		t.Errorf("Slug = %q, want %q", player.Slug, "mrt")
	}

	// An explicit slug is not silently suffixed: the caller asked for a
	// specific one, so a collision is an error rather than a near miss.
	if _, err := CreatePlayer(ctx, db, actor, "Someone", "mrt"); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("error = %v, want ErrSlugTaken", err)
	}
}

func TestCreatePlayerRejectsInvalidSlug(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	if _, err := CreatePlayer(ctx, db, actor, "Martin", "Not A Slug"); !errors.Is(err, ErrInvalidSlug) {
		t.Errorf("error = %v, want ErrInvalidSlug", err)
	}
}

func TestCreatePlayerRejectsUnslugifiableName(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	for _, name := range []string{"!!!", "Мартин", "Ħamed"} {
		_, err := CreatePlayer(ctx, db, actor, name, "")
		if !errors.Is(err, ErrUnslugifiable) {
			t.Errorf("CreatePlayer(%q) error = %v, want ErrUnslugifiable", name, err)
		}
	}

	// Passing one explicitly is the way through.
	if _, err := CreatePlayer(ctx, db, actor, "Мартин", "martin"); err != nil {
		t.Errorf("CreatePlayer() with an explicit slug failed: %v", err)
	}
}

// The database holds the same rule, so a future write path that forgets to
// validate cannot introduce a slug that breaks a URL.
func TestSchemaRejectsInvalidSlug(t *testing.T) {
	db := migratedDB(t)

	for _, slug := range []string{"", "Martin", "anna karin", "-leading", "trailing-", "double--hyphen", "emoji🙂"} {
		if _, err := db.Exec(`INSERT INTO players (slug, name) VALUES (?, ?)`, slug, "Someone"); err == nil {
			t.Errorf("the schema accepted slug %q", slug)
		}
	}
	for _, slug := range []string{"martin", "anna-karin", "player-2", "x1"} {
		if _, err := db.Exec(`INSERT INTO players (slug, name) VALUES (?, ?)`, slug, "Someone"); err != nil {
			t.Errorf("the schema rejected valid slug %q: %v", slug, err)
		}
	}
}

// Ownership is an invariant, so the constraint belongs in the schema
// rather than only in the code path that happens to check it today.
func TestSchemaRejectsDoubleLink(t *testing.T) {
	db := migratedDB(t)
	userID := seedUser(t, db, "martin@example.tld", false)
	first := seedPlayer(t, db, "martin")
	second := seedPlayer(t, db, "alex")

	if _, err := db.Exec(`UPDATE players SET user_id = ? WHERE id = ?`, userID, first); err != nil {
		t.Fatalf("first link failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE players SET user_id = ? WHERE id = ?`, userID, second); err == nil {
		t.Error("the schema allowed one user to hold two players")
	}

	// Multiple unlinked players must remain fine: SQLite treats NULLs as
	// distinct for uniqueness, which is what makes this constraint usable.
	third := seedPlayer(t, db, "sam")
	if _, err := db.Exec(`UPDATE players SET user_id = NULL WHERE id = ?`, third); err != nil {
		t.Errorf("leaving a player unlinked failed: %v", err)
	}
}

// : update must only touch the fields actually passed. Go's flag package
// defaults an unset bool to false, so the risk is deactivating a player as a
// side effect of renaming them.
func TestUpdatePlayerTouchesOnlyProvidedFields(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	newName := "Martin S"
	updated, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{Name: &newName})
	if err != nil {
		t.Fatalf("UpdatePlayer() failed: %v", err)
	}

	if updated.Name != newName {
		t.Errorf("Name = %q, want %q", updated.Name, newName)
	}
	if !updated.Active {
		t.Error("renaming a player deactivated them; an unset --active must not reach the column")
	}
	if updated.Slug != "martin" {
		t.Errorf("Slug = %q, want it unchanged", updated.Slug)
	}
}

// The inverse: an explicit --active=false must be honoured, and is what
// distinguishes "not provided" from "provided as the zero value".
func TestUpdatePlayerExplicitFalseIsApplied(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	inactive := false
	updated, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{Active: &inactive})
	if err != nil {
		t.Fatalf("UpdatePlayer() failed: %v", err)
	}
	if updated.Active {
		t.Error("Active = true after --active=false")
	}
}

func TestUpdatePlayerRenameAndSlug(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	name, slug := "Martin S", "martin-s"
	updated, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{Name: &name, Slug: &slug})
	if err != nil {
		t.Fatalf("UpdatePlayer() failed: %v", err)
	}
	if updated.Name != name || updated.Slug != slug {
		t.Errorf("got name=%q slug=%q, want %q/%q", updated.Name, updated.Slug, name, slug)
	}
}

func TestUpdatePlayerRejectsTakenSlug(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	first, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if _, err := CreatePlayer(ctx, db, actor, "Alex", "alex"); err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	taken := "alex"
	if _, err := UpdatePlayer(ctx, db, actor, first.ID, PlayerUpdate{Slug: &taken}); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("error = %v, want ErrSlugTaken", err)
	}
}

func TestUpdatePlayerRejectsEmptyUpdate(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	if _, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{}); err == nil {
		t.Error("UpdatePlayer() accepted an empty update")
	}
}

func TestUpdatePlayerUnknownPlayer(t *testing.T) {
	db := migratedDB(t)
	_, actor := adminFixture(t, db)

	name := "Nobody"
	_, err := UpdatePlayer(context.Background(), db, actor, 9999, PlayerUpdate{Name: &name})
	if !errors.Is(err, ErrPlayerNotFound) {
		t.Errorf("error = %v, want ErrPlayerNotFound", err)
	}
}

// Retirement and return are membership decisions, so they are logged
// as themselves rather than buried in a generic update.
func TestUpdatePlayerLogsRetirementDistinctly(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	inactive, active := false, true
	if _, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{Active: &inactive}); err != nil {
		t.Fatalf("retire failed: %v", err)
	}
	if _, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{Active: &active}); err != nil {
		t.Fatalf("reactivate failed: %v", err)
	}
	name := "Martin S"
	if _, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{Name: &name}); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	want := []string{ActionPlayerCreated, ActionPlayerRetired, ActionPlayerReactivated, ActionPlayerUpdated}
	got := activityActions(t, db, SubjectPlayer, player.ID)
	if len(got) != len(want) {
		t.Fatalf("activity actions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("activity action %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A no-op update writes nothing, so the log records changes rather than
// commands that happened to be run.
func TestUpdatePlayerNoOpIsNotLogged(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	sameName := "Martin"
	if _, err := UpdatePlayer(ctx, db, actor, player.ID, PlayerUpdate{Name: &sameName}); err != nil {
		t.Fatalf("UpdatePlayer() failed: %v", err)
	}

	got := activityActions(t, db, SubjectPlayer, player.ID)
	if len(got) != 1 || got[0] != ActionPlayerCreated {
		t.Errorf("activity actions = %v, want only the creation entry", got)
	}
}

func TestLinkAndUnlinkPlayer(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	linked, err := LinkPlayer(ctx, db, actor, player.ID, &user.ID)
	if err != nil {
		t.Fatalf("LinkPlayer() failed: %v", err)
	}
	if linked.UserID == nil || *linked.UserID != user.ID {
		t.Fatalf("UserID = %v, want %d", linked.UserID, user.ID)
	}

	unlinked, err := LinkPlayer(ctx, db, actor, player.ID, nil)
	if err != nil {
		t.Fatalf("LinkPlayer(nil) failed: %v", err)
	}
	if unlinked.UserID != nil {
		t.Errorf("UserID = %v after unlink, want nil", unlinked.UserID)
	}

	want := []string{ActionPlayerCreated, ActionPlayerLinked, ActionPlayerUnlinked}
	if got := activityActions(t, db, SubjectPlayer, player.ID); len(got) != 3 {
		t.Errorf("activity actions = %v, want %v", got, want)
	}
}

// One login per player: ownership is an invariant, so a single account
// must not be able to self-report as two people.
func TestLinkPlayerRejectsUserAlreadyLinked(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	first, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	second, err := CreatePlayer(ctx, db, actor, "Alex", "alex")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	if _, err := LinkPlayer(ctx, db, actor, first.ID, &user.ID); err != nil {
		t.Fatalf("first link failed: %v", err)
	}
	if _, err := LinkPlayer(ctx, db, actor, second.ID, &user.ID); err == nil {
		t.Error("linking one user to a second player succeeded")
	}
}

// Relinking the same user to the same player is not a collision with itself.
func TestLinkPlayerIsIdempotent(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	user, err := CreateUser(ctx, db, actor, "martin@example.tld", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	player, err := CreatePlayer(ctx, db, actor, "Martin", "martin")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := LinkPlayer(ctx, db, actor, player.ID, &user.ID); err != nil {
			t.Fatalf("link %d failed: %v", i, err)
		}
	}
}

func TestListPlayers(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	for _, name := range []string{"Martin", "Alex", "Sam"} {
		if _, err := CreatePlayer(ctx, db, actor, name, ""); err != nil {
			t.Fatalf("CreatePlayer(%s) failed: %v", name, err)
		}
	}

	players, err := ListPlayers(ctx, db)
	if err != nil {
		t.Fatalf("ListPlayers() failed: %v", err)
	}
	if len(players) != 3 {
		t.Fatalf("got %d players, want 3", len(players))
	}
	for i := 1; i < len(players); i++ {
		if players[i-1].Slug > players[i].Slug {
			t.Errorf("players are not ordered by slug: %q before %q", players[i-1].Slug, players[i].Slug)
		}
	}
}
