package store

import (
	"context"
	"errors"
	"testing"
)

func TestCorrectOwnResult(t *testing.T) {
	t.Parallel()

	db, playerID, _, actor := resultsFixture(t)
	ctx := context.Background()
	userID := seedUser(t, db, "martin@example.tld", false)
	if _, err := LinkPlayer(ctx, db, actor, playerID, &userID); err != nil {
		t.Fatalf("LinkPlayer() failed: %v", err)
	}
	other, err := CreatePlayer(ctx, db, actor, "Ada", "ada")
	if err != nil {
		t.Fatalf("CreatePlayer() failed: %v", err)
	}
	for _, id := range []int64{playerID, other.ID} {
		if _, _, err := UpsertResult(ctx, db, sampleResult(id, 1890, 4), nil, nil); err != nil {
			t.Fatalf("UpsertResult() failed: %v", err)
		}
	}

	three := Result{PuzzleNo: 1890, Guesses: ptr(3), Solved: true, HardMode: true}

	t.Run("own result", func(t *testing.T) {
		// Not parallel: the subtests share one database, in order.
		player, err := CorrectOwnResult(ctx, db, userID, three, "typed 4 by mistake")
		if err != nil {
			t.Fatalf("CorrectOwnResult() failed: %v", err)
		}
		if player.ID != playerID {
			t.Fatalf("corrected player %d, want %d", player.ID, playerID)
		}
		got, err := ResultFor(ctx, db, 1890, playerID)
		if err != nil {
			t.Fatalf("ResultFor() failed: %v", err)
		}
		if *got.Guesses != 3 || !got.HardMode || got.EnteredBy == nil || *got.EnteredBy != userID {
			t.Fatalf("result after correction = %+v, want a 3 in hard mode entered by %d", got, userID)
		}

		// The bridge cannot put the old score back.
		outcome, _, err := UpsertResult(ctx, db, sampleResult(playerID, 1890, 4), nil, nil)
		if err != nil || outcome != OutcomeIgnored {
			t.Fatalf("bridge write after correction = %v, %v; want ignored", outcome, err)
		}
	})

	t.Run("unchanged is refused", func(t *testing.T) {
		if _, err := CorrectOwnResult(ctx, db, userID, three, ""); !errors.Is(err, ErrResultUnchanged) {
			t.Fatalf("CorrectOwnResult() = %v, want ErrResultUnchanged", err)
		}
	})

	t.Run("no result to correct", func(t *testing.T) {
		r := Result{PuzzleNo: 1891, Guesses: ptr(2), Solved: true}
		if _, err := CorrectOwnResult(ctx, db, userID, r, ""); !errors.Is(err, ErrResultNotFound) {
			t.Fatalf("CorrectOwnResult() = %v, want ErrResultNotFound", err)
		}
	})

	t.Run("unlinked login", func(t *testing.T) {
		stranger := seedUser(t, db, "stranger@example.tld", false)
		if _, err := CorrectOwnResult(ctx, db, stranger, three, ""); !errors.Is(err, ErrPlayerNotFound) {
			t.Fatalf("CorrectOwnResult() = %v, want ErrPlayerNotFound", err)
		}
	})

	t.Run("trail", func(t *testing.T) {
		got, err := Corrections(ctx, db, 1890)
		if err != nil {
			t.Fatalf("Corrections() failed: %v", err)
		}
		if len(got[other.ID]) != 0 {
			t.Fatalf("corrections for the other player = %+v, want none", got[other.ID])
		}
		list := got[playerID]
		if len(list) != 1 {
			t.Fatalf("corrections = %+v, want one", list)
		}
		c := list[0]
		want := Correction{
			PlayerID: playerID, PuzzleNo: 1890, At: c.At,
			From:   Score{Solved: true, Guesses: 4},
			To:     Score{Solved: true, Guesses: 3, HardMode: true},
			Reason: "typed 4 by mistake",
		}
		if c != want {
			t.Fatalf("correction = %+v, want %+v", c, want)
		}

		// An admin's correction is the admin's log's, not the group's.
		if _, _, err := UpsertResult(ctx, db, sampleResult(other.ID, 1890, 5), ptr(int64(1)), nil); err != nil {
			t.Fatalf("UpsertResult() failed: %v", err)
		}
		if err := LogResultActivity(ctx, db, actor, ActionResultUpdated, other.ID,
			sampleResult(other.ID, 1890, 5), &Result{Guesses: ptr(4), Solved: true}); err != nil {
			t.Fatalf("LogResultActivity() failed: %v", err)
		}
		got, err = Corrections(ctx, db, 1890)
		if err != nil {
			t.Fatalf("Corrections() failed: %v", err)
		}
		if len(got[other.ID]) != 0 {
			t.Fatalf("an admin correction shows as a self-correction: %+v", got[other.ID])
		}
	})
}
