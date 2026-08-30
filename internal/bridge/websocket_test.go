package bridge

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsServer is a stand-in for signal-cli-rest-api's receive endpoint.
type wsServer struct {
	*httptest.Server
	// connections counts accepted upgrades, so a test can prove the client
	// reconnected rather than merely stayed up.
	connections atomic.Int32
}

// newWSServer serves frames, running onConn for each accepted connection.
func newWSServer(t *testing.T, onConn func(conn *websocket.Conn, n int)) *wsServer {
	t.Helper()

	s := &wsServer{}
	upgrader := websocket.Upgrader{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/receive/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		onConn(conn, int(s.connections.Add(1)))
	}))
	t.Cleanup(s.Close)
	return s
}

func testSource(t *testing.T, srv *wsServer) *websocketSource {
	t.Helper()

	src, err := newWebsocketSource(srv.URL, "+00000000000",
		slog.New(slog.NewTextHandler(io.Discard, nil)), newHealth(time.Now))
	if err != nil {
		t.Fatalf("newWebsocketSource: %v", err)
	}
	// Reconnect immediately in tests; the backoff itself is tested separately.
	src.dialer.HandshakeTimeout = 2 * time.Second
	return src
}

func TestWebsocketReceivesMessages(t *testing.T) {
	srv := newWSServer(t, func(conn *websocket.Conn, _ int) {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(dataEnvelope("Wordle 1 891 3/6*", testGroupID)))
		time.Sleep(200 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := make(chan Message, 4)
	go testSource(t, srv).Run(ctx, out)

	select {
	case m := <-out:
		if m.Body != "Wordle 1 891 3/6*" {
			t.Errorf("Body = %q", m.Body)
		}
		if m.SenderUUID != testUUID || m.GroupID != testGroupID {
			t.Errorf("message = %+v, want the sender and group carried through", m)
		}
	case <-ctx.Done():
		t.Fatal("no message arrived")
	}
}

// A dropped connection is a data-loss window, since nothing queues while we
// are away — so reconnecting promptly is the whole mitigation.
func TestWebsocketReconnects(t *testing.T) {
	srv := newWSServer(t, func(conn *websocket.Conn, n int) {
		if n == 1 {
			// Deliver one, then drop the connection mid-stream.
			_ = conn.WriteMessage(websocket.TextMessage, []byte(dataEnvelope("Wordle 1 891 3/6", testGroupID)))
			time.Sleep(50 * time.Millisecond)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(dataEnvelope("Wordle 1 892 4/6", testGroupID)))
		time.Sleep(time.Second)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	out := make(chan Message, 4)
	go testSource(t, srv).Run(ctx, out)

	var bodies []string
	for len(bodies) < 2 {
		select {
		case m := <-out:
			bodies = append(bodies, m.Body)
		case <-ctx.Done():
			t.Fatalf("only received %v before giving up", bodies)
		}
	}

	if bodies[0] != "Wordle 1 891 3/6" || bodies[1] != "Wordle 1 892 4/6" {
		t.Errorf("received %v, want both messages in order across the reconnect", bodies)
	}
	if got := srv.connections.Load(); got < 2 {
		t.Errorf("accepted %d connections, want the client to have reconnected", got)
	}
}

// Frames that are not messages must not stop the loop.
func TestWebsocketSkipsNonMessages(t *testing.T) {
	srv := newWSServer(t, func(conn *websocket.Conn, _ int) {
		for _, frame := range []string{
			`{"error":"signal-cli says something went wrong"}`,
			`not json at all`,
			`{"envelope":{"sourceUuid":"x","receiptMessage":{"when":1}}}`,
			dataEnvelope("Wordle 1 891 3/6", testGroupID),
		} {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(frame))
		}
		time.Sleep(500 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	out := make(chan Message, 4)
	go testSource(t, srv).Run(ctx, out)

	select {
	case m := <-out:
		if m.Body != "Wordle 1 891 3/6" {
			t.Errorf("Body = %q, want the loop to have skipped the noise", m.Body)
		}
	case <-ctx.Done():
		t.Fatal("the loop stopped on a frame it should have skipped")
	}
}

// In normal and native modes the same path is a plain GET rather than a
// websocket, which would otherwise look like a bot receiving nothing.
func TestWebsocketReportsARefusedUpgrade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	src, err := newWebsocketSource(srv.URL, "+00000000000",
		slog.New(slog.NewTextHandler(io.Discard, nil)), newHealth(time.Now))
	if err != nil {
		t.Fatalf("newWebsocketSource: %v", err)
	}

	_, streamErr := src.stream(context.Background(), make(chan Message, 1))
	if streamErr == nil || !strings.Contains(streamErr.Error(), "json-rpc") {
		t.Errorf("error = %v, want it to name the required mode", streamErr)
	}
}

func TestWebsocketRejectsOversizedFrames(t *testing.T) {
	srv := newWSServer(t, func(conn *websocket.Conn, _ int) {
		_ = conn.WriteMessage(websocket.TextMessage, make([]byte, maxWebsocketFrameSize+1))
	})

	_, err := testSource(t, srv).stream(context.Background(), make(chan Message, 1))
	if err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Errorf("error = %v, want the frame read limit", err)
	}
}

func TestWebsocketURL(t *testing.T) {
	tests := map[string]string{
		"http://signal:8080":  "ws://signal:8080/v1/receive/+00000000000",
		"https://signal:8080": "wss://signal:8080/v1/receive/+00000000000",
		"http://signal:8080/": "ws://signal:8080/v1/receive/+00000000000",
	}

	for in, want := range tests {
		src, err := newWebsocketSource(in, "+00000000000",
			slog.New(slog.NewTextHandler(io.Discard, nil)), newHealth(time.Now))
		if err != nil {
			t.Fatalf("newWebsocketSource(%q): %v", in, err)
		}
		if src.url != want {
			t.Errorf("url for %q = %q, want %q", in, src.url, want)
		}
	}
}

// The health signal must follow the connection, since that is what the
// probe reports on.
func TestWebsocketUpdatesHealth(t *testing.T) {
	srv := newWSServer(t, func(conn *websocket.Conn, _ int) {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(dataEnvelope("Wordle 1 891 3/6", testGroupID)))
		time.Sleep(300 * time.Millisecond)
	})

	h := newHealth(time.Now)
	src, err := newWebsocketSource(srv.URL, "+00000000000",
		slog.New(slog.NewTextHandler(io.Discard, nil)), h)
	if err != nil {
		t.Fatalf("newWebsocketSource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := make(chan Message, 4)
	go src.Run(ctx, out)

	select {
	case <-out:
	case <-ctx.Done():
		t.Fatal("no message arrived")
	}

	h.mu.Lock()
	connected, lastMessage := h.connectedNow, h.lastMessage
	h.mu.Unlock()

	if !connected {
		t.Error("health does not report the connection")
	}
	if lastMessage.IsZero() {
		t.Error("health did not record a received frame")
	}
}
