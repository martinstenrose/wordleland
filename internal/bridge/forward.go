// Package bridge connects a Signal group to the result store.
//
// It subscribes to signal-cli-rest-api, keeps only Wordle results posted in
// the configured group, and files each one. It runs inside the application
// rather than beside it: since the two were merged there is no network hop,
// no token, and no second process to keep alive.
//
// It stays its own package because what it does — read a chat, decide what
// is a result — has nothing to do with serving a board, and because the
// moment a second bridge exists this is the shape to copy.
package bridge

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// SourceSignal names this bridge in player_identities and in the audit
// trail. One constant so the stored value and the logged one cannot drift.
const SourceSignal = "signal"

// Deliverer files one parsed result. The filer does not know how: it
// posted over HTTP when it was its own service, and calls ingest directly
// now. Kept as a function so a test can watch what it was handed without
// standing up a database.
type Deliverer func(context.Context, ingest.Submission) (ingest.Result, error)

// Announcer checks whether a month's scheduled noon post was missed and
// posts its winner if so. It is called after every live result, but is a
// no-op before noon on the first and after a successful send. The stored
// announcement record makes repeated checks restart-safe.
//
// A nil Announcer means announcing is off — unconfigured, or disabled by
// SIGNAL_ANNOUNCE_MONTHS — and is never called.
//
// An error is a genuine failure — the store, or the send, went wrong — not
// "nothing to announce yet", which the Announcer reports by returning nil.
// The filer logs it and moves on: see maybeAnnounce.
type Announcer func(ctx context.Context, now time.Time) error

// announceTimeout bounds the Announcer's own work — a database read and one
// HTTP call to signal-cli-rest-api — so a slow month check cannot stall the
// worker that also files results. Independent of sendTimeout: this also
// covers the store reads around the send.
const announceTimeout = 20 * time.Second

// Back-dating window, in puzzles either side of today's.
//
// Explicitly labeled Archive shares are rejected before this window. The
// puzzle-number check is the fallback for other back-dated results. Those
// were deliberately excluded from imported history, and forwarding them
// would let them back in by another door: an existing row is protected by
// the precedence rule, but an old result for a puzzle the player has no row
// for inserts cleanly.
//
// The check lives here rather than in ingest because ingest is
// source-agnostic and the CLI legitimately writes old puzzles.
const (
	// maxPuzzlesBehind allows a result posted late in the evening, or from
	// a player whose timezone is still on yesterday. Either is one puzzle
	// behind; neither needs more.
	//
	// Two would also cover someone in a trailing timezone posting after
	// their own midnight — both slips at once. That is not a case worth
	// widening the window for: the point is to post on the day, and in
	// practice late has meant a minute or two, never a day and a half. A
	// window that accommodates it is a window that also lets a genuinely
	// back-dated result in through the same door.
	maxPuzzlesBehind = 1
	// maxPuzzlesAhead allows a timezone that has already rolled over.
	maxPuzzlesAhead = 1
)

// Retry policy for a failed write. signal-cli has already acknowledged the
// message to Signal by the time it reaches us, so nothing will redeliver
// it: a result dropped here is lost.
const (
	maxAttempts    = 6
	baseRetryDelay = time.Second
	maxRetryDelay  = 30 * time.Second
)

// filer turns messages into ingest calls.
type filer struct {
	groupID string
	deliver Deliverer
	logger  *slog.Logger
	// health is told whether results are landing, so the diagnostics cover
	// delivery and not only the Signal side.
	health *health

	// announce checks for a month to post about, after every live result.
	// Nil when announcing is off.
	announce Announcer

	// now is swapped in tests so the back-dating window can be exercised
	// without waiting for the calendar.
	now func() time.Time
	// sleep is swapped in tests so retry backoff costs no real time.
	sleep func(context.Context, time.Duration)
}

func newFiler(groupID string, deliver Deliverer, announce Announcer, logger *slog.Logger, h *health) *filer {
	if h == nil {
		h = newHealth(time.Now)
	}
	return &filer{
		groupID: groupID, deliver: deliver, announce: announce, logger: logger, health: h,
		now:   time.Now,
		sleep: sleepContext,
	}
}

// handle processes one message. It returns nothing: every outcome is either
// normal or already logged, and there is no caller who could do better.
func (f *filer) handle(ctx context.Context, m Message) {
	// The group id that arrives on a message is the bare base64 form, which
	// /v1/groups reports as internal_id. The same endpoint also reports a
	// "group.<base64>" id, and configuring that one matches nothing while
	// looking exactly like a bot that is simply not receiving. The
	// config layer rejects that form at boot for the same reason.
	if m.GroupID != f.groupID {
		// The single most useful line for telling "wrong group configured"
		// apart from "bot not receiving at all" — both ids are opaque
		// base64, safe to log in full.
		f.logger.Debug("message is for a different group; ignoring",
			"received_group", m.GroupID, "configured_group", f.groupID)
		return
	}

	result, ok := wordle.Parse(m.Body)
	if !ok {
		// Most traffic in the group is ordinary conversation. The body
		// itself is never logged — only its shape — so this line
		// distinguishes a quiet group from a broken parser without
		// recording anyone's words.
		f.logger.Debug("message did not parse as a result",
			"sender", m.SenderUUID, "length", len(m.Body))
		return
	}
	if isArchiveShare(m.Body) {
		// A recently archived puzzle may still fall inside the small timezone
		// and late-post window below. Wordle labels Archive shares explicitly,
		// so reject that stronger signal regardless of the puzzle number.
		f.logger.Info("ignoring an archive result", "puzzle", result.PuzzleNo)
		return
	}

	if !f.withinWindow(result.PuzzleNo) {
		// Logged at info rather than dropped silently, so old posts are
		// visible as a thing that happened rather than a mystery. No
		// identifying field: the puzzle number is enough to recognise it.
		//
		// Not passed to maybeAnnounce either: a back-dated result is not
		// evidence that time has moved into a new month, only that someone
		// posted an old puzzle, which can happen at any point in the
		// current one.
		f.logger.Info("ignoring a back-dated result",
			"puzzle", result.PuzzleNo, "current", f.currentPuzzle())
		return
	}

	f.file(ctx, result, m)

	// After, not before: filing the actual result takes priority over a
	// once-a-month side effect, and a slow or failing announcement must
	// never delay or cost a score.
	f.maybeAnnounce(ctx)
}

func isArchiveShare(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "archive ") {
			return true
		}
	}
	return false
}

// maybeAnnounce runs the Announcer, if one is configured, and only ever
// logs what it reports. A failure here — the store, or the send to Signal
// — is real, but it is not a result: retrying happens on its own, because
// nothing here marks the month done until a post actually lands, so the
// next live message tries again.
func (f *filer) maybeAnnounce(ctx context.Context) {
	if f.announce == nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, announceTimeout)
	defer cancel()
	if err := f.announce(actx, f.now()); err != nil {
		f.logger.Warn("could not announce the month's winner; will retry on the next live message",
			"error", err)
	}
}

// withinWindow reports whether a puzzle is close enough to today's to be a
// live result rather than another kind of back-dated one.
func (f *filer) withinWindow(puzzleNo int) bool {
	current := f.currentPuzzle()
	return puzzleNo >= current-maxPuzzlesBehind && puzzleNo <= current+maxPuzzlesAhead
}

func (f *filer) currentPuzzle() int {
	return wordle.PuzzleForDate(f.now())
}

// post delivers a result, retrying only what retrying can fix.
func (f *filer) file(ctx context.Context, result wordle.Result, m Message) {
	sub := ingest.Submission{
		Source:      SourceSignal,
		ExternalID:  m.SenderUUID,
		DisplayHint: m.SenderName,
		PuzzleNo:    result.PuzzleNo,
		Solved:      result.Solved,
		HardMode:    result.HardMode,
		Via:         SourceSignal,
	}
	if result.Solved {
		guesses := result.Guesses
		sub.Guesses = &guesses
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		delivered, err := f.deliver(ctx, sub)
		if ctx.Err() != nil {
			return
		}

		var invalid *ingest.ValidationError
		switch {
		case errors.As(err, &invalid):
			// The parser produced something ingest will never accept.
			// Retrying cannot fix it, and dropping it silently would hide a
			// bug in the one component whose job is reading these messages.
			f.health.deliveryDropped()
			f.logger.Error("a parsed result was rejected as invalid; this is a parser bug",
				"puzzle", result.PuzzleNo, "error", err)
			return

		case err != nil:
			// In process this is the database being busy or broken rather
			// than a network fault, but the answer is the same: wait and
			// try again, because the alternative is losing the score.
			f.health.deliveryFailed()
			f.logger.Warn("filing a result failed, will retry",
				"puzzle", result.PuzzleNo, "attempt", attempt, "error", err)

		case delivered.Status == ingest.StatusPending:
			// The sender has no claimed identity yet, so there is no player
			// to name — `wordleland identity pending` already lists exactly
			// who is waiting, from the database. Normal, not an error.
			f.health.deliverySucceeded()
			f.logger.Info("result held for an unclaimed sender; "+
				"run 'wordleland identity pending' to see who and claim them",
				"puzzle", result.PuzzleNo)
			return

		case delivered.Status == ingest.StatusIgnored:
			// A human-entered value beat this one on precedence: not an
			// error, and not the common case either, so it earns a line
			// only at debug — an operator diagnosing "why didn't my score
			// change" needs it, nobody watching the log at info does.
			f.health.deliverySucceeded()
			f.logger.Debug("result was ignored; an existing value takes precedence",
				"puzzle", result.PuzzleNo, "player_id", delivered.PlayerID, "slug", delivered.Slug)
			return

		default:
			// The line that would have made the outage this feature exists
			// for visible in real time: at info, because a quiet bridge and
			// a working one otherwise look identical from the log alone.
			//
			// player_id and slug both appear because the slug is what an
			// admin reading the log actually recognises, and the id is what
			// survives a rename — see docs/decisions.md, "Logging".
			f.health.deliverySucceeded()
			f.logger.Info("filed a result", "puzzle", result.PuzzleNo, "status", delivered.Status,
				"player_id", delivered.PlayerID, "slug", delivered.Slug)
			return
		}

		if attempt < maxAttempts {
			f.sleep(ctx, retryDelay(attempt))
		}
	}

	// Nothing will redeliver this: signal-cli acknowledged it to Signal
	// before it reached us.
	f.health.deliveryDropped()
	f.logger.Error("giving up on a result after repeated failures; it is lost, "+
		"and can be entered by hand with 'wordleland results set'",
		"puzzle", result.PuzzleNo, "attempts", maxAttempts)
}

func retryDelay(attempt int) time.Duration {
	delay := baseRetryDelay << (attempt - 1)
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	jitter := time.Duration(rand.Int63n(int64(delay / 2)))
	return delay/2 + jitter
}

// sleepContext waits, or returns early if the context is cancelled.
func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
