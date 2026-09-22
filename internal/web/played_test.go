package web

import (
	"context"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// The count behind the wordmark's subtitle is read once a minute, not once
// a page. It was a query on every render, for a number that changes once a
// day; a result that lands is in the subtitle within the minute, and a page
// opened inside that minute still shows the count from before.
func TestThePlayedPuzzleCountIsHeldBetweenPages(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	ctx := context.Background()
	slug, _, _ := store.EnsureShareSlug(ctx, srv.db)

	subtitle := func() string {
		t.Helper()
		body := fetchAs(t, srv, "/share/"+slug+"/", nil).Body.String()
		s, ok := sectionOf(body, `<span class="brand-sub">`, "</span>")
		if !ok {
			t.Fatal("the page has no subtitle under the wordmark")
		}
		return s
	}
	before := subtitle()

	// A result for a puzzle nobody had played, well inside the window the
	// board is seeded with so it is a valid date.
	p, err := store.CreatePlayer(ctx, srv.db, store.SystemActor(), "Newcomer", "newcomer")
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	puzzle := wordle.PuzzleForDate(time.Now()) - 40
	date, err := wordle.DateForPuzzle(puzzle)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}
	guesses := 4
	if _, _, err := store.UpsertResult(ctx, srv.db, store.Result{
		PuzzleNo: puzzle, Date: date, PlayerID: p.ID, Guesses: &guesses, Solved: true,
	}, nil, nil); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}
	if n, _ := store.CountPlayedPuzzles(ctx, srv.db); n == 0 {
		t.Fatal("the seed left nothing to count")
	}

	// Inside the minute, the page shows what it showed.
	if got := subtitle(); got != before {
		t.Fatalf("the subtitle moved within the hold: %q, was %q", got, before)
	}

	// Once the hold is over, it moves.
	srv.played.Lock()
	srv.played.at = time.Time{}
	srv.played.Unlock()
	if got := subtitle(); got == before {
		t.Errorf("the subtitle still reads %q after the hold expired and a new puzzle was played", got)
	}
}
