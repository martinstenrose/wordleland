package web

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/ingest"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// stream is one open connection to the events route. One goroutine reads
// it for as long as it is open and hands every event's id on; a reader
// started per call would swallow the line it was waiting on when the call
// timed out.
type stream struct {
	t    *testing.T
	resp *http.Response
	ids  chan int64
	done chan struct{}
}

// openStream connects to path on a server listening on a real port, with
// the cookies given, and fails unless the request is answered.
func openStream(t *testing.T, ts *httptest.Server, path string, cookies []*http.Cookie, headers map[string]string) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	s := &stream{t: t, resp: resp, ids: make(chan int64, 16), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		lines := bufio.NewReader(resp.Body)
		for {
			text, err := lines.ReadString('\n')
			if err != nil {
				return
			}
			// Comment lines — the heartbeat — and the event and data
			// lines are skipped; the id is the event.
			if strings.HasPrefix(text, "id: ") {
				id, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(text, "id: ")), 10, 64)
				s.ids <- id
			}
		}
	}()
	return s
}

// next returns the next event's id, or 0 when none arrives within the wait.
func (s *stream) next(wait time.Duration) int64 {
	s.t.Helper()
	select {
	case id := <-s.ids:
		return id
	case <-time.After(wait):
		return 0
	}
}

// ended reports whether the server closed the stream within the wait.
func (s *stream) ended(wait time.Duration) bool {
	s.t.Helper()
	select {
	case <-s.done:
		return true
	case <-time.After(wait):
		return false
	}
}

// file lands one result for a player through the same rules the bridge
// uses, which is what moves the mark.
func file(t *testing.T, srv *Server, slug string, puzzle, guesses int) {
	t.Helper()
	g := guesses
	_, err := ingest.Apply(context.Background(), srv.db, store.SystemActor(), ingest.Submission{
		Slug: slug, PuzzleNo: puzzle, Solved: true, Guesses: &g,
	}, true)
	if err != nil {
		t.Fatalf("file %s: %v", slug, err)
	}
}

func liveServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := testServer(t)
	seedBoard(t, srv)
	// Fast enough for a test to wait on; production polls every two
	// seconds.
	srv.live.interval = 20 * time.Millisecond
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func currentMark(t *testing.T, srv *Server) int64 {
	t.Helper()
	mark, err := store.LatestResultMark(context.Background(), srv.db)
	if err != nil {
		t.Fatalf("LatestResultMark: %v", err)
	}
	return mark
}

// The stream is behind the same door as the pages: a session, or the share
// slug. A stranger learns nothing from it — and the shared copy exists so
// the shared Today redraws too.
func TestTheStreamIsBehindTheSameDoorAsThePages(t *testing.T) {
	srv, ts := liveServer(t)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	anon := fetchAs(t, srv, "/events", nil)
	if anon.Code != http.StatusSeeOther || anon.Header().Get("Location") != "/" {
		t.Errorf("GET /events without a session = %d to %q, want a redirect to sign in", anon.Code, anon.Header().Get("Location"))
	}
	if wrong := fetchAs(t, srv, "/share/not-the-slug/events", nil); wrong.Code != http.StatusNotFound {
		t.Errorf("GET /share/<wrong>/events = %d, want 404", wrong.Code)
	}

	for _, path := range []string{"/events", "/share/" + slug + "/events"} {
		var cookies []*http.Cookie
		if path == "/events" {
			_, session := adminSession(t, srv)
			cookies = []*http.Cookie{session}
		}
		s := openStream(t, ts, path, cookies, nil)
		if s.resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, s.resp.StatusCode)
		}
		if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("GET %s served as %q", path, ct)
		}
	}
}

// A stream opened with the mark its page was rendered at is caught up at
// once when something has landed since, and hears nothing when nothing has.
// Then every open stream hears a result the moment the poll sees it.
func TestTheStreamCatchesUpAndThenBroadcasts(t *testing.T) {
	srv, ts := liveServer(t)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	_, session := adminSession(t, srv)
	current := wordle.PuzzleForDate(time.Now())

	// The seed writes results directly, so the log is empty until one is
	// filed properly. Two, so there is a mark to be behind.
	file(t, srv, "lapsed", current-1, 4)
	before := currentMark(t, srv)
	file(t, srv, "lapsed", current-2, 5)
	mark := currentMark(t, srv)
	if mark <= before {
		t.Fatalf("filing a result did not move the mark (%d then %d)", before, mark)
	}

	behind := openStream(t, ts, fmt.Sprintf("/events?since=%d", before), []*http.Cookie{session}, nil)
	if got := behind.next(2 * time.Second); got != mark {
		t.Errorf("a stream behind by one heard %d, want the current mark %d", got, mark)
	}
	// A browser reconnecting says where it got to in a header, which wins.
	reconnect := openStream(t, ts, fmt.Sprintf("/events?since=%d", mark), []*http.Cookie{session}, map[string]string{"Last-Event-ID": strconv.FormatInt(before, 10)})
	if got := reconnect.next(2 * time.Second); got != mark {
		t.Errorf("a reconnecting stream heard %d, want the current mark %d", got, mark)
	}

	fresh := openStream(t, ts, fmt.Sprintf("/events?since=%d", mark), []*http.Cookie{session}, nil)
	shared := openStream(t, ts, fmt.Sprintf("/share/%s/events?since=%d", slug, mark), nil, nil)
	if got := fresh.next(300 * time.Millisecond); got != 0 {
		t.Errorf("a current stream heard %d before anything happened", got)
	}

	file(t, srv, "lapsed", current, 3)
	after := currentMark(t, srv)
	for name, s := range map[string]*stream{"signed in": fresh, "shared": shared, "behind": behind, "reconnect": reconnect} {
		if got := s.next(2 * time.Second); got != after {
			t.Errorf("the %s stream heard %d after a result landed, want %d", name, got, after)
		}
	}
}

// Each stream is a held connection, so there is a ceiling, and a client
// over it is told to come back rather than left hanging.
func TestTheStreamHasACeiling(t *testing.T) {
	srv, ts := liveServer(t)
	srv.live.limit = 2
	_, session := adminSession(t, srv)

	for i := 0; i < 2; i++ {
		s := openStream(t, ts, "/events", []*http.Cookie{session}, nil)
		if s.resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d refused with %d", i+1, s.resp.StatusCode)
		}
	}
	third := openStream(t, ts, "/events", []*http.Cookie{session}, nil)
	if third.resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("the stream over the ceiling = %d, want 503", third.resp.StatusCode)
	}
	if third.resp.Header.Get("Retry-After") == "" {
		t.Error("the refused stream is not told when to come back")
	}
}

// Closing the server ends every stream, so a restart is not held up by
// readers who are not going anywhere.
func TestCloseEndsEveryStream(t *testing.T) {
	srv, ts := liveServer(t)
	_, session := adminSession(t, srv)

	s := openStream(t, ts, "/events", []*http.Cookie{session}, nil)
	if s.resp.StatusCode != http.StatusOK {
		t.Fatalf("stream refused with %d", s.resp.StatusCode)
	}
	srv.Close()
	if !s.ended(2 * time.Second) {
		t.Error("the stream is still open after Close")
	}
	if late := fetchAs(t, srv, "/events", session); late.Code != http.StatusServiceUnavailable {
		t.Errorf("a stream opened after Close = %d, want 503", late.Code)
	}
}

// Today and the board carry the subscription, with the mark they were
// rendered at and their own URL to fetch again; the shared copies point
// under the share prefix; nothing else subscribes.
func TestTodayAndTheBoardSubscribe(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)
	_, session := adminSession(t, srv)
	mark := strconv.FormatInt(currentMark(t, srv), 10)

	for _, tt := range []struct {
		path, stream, page string
		cookie             *http.Cookie
	}{
		{"/today", "/events?since=" + mark, "/today", session},
		{"/leaderboard?mode=hard", "/events?since=" + mark, "/leaderboard?mode=hard", session},
		{"/share/" + slug + "/", "/share/" + slug + "/events?since=" + mark, "/share/" + slug + "/", nil},
		{"/share/" + slug + "/board", "/share/" + slug + "/events?since=" + mark, "/share/" + slug + "/board", nil},
	} {
		body := fetchAs(t, srv, tt.path, tt.cookie).Body.String()
		// The tag ends at the last attribute's closing quote; a bare ">"
		// would stop inside hx-select's "#main > *".
		main, ok := sectionOf(body, `<main id="main"`, `">`)
		if !ok {
			t.Fatalf("%s: no main region", tt.path)
		}
		for _, attr := range []string{
			`hx-ext="sse"`,
			`sse-connect="` + tt.stream + `"`,
			`hx-trigger="sse:result"`,
			`hx-get="` + tt.page + `"`,
			`hx-select="#main > *"`,
			`hx-target="this"`,
			`hx-push-url="false"`,
			// Or every boosted link inside the region would inherit the
			// region's own swap and land in it, at the same address.
			`hx-disinherit="hx-select hx-target hx-swap hx-push-url hx-indicator"`,
		} {
			if !strings.Contains(main, attr) {
				t.Errorf("%s: the main region is missing %s\n%s", tt.path, attr, main)
			}
		}
	}

	for _, path := range []string{"/months", "/grid", "/players", "/settings", "/admin/settings"} {
		body := fetchAs(t, srv, path, session).Body.String()
		if strings.Contains(body, "sse-connect") {
			t.Errorf("%s subscribes to the stream, and is not one of the two pages that should", path)
		}
	}
}
