package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ViaSelf marks, in the activity detail, a result a player corrected
// themselves from the browser.
const ViaSelf = "self"

// MaxCorrectionReason caps the note a player may leave with a correction.
const MaxCorrectionReason = 200

// ErrResultUnchanged is a correction that would write what is already there.
// Refused rather than logged, so the trail holds only real changes.
var ErrResultUnchanged = errors.New("result unchanged")

// CorrectOwnResult lets the login linked to a player change one of that
// player's existing results, and records that it did.
//
// The player is resolved from the login inside the transaction rather than
// passed in: players.user_id is the whole grant, and an admin unlinking the
// login a moment earlier must win. Only an existing result can be
// corrected — filing a new one from the browser is still not built (see
// docs/decisions.md) — and the correction is written as a human entry, so
// the Signal bridge cannot quietly put the old score back.
//
// The trail is the activity log: the entry carries the previous value, the
// reason given, and via "self", and Corrections reads it back for the pages
// everyone sees.
func CorrectOwnResult(ctx context.Context, db *sql.DB, userID int64, r Result, reason string) (Player, error) {
	var player Player
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		var err error
		player, err = PlayerByUserID(ctx, tx, userID)
		if err != nil {
			return err
		}
		r.PlayerID = player.ID

		previous, err := resultFor(ctx, tx, r.PuzzleNo, player.ID)
		if err != nil {
			return err
		}
		if sameScore(*previous, r) {
			return ErrResultUnchanged
		}
		r.Date = previous.Date

		if _, _, err := UpsertResult(ctx, tx, r, &userID, nil); err != nil {
			return err
		}
		detail := activityDetailFor(r, previous)
		detail["via"] = ViaSelf
		if reason != "" {
			detail["reason"] = reason
		}
		return LogActivity(ctx, tx, PlayerActor(userID), ActionResultUpdated, SubjectResult, &player.ID, detail)
	})
	return player, err
}

func sameScore(a, b Result) bool {
	return a.Solved == b.Solved && a.HardMode == b.HardMode &&
		(a.Guesses == nil) == (b.Guesses == nil) &&
		(a.Guesses == nil || *a.Guesses == *b.Guesses)
}

// Correction is one change a player made to their own result.
type Correction struct {
	PlayerID int64
	PuzzleNo int
	At       time.Time
	From, To Score
	Reason   string
}

// Score is a result's value, without who or when.
type Score struct {
	Solved   bool
	Guesses  int
	HardMode bool
}

// Corrections returns the self-corrections made to one puzzle's results,
// oldest first, keyed by player.
//
// Read from the activity log, which already holds every one with its
// previous value, rather than from a second copy that could disagree with
// it. Admin corrections are not included: they are the admin's job and
// already in the admin's log; what the group is owed a view of is a player
// changing their own score.
func Corrections(ctx context.Context, q Querier, puzzleNo int) (map[int64][]Correction, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT subject_id, at, detail FROM activity_log
		WHERE action = ? AND subject_type = ? AND actor_kind = ?
		  AND json_extract(detail, '$.via') = ?
		  AND json_extract(detail, '$.puzzle_no') = ?
		ORDER BY at, id`,
		ActionResultUpdated, SubjectResult, ActorPlayer, ViaSelf, puzzleNo)
	if err != nil {
		return nil, fmt.Errorf("read corrections: %w", err)
	}
	defer rows.Close()

	out := map[int64][]Correction{}
	for rows.Next() {
		var (
			c      Correction
			detail string
		)
		if err := rows.Scan(&c.PlayerID, &c.At, &detail); err != nil {
			return nil, fmt.Errorf("scan correction: %w", err)
		}
		var d struct {
			Solved   bool   `json:"solved"`
			Guesses  int    `json:"guesses"`
			HardMode bool   `json:"hard_mode"`
			Reason   string `json:"reason"`
			Previous struct {
				Solved   bool `json:"solved"`
				Guesses  int  `json:"guesses"`
				HardMode bool `json:"hard_mode"`
			} `json:"previous"`
		}
		if err := json.Unmarshal([]byte(detail), &d); err != nil {
			return nil, fmt.Errorf("decode correction: %w", err)
		}
		c.PuzzleNo = puzzleNo
		c.To = Score{Solved: d.Solved, Guesses: d.Guesses, HardMode: d.HardMode}
		c.From = Score{Solved: d.Previous.Solved, Guesses: d.Previous.Guesses, HardMode: d.Previous.HardMode}
		c.Reason = d.Reason
		out[c.PlayerID] = append(out[c.PlayerID], c)
	}
	return out, rows.Err()
}
