package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/martinstenrose/wordleland/internal/store"
)

// Live updates: a result that lands while a page is open appears on it.
//
// The stream carries no HTML. It carries a mark — store.LatestResultMark, the
// id of the last activity-log row that filed or changed a result — and a
// page that hears a new one fetches its own URL again and swaps its content
// in place (the attributes are on <main>, see base.html). So the fragment is
// rendered inside an ordinary request, with the reader's own language,
// share prefix and filter, by the one render path there is. Rendering it
// here instead would have meant one render per open page per event anyway,
// minus the request everything in newChrome needs.
//
// The server learns of a change by polling the mark, not from the bridge:
// results also land from the API, from a claim on the pending screen and
// from the CLI in another process, and every one of them moves the mark.
// The poll runs only while somebody is listening.

// liveHub is the set of open streams and the one poller that feeds them.
type liveHub struct {
	db     *sql.DB
	logger *slog.Logger

	// interval is how often the mark is read while a stream is open. Two
	// seconds is the latency a reader sees between a result being filed
	// and their page redrawing; one query on an indexed table is the cost.
	interval time.Duration
	// limit caps the open streams. Each is a goroutine and a connection
	// held open; a group of a dozen readers with a tab each is nowhere near
	// it, and a client that is refused backs off and tries again.
	limit int

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
	// mark is the last value the poller read, 0 until it has read one.
	mark    int64
	polling bool

	closed    chan struct{}
	closeOnce sync.Once
}

func newLiveHub(db *sql.DB, logger *slog.Logger) *liveHub {
	return &liveHub{
		db:       db,
		logger:   logger,
		interval: 2 * time.Second,
		limit:    64,
		subs:     make(map[chan struct{}]struct{}),
		closed:   make(chan struct{}),
	}
}

// subscribe registers a stream and starts the poller if it is the first.
// The channel is nudged, never sent a value: a subscriber that hears it
// reads the current mark from the hub. It reports false when the hub is
// full or closed.
func (h *liveHub) subscribe() (chan struct{}, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.closed:
		return nil, false
	default:
	}
	if len(h.subs) >= h.limit {
		return nil, false
	}
	ch := make(chan struct{}, 1)
	h.subs[ch] = struct{}{}
	if !h.polling {
		h.polling = true
		go h.poll()
	}
	return ch, true
}

func (h *liveHub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, ch)
}

// current is the last mark the poller read.
func (h *liveHub) current() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mark
}

// poll reads the mark on every tick and nudges every stream when it moves.
// It stops itself once nobody is listening, so an idle server runs no
// query at all.
func (h *liveHub) poll() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.closed:
			return
		case <-ticker.C:
		}

		ctx, cancel := context.WithTimeout(context.Background(), h.interval)
		mark, err := store.LatestResultMark(ctx, h.db)
		cancel()
		if err != nil {
			h.logger.Error("poll result mark", "error", err)
			continue
		}

		h.mu.Lock()
		if len(h.subs) == 0 {
			h.polling = false
			h.mu.Unlock()
			return
		}
		moved := mark != h.mark
		h.mark = mark
		if moved {
			for ch := range h.subs {
				select {
				case ch <- struct{}{}:
				default: // Already nudged and not yet read; one nudge is enough.
				}
			}
		}
		h.mu.Unlock()
	}
}

// Close ends every stream. Registered with the HTTP server's shutdown by
// cmd/wordleland: a stream is never idle, so without this Shutdown would
// wait its whole grace period for readers who are not going anywhere.
func (h *liveHub) Close() {
	h.closeOnce.Do(func() { close(h.closed) })
}

// liveView is what a page that subscribes carries: where its stream is and
// what to fetch when the stream says something changed.
type liveView struct {
	// Stream is the events URL with the mark this page was rendered at, so
	// a stream opened late — a page restored from the history cache, a
	// reconnect the extension makes from scratch — is caught up at once
	// when anything has landed in between.
	Stream string
	// Page is this page's own URL, path and query, which is what is fetched
	// again: the same prefix, the same filter, the reader's own cookies.
	Page string
}

// liveViewFor builds the subscription for the page being rendered, or nil
// when the mark cannot be read — the page is correct on load without it.
func (s *Server) liveViewFor(r *http.Request, prefix string) *liveView {
	mark, err := store.LatestResultMark(r.Context(), s.db)
	if err != nil {
		s.logger.Error("read result mark", "error", err)
		return nil
	}
	return &liveView{
		Stream: prefix + "/events?since=" + strconv.FormatInt(mark, 10),
		Page:   r.URL.RequestURI(),
	}
}

// liveHeartbeat keeps a quiet stream from being closed by a proxy that
// times out idle responses; a comment line is nothing to the client.
const liveHeartbeat = 25 * time.Second

// liveWriteGrace is how long a single write to the stream may take before
// the connection is given up on. The server's WriteTimeout would otherwise
// end every stream at thirty seconds, so the deadline is pushed forward on
// each write instead.
const liveWriteGrace = 30 * time.Second

// handleEvents is the stream. Behind requireAuth at /events and behind the
// share slug's check under the share prefix; both arrive here.
//
// The client sends the mark it last heard as Last-Event-ID on a browser
// reconnect, and the mark it was rendered at as ?since= on a fresh one;
// whichever it has is what it is caught up from.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if last, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil {
		since = last
	}

	ch, ok := s.live.subscribe()
	if !ok {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "too many open streams", http.StatusServiceUnavailable)
		return
	}
	defer s.live.unsubscribe(ch)

	rc := http.NewResponseController(w)
	write := func(format string, args ...any) bool {
		// A recorder in a test has no deadline to set; that is not a fault.
		if err := rc.SetWriteDeadline(time.Now().Add(liveWriteGrace)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	event := func(mark int64) bool {
		return write("id: %d\nevent: result\ndata: %d\n\n", mark, mark)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// nginx buffers responses by default and would hold every event back.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// How long the browser waits before reconnecting a dropped stream.
	if !write("retry: 5000\n\n") {
		return
	}

	// Catch up: anything that landed between the page being rendered and
	// this stream opening is one event, now.
	sent := since
	current, err := store.LatestResultMark(r.Context(), s.db)
	if err != nil {
		s.logger.Error("read result mark", "error", err)
		return
	}
	if since > 0 && current > since {
		if !event(current) {
			return
		}
		sent = current
	}

	heartbeat := time.NewTicker(liveHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.live.closed:
			return
		case <-heartbeat.C:
			if !write(": ping\n\n") {
				return
			}
		case <-ch:
			if mark := s.live.current(); mark > sent {
				if !event(mark) {
					return
				}
				sent = mark
			}
		}
	}
}
