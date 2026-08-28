package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// reply is one canned outcome from the delivery seam, standing in for what
// ingest would have returned.
type reply struct {
	status ingest.Status
	err    error
}

// These name the outcomes the old HTTP harness expressed as status codes,
// so the tests below read the same way they did before delivery moved
// in-process.
var (
	filed     = reply{status: ingest.StatusCreated}
	updated   = reply{status: ingest.StatusUpdated}
	held      = reply{status: ingest.StatusPending}
	transient = reply{err: errors.New("database is locked")}
	unusable  = reply{err: &ingest.ValidationError{}}
)

// capture records what a filer filed and what it logged.
type capture struct {
	mu   sync.Mutex
	subs []ingest.Submission
	logs *bytes.Buffer
}

// testFiler wires a filer to a delivery seam returning replies in
// order, with a clock fixed to a known puzzle and no real sleeping.
func testFiler(t *testing.T, replies ...reply) (*filer, *capture) {
	t.Helper()

	cap := &capture{logs: &bytes.Buffer{}}
	var calls atomic.Int32

	deliver := func(_ context.Context, sub ingest.Submission) (ingest.Status, error) {
		n := int(calls.Add(1)) - 1
		r := filed
		if n < len(replies) {
			r = replies[n]
		} else if len(replies) > 0 {
			r = replies[len(replies)-1]
		}

		cap.mu.Lock()
		cap.subs = append(cap.subs, sub)
		cap.mu.Unlock()

		return r.status, r.err
	}

	logger := slog.New(slog.NewTextHandler(cap.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := newFiler(testGroupID, deliver, nil, logger, nil)

	// A fixed "today" so the back-dating window is deterministic.
	today, err := wordle.DateForPuzzle(1891)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}
	f.now = func() time.Time { return today }
	f.sleep = func(context.Context, time.Duration) {} // no real backoff in tests

	return f, cap
}

func (c *capture) sent() []ingest.Submission {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ingest.Submission(nil), c.subs...)
}

func (c *capture) log() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logs.String()
}

func msg(body string) Message {
	return Message{SenderUUID: testUUID, SenderName: testName, GroupID: testGroupID, Body: body}
}

// The body must match exactly, including hard mode.
func TestForwardsAResult(t *testing.T) {
	f, cap := testFiler(t)

	f.handle(context.Background(), msg("Wordle 1 891 3/6*"))

	sent := cap.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(sent))
	}
	got := sent[0]
	if got.Source != "signal" {
		t.Errorf("source = %q, want signal", got.Source)
	}
	if got.ExternalID != testUUID {
		t.Errorf("external_id = %q, want the sender uuid", got.ExternalID)
	}
	if got.DisplayHint != testName {
		t.Errorf("display_hint = %q, want the profile name", got.DisplayHint)
	}
	if got.PuzzleNo != 1891 || !got.Solved || got.Guesses == nil || *got.Guesses != 3 {
		t.Errorf("result = %+v, want puzzle 1891 solved in 3", got)
	}
	if !got.HardMode {
		t.Error("hard_mode was not carried through")
	}
}

func TestForwardsAFailure(t *testing.T) {
	f, cap := testFiler(t)

	f.handle(context.Background(), msg("Wordle 1 891 X/6"))

	sent := cap.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(sent))
	}
	if sent[0].Solved {
		t.Error("solved = true for an X/6")
	}
	// The "7" convention stays out of storage: a failure carries no
	// guess count at all.
	if sent[0].Guesses != nil {
		t.Errorf("guesses = %v for a failure, want it omitted", *sent[0].Guesses)
	}
}

// The group id that arrives on messages is the bare base64 internal_id. The
// "group.<base64>" form the same endpoint reports as id matches nothing,
// and the failure is invisible in production.
func TestGroupFiltering(t *testing.T) {
	tests := []struct {
		name    string
		groupID string
		want    int
	}{
		{"the configured group", testGroupID, 1},
		{"another group", "b3RoZXItZ3JvdXAtaWQtdmFsdWU=", 0},
		{"no group", "", 0},
		{"the group.<base64> form", "group." + testGroupID, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, cap := testFiler(t)
			m := msg("Wordle 1 891 3/6")
			m.GroupID = tt.groupID

			f.handle(context.Background(), m)

			if got := len(cap.sent()); got != tt.want {
				t.Errorf("sent %d requests, want %d", got, tt.want)
			}
		})
	}
}

func TestIgnoresOrdinaryConversation(t *testing.T) {
	f, cap := testFiler(t)

	for _, body := range []string{
		"anyone else find that brutal",
		"🟩🟩🟩🟩🟩",
		"",
	} {
		f.handle(context.Background(), msg(body))
	}
	if got := len(cap.sent()); got != 0 {
		t.Errorf("sent %d requests for non-results, want 0", got)
	}
}

// Archive posts parse exactly like live ones. Back-dating was excluded from
// the imported history deliberately, and an archive result for a puzzle the
// player has no row for would insert cleanly.
func TestRejectsBackDatedResults(t *testing.T) {
	tests := []struct {
		name   string
		puzzle int
		want   int
	}{
		{"today", 1891, 1},
		{"yesterday, posted after midnight", 1890, 1},
		{"two days back, a late catch-up", 1889, 1},
		{"one ahead, a timezone that has rolled over", 1892, 1},

		{"three days back", 1888, 0},
		{"an archive puzzle from February", 1707, 0},
		{"the very first puzzle", 1, 0},
		{"implausibly far ahead", 2000, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, cap := testFiler(t)

			f.handle(context.Background(), msg(sprintPuzzle(tt.puzzle)))

			if got := len(cap.sent()); got != tt.want {
				t.Errorf("sent %d requests, want %d", got, tt.want)
			}
			if tt.want == 0 && !strings.Contains(cap.log(), "back-dated") {
				t.Errorf("a dropped archive result was not logged:\n%s", cap.log())
			}
		})
	}
}

// Puzzle distance alone cannot identify an Archive result for yesterday.
// Wordle's explicit Archive heading must win even while that puzzle remains
// inside the normal late-post window, and the rejected result must not act as
// the fallback trigger for a missed monthly announcement.
func TestRejectsARecentArchiveResult(t *testing.T) {
	f, cap := testFiler(t)

	var announceCalls atomic.Int32
	f.announce = func(context.Context, time.Time) error {
		announceCalls.Add(1)
		return nil
	}

	body := "Archive August 31, 2026\nWordle 1 890 3/6"
	f.handle(context.Background(), msg(body))

	if got := len(cap.sent()); got != 0 {
		t.Errorf("sent %d results for a recent Archive share, want 0", got)
	}
	if announceCalls.Load() != 0 {
		t.Error("a recent Archive share triggered the monthly announcement")
	}
	if !strings.Contains(cap.log(), "ignoring an archive result") {
		t.Errorf("the rejected Archive share was not logged:\n%s", cap.log())
	}
}

func sprintPuzzle(n int) string {
	return "Wordle " + itoaTest(n) + " 3/6"
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// The forms that must keep failing to parse. Both appear in the group, and
// both would be wrong to forward.
func TestFormsThatMustNotParse(t *testing.T) {
	tests := map[string]string{
		"a custom puzzle":       "Wordle puzzle created by Sample 3/6*",
		"a custom puzzle, grid": "Wordle puzzle created by Sample 4/6\n\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩",
		"the Swedish clone":     "Ordell 412 3/6\n\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩",
		"another clone":         "Quordle 1234 5/6",
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			f, cap := testFiler(t)
			f.handle(context.Background(), msg(body))
			if got := len(cap.sent()); got != 0 {
				t.Errorf("forwarded %q, want it ignored", body)
			}
		})
	}
}

// The ingest outcome matrix, including which outcomes are retried.
func TestResponseHandling(t *testing.T) {
	tests := []struct {
		name      string
		replies   []reply
		wantCalls int
		wantLog   string
	}{
		{"filed", []reply{filed}, 1, ""},
		{"updated", []reply{updated}, 1, ""},
		{
			// Normal: the sender has no claimed identity, so the result is
			// held and replayed on claim.
			name: "held for an unclaimed sender", replies: []reply{held},
			wantCalls: 1, wantLog: "identity pending",
		},
		{
			// A parsed result ingest will never accept. Retrying cannot fix
			// it, so it is dropped loudly rather than spun on.
			name: "rejected as invalid", replies: []reply{unusable},
			wantCalls: 1, wantLog: "parser bug",
		},
		{
			name: "transient failure then success", replies: []reply{transient, filed},
			wantCalls: 2,
		},
		{
			name: "failing throughout", replies: []reply{transient},
			wantCalls: maxAttempts, wantLog: "it is lost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, cap := testFiler(t, tt.replies...)

			f.handle(context.Background(), msg("Wordle 1 891 3/6"))

			if got := len(cap.sent()); got != tt.wantCalls {
				t.Errorf("made %d calls, want %d", got, tt.wantCalls)
			}
			if tt.wantLog != "" && !strings.Contains(cap.log(), tt.wantLog) {
				t.Errorf("log does not mention %q:\n%s", tt.wantLog, cap.log())
			}
		})
	}
}

// A 202 is normal and must not be retried: retrying would multiply held
// results for every new player's first week.
func TestHeldResultIsNotRetried(t *testing.T) {
	f, cap := testFiler(t, held)

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))

	if got := len(cap.sent()); got != 1 {
		t.Errorf("made %d calls for a 202, want 1", got)
	}
}

// Logs must carry no identifying field: display names are Signal display
// names and the uuid is an account identifier (CLAUDE.md).
func TestLogsCarryNoIdentifyingFields(t *testing.T) {
	for _, replies := range [][]reply{
		{filed},
		{held},
		{unusable},
		{transient},
	} {
		f, cap := testFiler(t, replies...)
		f.handle(context.Background(), msg("Wordle 1 891 3/6*"))
		// And a dropped archive result, which also logs.
		f.handle(context.Background(), msg("Wordle 1707 2/6*"))

		log := cap.log()
		for _, secret := range []string{testUUID, testName} {
			if strings.Contains(log, secret) {
				t.Errorf("log leaks %q with replies %v:\n%s", secret, replies, log)
			}
		}
	}
}

// The reader must never wait on delivery. signal-cli-rest-api fans out with
// a non-blocking send, so a reader that pauses to work has messages dropped
// upstream with only a debug line to show for it.
func TestSlowDeliveryDoesNotBlockTheReader(t *testing.T) {
	release := make(chan struct{})
	var served atomic.Int32

	deliver := func(context.Context, ingest.Submission) (ingest.Status, error) {
		served.Add(1)
		<-release // hold the first write open
		return ingest.StatusCreated, nil
	}
	defer close(release)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := newFiler(testGroupID, deliver, nil, logger, nil)
	today, _ := wordle.DateForPuzzle(1891)
	f.now = func() time.Time { return today }
	f.sleep = func(context.Context, time.Duration) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := make(chan Message, queueSize)
	go func() {
		for m := range queue {
			f.handle(ctx, m)
		}
	}()

	// The source hands over messages while the worker is stuck on the first.
	source := newFakeSource(
		msg("Wordle 1 891 3/6"),
		msg("Wordle 1 890 4/6"),
		msg("Wordle 1 892 2/6"),
	)
	go source.Run(ctx, queue)

	select {
	case <-source.sent:
		// Every message was accepted even though delivery is blocked, which
		// is the property that keeps upstream from dropping them.
	case <-time.After(2 * time.Second):
		t.Fatal("the reader blocked behind a slow delivery")
	}
}

// The real separator is an NBSP: byte inspection of the export found
// 664 of them against 681 commas. It survives copy-paste as a plain space,
// so it is asserted here explicitly rather than trusted to a fixture typed
// by hand.
func TestForwardsRealSeparatorForms(t *testing.T) {
	tests := map[string]string{
		"non-breaking space": "Wordle 1\u00a0891 3/6*",
		"comma":              "Wordle 1,891 3/6*",
		"plain space":        "Wordle 1 891 3/6*",
		"no separator":       "Wordle 1891 3/6*",
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			f, cap := testFiler(t)
			f.handle(context.Background(), msg(body))

			sent := cap.sent()
			if len(sent) != 1 {
				t.Fatalf("sent %d requests for %q, want 1", len(sent), body)
			}
			if sent[0].PuzzleNo != 1891 {
				t.Errorf("puzzle = %d, want 1891", sent[0].PuzzleNo)
			}
			if !sent[0].HardMode {
				t.Error("hard mode was lost")
			}
		})
	}
}

// The full body as it arrives, grid and all.
func TestForwardsAMessageWithItsGrid(t *testing.T) {
	f, cap := testFiler(t)

	f.handle(context.Background(), msg("Wordle 1\u00a0891 3/6*\n\n\u2b1b\u2b1b\U0001f7e8\U0001f7e9\u2b1b\n\U0001f7e8\U0001f7e8\U0001f7e8\U0001f7e9\u2b1b\n\U0001f7e9\U0001f7e9\U0001f7e9\U0001f7e9\U0001f7e9"))

	sent := cap.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(sent))
	}
	if sent[0].PuzzleNo != 1891 || sent[0].Guesses == nil || *sent[0].Guesses != 3 || !sent[0].HardMode {
		t.Errorf("result = %+v, want puzzle 1891 solved in 3 in hard mode", sent[0])
	}
}

// The incident this reproduces: results could not be written, every one was
// lost, and the status stayed green because it only watched the Signal side.
// The cause was a missing network between two containers; in one process it
// would be a database that will not accept writes. Either way the filer
// must not report itself well while losing scores.
func TestFailingDeliveryTurnsTheStatusRed(t *testing.T) {
	c := newClock()
	h := newHealth(c.Now)
	h.connected()
	h.received()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deliver := func(context.Context, ingest.Submission) (ingest.Status, error) {
		return "", errors.New("database is locked")
	}
	f := newFiler(testGroupID, deliver, nil, logger, h)
	today, err := wordle.DateForPuzzle(1891)
	if err != nil {
		t.Fatalf("DateForPuzzle: %v", err)
	}
	f.now = func() time.Time { return today }
	f.sleep = func(context.Context, time.Duration) { c.advance(30 * time.Second) }

	if ok, _ := h.status(); !ok {
		t.Fatal("unhealthy before anything was attempted")
	}

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))

	ok, reason := h.status()
	if ok {
		t.Fatal("the probe stayed green while every result was being lost")
	}
	if !strings.Contains(reason, "lost") && !strings.Contains(reason, "deliver") {
		t.Errorf("reason = %q, want it to name the delivery failure", reason)
	}
}

// A result ingest refuses is not a delivery failure: the write path is
// working. The result is still lost, so the status reports that, but not as
// an inability to write.
func TestRejectedResultIsReportedAsLossNotFailure(t *testing.T) {
	c := newClock()
	h := newHealth(c.Now)
	h.connected()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deliver := func(context.Context, ingest.Submission) (ingest.Status, error) {
		return "", &ingest.ValidationError{}
	}
	f := newFiler(testGroupID, deliver, nil, logger, h)
	today, _ := wordle.DateForPuzzle(1891)
	f.now = func() time.Time { return today }
	f.sleep = func(context.Context, time.Duration) {}

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))

	ok, reason := h.status()
	if ok {
		t.Fatal("a dropped result left the probe green")
	}
	if strings.Contains(reason, "cannot deliver") {
		t.Errorf("reason = %q, want it to report loss rather than an inability to write", reason)
	}
}

// Recovery has to be automatic, or one bad minute leaves the status red for
// ever and the operator learns to ignore it.
func TestStatusRecoversAfterDeliveryResumes(t *testing.T) {
	c := newClock()
	h := newHealth(c.Now)
	h.connected()

	var fail atomic.Bool
	fail.Store(true)
	deliver := func(context.Context, ingest.Submission) (ingest.Status, error) {
		if fail.Load() {
			return "", errors.New("database is locked")
		}
		return ingest.StatusCreated, nil
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := newFiler(testGroupID, deliver, nil, logger, h)
	today, _ := wordle.DateForPuzzle(1891)
	f.now = func() time.Time { return today }
	f.sleep = func(context.Context, time.Duration) { c.advance(30 * time.Second) }

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))
	if ok, _ := h.status(); ok {
		t.Fatal("healthy while delivery was failing")
	}

	fail.Store(false)
	f.handle(context.Background(), msg("Wordle 1 892 4/6"))

	if ok, reason := h.status(); !ok {
		t.Errorf("still unhealthy after delivery resumed: %s", reason)
	}
}

// Every live result is a chance to notice a month has closed: see
// Announcer's comment on why "after every live result" is simpler than
// trying to recognise which one is the month's first.
func TestAnnouncerIsCalledAfterALiveResult(t *testing.T) {
	f, _ := testFiler(t)

	var calls atomic.Int32
	var gotNow time.Time
	f.announce = func(_ context.Context, now time.Time) error {
		calls.Add(1)
		gotNow = now
		return nil
	}

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))

	if calls.Load() != 1 {
		t.Fatalf("announce called %d times, want 1", calls.Load())
	}
	if !gotNow.Equal(f.now()) {
		t.Errorf("announce saw now = %v, want %v", gotNow, f.now())
	}
}

// A back-dated result is not evidence that time has moved into a new
// month — someone can post an old puzzle at any point in the current one —
// so it must not trigger the check.
func TestAnnouncerIsNotCalledForABackDatedResult(t *testing.T) {
	f, _ := testFiler(t)

	var calls atomic.Int32
	f.announce = func(context.Context, time.Time) error {
		calls.Add(1)
		return nil
	}

	f.handle(context.Background(), msg(sprintPuzzle(1707))) // an archive puzzle

	if calls.Load() != 0 {
		t.Errorf("announce was called for a back-dated result")
	}
}

// Ordinary conversation must not reach the announcer. In particular, the
// bot can receive its own sent announcement as a sync message; if recording
// failed after sending, treating that message as a trigger could immediately
// send another copy and repeat.
func TestAnnouncerIsNotCalledForOrdinaryConversation(t *testing.T) {
	f, _ := testFiler(t)

	var calls atomic.Int32
	f.announce = func(context.Context, time.Time) error {
		calls.Add(1)
		return nil
	}

	f.handle(context.Background(), msg("anyone else find that brutal"))

	if calls.Load() != 0 {
		t.Errorf("announce was called for a non-result message")
	}
}

// A nil Announcer is what "announcing is off" looks like — unconfigured, or
// SIGNAL_ANNOUNCE_MONTHS=false — and handling a live result must not panic
// on it.
func TestNilAnnouncerIsNeverCalled(t *testing.T) {
	f, _ := testFiler(t)
	f.announce = nil

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))
}

// The result the message actually carried must still be filed even when
// the announcer fails: a once-a-month side effect must never cost a score.
// The failure is logged, not swallowed silently, so an operator can see
// Signal is unreachable without it ever showing up as a lost result.
func TestAnnouncerFailureDoesNotAffectTheResult(t *testing.T) {
	f, cap := testFiler(t)

	f.announce = func(context.Context, time.Time) error {
		return errors.New("signal is unreachable")
	}

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))

	if len(cap.sent()) != 1 {
		t.Fatalf("sent %d results despite the announcer failing, want 1", len(cap.sent()))
	}
	if !strings.Contains(cap.log(), "could not announce") {
		t.Errorf("the announce failure was not logged:\n%s", cap.log())
	}
}

// maybeAnnounce bounds its own call, so a hung signal-cli-rest-api cannot
// stall the worker that also files results.
func TestAnnouncerContextCarriesADeadline(t *testing.T) {
	f, _ := testFiler(t)

	var hadDeadline bool
	f.announce = func(ctx context.Context, _ time.Time) error {
		_, hadDeadline = ctx.Deadline()
		return nil
	}

	f.handle(context.Background(), msg("Wordle 1 891 3/6"))

	if !hadDeadline {
		t.Error("the announcer's context had no deadline")
	}
}
