package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGenerateSlug(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		slug, err := GenerateSlug()
		if err != nil {
			t.Fatalf("GenerateSlug() failed: %v", err)
		}
		if len(slug) != slugLength {
			t.Fatalf("slug length = %d, want %d", len(slug), slugLength)
		}
		if seen[slug] {
			t.Fatalf("GenerateSlug() repeated %q within 200 draws", slug)
		}
		seen[slug] = true

		// The alphabet omits vowels and the characters that are easy to
		// confuse when a link is retyped from a phone: 0/o and 1/l/i.
		for _, r := range slug {
			if !strings.ContainsRune(slugAlphabet, r) {
				t.Fatalf("slug %q contains %q, which is outside the alphabet", slug, r)
			}
		}
	}
}

func TestShareSlugBeforeBootstrap(t *testing.T) {
	db := migratedDB(t)

	// Only the app creates the row, so the CLI seeing this means the app
	// has never run against the database.
	_, err := ShareSlug(context.Background(), db)
	if !errors.Is(err, ErrNoSettings) {
		t.Errorf("error = %v, want ErrNoSettings", err)
	}
}

func TestEnsureShareSlugCreatesOnce(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	slug, created, err := EnsureShareSlug(ctx, db)
	if err != nil {
		t.Fatalf("EnsureShareSlug() failed: %v", err)
	}
	if !created {
		t.Error("created = false on the first call")
	}
	if slug == "" {
		t.Fatal("slug is empty")
	}

	// Restarting the app must not mint a new share link: everyone
	// holding the old URL would lose access without anyone asking for that.
	again, created, err := EnsureShareSlug(ctx, db)
	if err != nil {
		t.Fatalf("second EnsureShareSlug() failed: %v", err)
	}
	if created {
		t.Error("created = true on the second call")
	}
	if again != slug {
		t.Errorf("slug changed across restarts: %q then %q", slug, again)
	}
}

// The first slug should have the same visible provenance as every later
// rotation rather than appearing from nowhere.
func TestEnsureShareSlugIsLogged(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()

	if _, _, err := EnsureShareSlug(ctx, db); err != nil {
		t.Fatalf("EnsureShareSlug() failed: %v", err)
	}

	var kind, action string
	if err := db.QueryRow(
		`SELECT actor_kind, action FROM activity_log WHERE subject_type = ?`, SubjectSettings,
	).Scan(&kind, &action); err != nil {
		t.Fatalf("read activity entry: %v", err)
	}
	if kind != ActorSystem {
		t.Errorf("actor_kind = %q, want %q", kind, ActorSystem)
	}
	if action != ActionSlugGenerated {
		t.Errorf("action = %q, want %q", action, ActionSlugGenerated)
	}

	// A second startup must not log a generation that did not happen.
	if _, _, err := EnsureShareSlug(ctx, db); err != nil {
		t.Fatalf("second EnsureShareSlug() failed: %v", err)
	}
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM activity_log WHERE action = ?`, ActionSlugGenerated).Scan(&count); err != nil {
		t.Fatalf("count activity entries: %v", err)
	}
	if count != 1 {
		t.Errorf("slug generation logged %d times, want 1", count)
	}
}

func TestRotateShareSlug(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	original, _, err := EnsureShareSlug(ctx, db)
	if err != nil {
		t.Fatalf("EnsureShareSlug() failed: %v", err)
	}

	rotated, err := RotateShareSlug(ctx, db, actor)
	if err != nil {
		t.Fatalf("RotateShareSlug() failed: %v", err)
	}
	if rotated == original {
		t.Error("rotation produced the same slug")
	}

	current, err := ShareSlug(ctx, db)
	if err != nil {
		t.Fatalf("ShareSlug() failed: %v", err)
	}
	if current != rotated {
		t.Errorf("stored slug = %q, want the rotated value %q", current, rotated)
	}
}

// A link that stops working should be traceable to the rotation that retired
// it; the activity log is admin-only and the recorded slug is already spent.
func TestRotateShareSlugRecordsPrevious(t *testing.T) {
	db := migratedDB(t)
	ctx := context.Background()
	_, actor := adminFixture(t, db)

	original, _, err := EnsureShareSlug(ctx, db)
	if err != nil {
		t.Fatalf("EnsureShareSlug() failed: %v", err)
	}
	if _, err := RotateShareSlug(ctx, db, actor); err != nil {
		t.Fatalf("RotateShareSlug() failed: %v", err)
	}

	var detail string
	if err := db.QueryRow(
		`SELECT detail FROM activity_log WHERE action = ?`, ActionSlugRotated).Scan(&detail); err != nil {
		t.Fatalf("read activity detail: %v", err)
	}
	if !strings.Contains(detail, original) {
		t.Errorf("activity detail = %q, want it to record the previous slug %q", detail, original)
	}
}

func TestRotateShareSlugBeforeBootstrap(t *testing.T) {
	db := migratedDB(t)
	_, actor := adminFixture(t, db)

	_, err := RotateShareSlug(context.Background(), db, actor)
	if !errors.Is(err, ErrNoSettings) {
		t.Errorf("error = %v, want ErrNoSettings", err)
	}
}
