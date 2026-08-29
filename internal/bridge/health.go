package bridge

import (
	"fmt"
	"sync"
	"time"
)

// Thresholds for reporting unhealthy.
//
// Both exist because a wedged bridge is silent data loss rather than a
// visible failure: signal-cli-rest-api drops messages that arrive while no
// websocket is attached, and reconnect backoff caps at a minute and then
// retries for ever. The container stays up and looks fine throughout.
const (
	// maxDisconnected distinguishes an ordinary blip — a container restart
	// upstream, a network hiccup — from a connection that is not coming
	// back. Reconnect backoff tops out at 60s, so anything past a few
	// minutes means repeated failures rather than one.
	maxDisconnected = 5 * time.Minute

	// maxSilence catches the harder failure: a socket that is open and
	// answering pings but delivers nothing. The group posts daily, so a
	// silence this long is wrong even though everything looks connected.
	// Long enough to sit out a quiet weekend.
	maxSilence = 36 * time.Hour

	// maxSilenceBeforeFirst is the same question asked of a bridge that has
	// never received anything at all, and it is shorter because the two are
	// not equally suspicious.
	//
	// A long quiet spell on a bridge that has been delivering is evidence
	// about the group. Silence on one that has delivered nothing since it
	// connected is evidence about nothing: there is no proof the
	// subscription works at all, and the ping handler keeps the read
	// deadline alive whether it does or not.
	//
	// Short for that reason. Frames are not only results — typing
	// indicators and receipts arrive too — so an active group produces
	// something within minutes. Six hours sits out a night with nobody
	// awake and still catches a bridge that came up wrong before the
	// morning's scores are lost.
	maxSilenceBeforeFirst = 6 * time.Hour

	// maxDeliveryFailure is how long filing a result may keep
	// failing before the bridge calls itself unhealthy.
	//
	// Watching only the Signal side was a real gap: a bridge that
	// receives perfectly and cannot write to the store loses every result
	// it handles, and used to report itself green throughout. A misconfigured
	// network is exactly that shape — both containers up, one unable to
	// resolve the other.
	//
	// Longer than one result's retry budget (about half a minute), so an
	// ordinary restart does not raise an alarm, and short
	// enough that a broken link is caught before the next day's results.
	maxDeliveryFailure = 2 * time.Minute
)

// health tracks enough to answer a probe, and no more. It exists for
// something to fail on, not as a status page.
type health struct {
	mu sync.Mutex

	connectedNow bool
	// since is when the current connected/disconnected state began.
	since time.Time
	// lastMessage is when a frame last arrived, zero until the first one.
	lastMessage time.Time

	// verification is what signal-cli confirmed about the configuration,
	// and account/group are what was configured. Held here because this is
	// already the thing the handlers read under a lock.
	verification Verification
	account      string
	group        string

	// deliveryFailingSince is when filing results started
	// failing, zero while it is working.
	deliveryFailingSince time.Time
	// dropped counts results abandoned since the last successful delivery.
	// Any value above zero means data was lost, which stays reportable
	// until something gets through again.
	dropped int

	now func() time.Time
}

// deliverySucceeded records a result reaching the store, and clears
// everything the failure path accumulated.
func (h *health) deliverySucceeded() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deliveryFailingSince = time.Time{}
	h.dropped = 0
}

// deliveryFailed records an attempt that did not reach the store.
//
// Only the first failure in a run sets the clock, so the reported duration
// is how long delivery has been broken rather than how long ago the last
// attempt was.
func (h *health) deliveryFailed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.deliveryFailingSince.IsZero() {
		h.deliveryFailingSince = h.now()
	}
}

// deliveryDropped records a result abandoned after its retries ran out.
//
// This is data loss rather than a warning about it: signal-cli acknowledged
// the message to Signal before it reached us, so nothing will redeliver it.
func (h *health) deliveryDropped() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropped++
}

func newHealth(now func() time.Time) *health {
	return &health{since: now(), now: now}
}

func (h *health) connected() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connectedNow = true
	h.since = h.now()
}

func (h *health) disconnected() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connectedNow = false
	h.since = h.now()
}

// describe records what the bridge was configured to watch.
func (h *health) describe(account, group string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.account, h.group = account, group
}

// verified records the outcome of asking signal-cli about the config.
func (h *health) verified(v Verification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.verification = v
}

// received records a frame of any kind, including ones the bridge
// ignores. The point is that the subscription is alive, not that the
// message was interesting.
func (h *health) received() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastMessage = h.now()
}

// status reports whether the bridge is healthy, and why not when it is
// not.
func (h *health) status() (ok bool, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := h.now()

	if !h.connectedNow {
		if down := now.Sub(h.since); down > maxDisconnected {
			return false, fmt.Sprintf("disconnected from signal for %s", down.Round(time.Second))
		}
		// A short gap is ordinary: reconnection is in progress.
		return true, "reconnecting"
	}

	// Delivery is checked before silence: a bridge that cannot file what it
	// receives is losing results now, which matters more than how long the
	// group has been quiet.
	if h.dropped > 0 {
		return false, fmt.Sprintf("%d result(s) lost — could not be filed", h.dropped)
	}
	if !h.deliveryFailingSince.IsZero() {
		if failing := now.Sub(h.deliveryFailingSince); failing > maxDeliveryFailure {
			return false, fmt.Sprintf("cannot file results, failing for %s",
				failing.Round(time.Second))
		}
	}

	// Silence is measured from the last evidence the subscription works: a
	// message if one has ever arrived, and otherwise the moment we
	// connected.
	//
	// It used to be skipped entirely until the first message, reasoning
	// that a fresh deploy has no baseline. That was half right — a deploy
	// should not look broken immediately — and the consequence was that a
	// bridge which connects and never receives anything reported healthy
	// for ever. Not hypothetical: it ran eight hours against a wrongly
	// configured account, green throughout, and was found because somebody
	// noticed a missing score.
	//
	// Having no baseline is not a reason to stop asking. It is a reason to
	// wait longer before answering, which is what the two limits do.
	quiet, limit := now.Sub(h.lastMessage), maxSilence
	if h.lastMessage.IsZero() {
		quiet, limit = now.Sub(h.since), maxSilenceBeforeFirst
	}
	if quiet > limit {
		if h.lastMessage.IsZero() {
			return false, fmt.Sprintf(
				"connected for %s and nothing has ever arrived; check the configuration",
				quiet.Round(time.Minute))
		}
		return false, fmt.Sprintf("connected but nothing received for %s",
			quiet.Round(time.Minute))
	}
	return true, "connected"
}

// handler serves the probe endpoint.

// Status is what the bridge is doing, for the diagnostics page and for the
// liveness probe.
//
// Freshness first, deliberately. The failure that costs scores is not a
// dropped connection — that is loud and self-healing — it is a socket that
// is open and delivering nothing because the group id is wrong. A
// connection indicator is green throughout that; "nothing for two days" is
// not.
type Status struct {
	Connected bool
	// Since is when the current connected state began.
	Since time.Time
	// LastMessage is when a frame last arrived, zero until the first.
	LastMessage time.Time
	// DeliveryFailingSince is zero while results are landing.
	DeliveryFailingSince time.Time
	// Dropped counts results abandoned since the last that landed. Above
	// zero means data was lost.
	Dropped int

	// Verification is what signal-cli confirmed about the configuration.
	// A connected bridge with a failed verification is the shape of every
	// silent misconfiguration: it works perfectly and matches nothing.
	Verification Verification

	// Account and Group are the configuration as the bridge received it,
	// for a reader who needs to compare against signal-cli by eye.
	Account string
	Group   string

	// OK and Reason summarise the above for a reader. OK false is a warning
	// worth showing, not a reason to restart anything: see Alive.
	OK     bool
	Reason string
}

func (h *health) snapshot() Status {
	ok, reason := h.status()

	h.mu.Lock()
	defer h.mu.Unlock()
	return Status{
		Connected:            h.connectedNow,
		Since:                h.since,
		LastMessage:          h.lastMessage,
		DeliveryFailingSince: h.deliveryFailingSince,
		Dropped:              h.dropped,
		Verification:         h.verification,
		Account:              h.account,
		Group:                h.group,
		OK:                   ok,
		Reason:               reason,
	}
}
