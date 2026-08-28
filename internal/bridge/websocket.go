package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Connection timings.
//
// signal-cli-rest-api pings every 54s (its pingPeriod is pongWait*9/10 with
// pongWait at 60s) and gorilla answers pings automatically. A read deadline
// comfortably past that interval therefore distinguishes a dead connection
// from an idle one: the group can be silent for hours, but the server's
// pings cannot stop.
const (
	readDeadline     = 90 * time.Second
	handshakeTimeout = 15 * time.Second

	minReconnectDelay = time.Second
	maxReconnectDelay = 60 * time.Second
)

// websocketSource receives from signal-cli-rest-api and reconnects on its
// own, so nothing above this layer has to know the connection dropped.
type websocketSource struct {
	url    string
	logger *slog.Logger
	// health is notified as the connection comes and goes.
	health *health

	dialer *websocket.Dialer
}

// newWebsocketSource builds the receive URL from the API base and account.
//
// The path is a websocket only in json-rpc mode. In normal or native mode
// the same path is a plain GET that returns one batch and stops, which
// would look like a bot that receives once and then goes quiet.
func newWebsocketSource(apiURL, account string, logger *slog.Logger, h *health) (*websocketSource, error) {
	base, err := url.Parse(apiURL)
	if err != nil {
		return nil, err
	}
	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	default:
		return nil, errors.New("SIGNAL_API_URL must be http or https")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/receive/" + url.PathEscape(account)

	return &websocketSource{
		url:    base.String(),
		logger: logger,
		health: h,
		dialer: &websocket.Dialer{HandshakeTimeout: handshakeTimeout},
	}, nil
}

// Run connects, reads, and reconnects until the context is cancelled.
func (s *websocketSource) Run(ctx context.Context, out chan<- Message) error {
	delay := minReconnectDelay

	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		read, err := s.stream(ctx, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// The delay resets only once a frame has actually arrived, not
		// merely because the dial succeeded: a server that accepts the
		// connection and immediately closes it should not reset the backoff
		// and be hammered once a second.
		if read {
			delay = minReconnectDelay
			attempt = 1
		}

		// A dropped connection is a data-loss window, not just a blip:
		// signal-cli-rest-api registers a receive channel per websocket and
		// removes it on disconnect, so messages arriving while we are away
		// are dropped rather than queued. Logged at info every time for
		// that reason.
		s.logger.Info("signal connection lost, reconnecting",
			"attempt", attempt, "in", delay.Round(time.Millisecond), "error", err)

		sleepContext(ctx, jitter(delay))
		if delay < maxReconnectDelay {
			delay *= 2
			if delay > maxReconnectDelay {
				delay = maxReconnectDelay
			}
		}
	}
}

// stream holds one connection open, reporting whether it read anything.
func (s *websocketSource) stream(ctx context.Context, out chan<- Message) (read bool, err error) {
	conn, resp, err := s.dialer.DialContext(ctx, s.url, nil)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusBadRequest {
				// The endpoint answers a plain GET in normal and native
				// modes and refuses the upgrade.
				return false, errors.New("the server refused the websocket upgrade; " +
					"signal-cli-rest-api must be running with MODE=json-rpc")
			}
		}
		return false, err
	}
	defer conn.Close()

	s.health.connected()
	defer s.health.disconnected()
	s.logger.Info("connected to signal")

	if err := conn.SetReadDeadline(time.Now().Add(readDeadline)); err != nil {
		return false, err
	}
	// Gorilla answers pings automatically; this only extends the deadline,
	// so a quiet group does not look like a dead socket.
	conn.SetPingHandler(func(payload string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		return conn.WriteControl(websocket.PongMessage, []byte(payload),
			time.Now().Add(handshakeTimeout))
	})

	// Closing the connection is what unblocks ReadMessage on shutdown.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return read, err
		}
		read = true
		if err := conn.SetReadDeadline(time.Now().Add(readDeadline)); err != nil {
			return read, err
		}
		s.health.received()

		msg, ok := decode(data, s.logger)
		if !ok {
			continue
		}

		// This send is the only thing the read loop does with a message,
		// and out is buffered. That matters: signal-cli-rest-api fans out to
		// its receive channel with a non-blocking send, so a reader that
		// pauses to do work has messages dropped on the floor upstream. All
		// parsing and delivery happens in another goroutine for that reason.
		select {
		case out <- msg:
		case <-ctx.Done():
			return read, ctx.Err()
		}
	}
}

// decode turns a frame into a message, reporting whether there was one.
func decode(data []byte, logger *slog.Logger) (Message, bool) {
	// The server also writes {"error":"..."} frames when signal-cli reports
	// a problem.
	var wsErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &wsErr); err == nil && wsErr.Error != "" {
		// Sanitised, not relayed: this string is written by signal-cli and
		// can carry both newlines and a phone number. See sanitizeRemote.
		logger.Warn("signal reported an error", "error", sanitizeRemote(wsErr.Error))
		return Message{}, false
	}

	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		// Deliberately without the frame itself: a raw envelope carries
		// phone numbers, and dumping one would write them to disk.
		logger.Warn("could not parse a frame from signal",
			"error", sanitizeRemote(err.Error()))
		return Message{}, false
	}
	return env.message()
}

// jitter spreads reconnects so a whole-stack restart does not produce a
// synchronised retry against a server that is still starting.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}
