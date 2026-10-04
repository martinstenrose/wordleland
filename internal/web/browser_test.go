//go:build browser

package web

// Tests that need a browser.
//
// Everything else in this package tests the markup the server sends. These
// test what a browser makes of it: whether following a link reloads the
// document, where the page is scrolled to afterwards, where focus went,
// whether two headings are the same height. None of that is in the HTML, and
// every one of these was a bug that shipped past the rest of the suite.
//
// They are behind a build tag so that `go test ./...` stays fast and needs
// nothing installed. Run them with:
//
//	go test -tags browser -run TestBrowser ./internal/web/
//
// They drive whatever Chrome is on PATH — WORDLELAND_CHROME overrides — over
// the DevTools protocol, with the websocket client the Signal bridge already
// depends on. No other dependency, no npm, no browser download: the one
// thing these need is a browser, which every developer and every CI runner
// already has.
//
// The harness is written to outlive the current front end. The assertions
// read pages off the bar and the pill row rather than from a list, and they
// assert what a reader would notice — "no reload", "same place", "still
// here" — rather than how the script achieves it. Swap the script for
// another and these are the parity net.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/martinstenrose/wordleland/internal/store"
	"github.com/martinstenrose/wordleland/internal/wordle"
)

// ---- Chrome ---------------------------------------------------------------

// chromePath finds a Chrome or Chromium to drive, or returns "" when there is
// none. WORDLELAND_CHROME wins, then PATH, then the two places macOS keeps
// them.
func chromePath() string {
	if p := os.Getenv("WORDLELAND_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// browser is one headless Chrome, started for one test and killed after it.
// One per test rather than one per package: a browser that a previous test
// left in a strange state is a flaky suite, and a second of startup is cheap
// against that.
type browser struct {
	t    *testing.T
	port int
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	path := chromePath()
	if path == "" {
		t.Skip("no Chrome found; set WORDLELAND_CHROME to point at one")
	}
	port, err := startChrome(t, path, chromeStartTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, port: port}
}

// chromeStartTimeout is how long a Chrome gets to answer. A warm one takes
// well under a second, but the first start on a fresh CI runner has been
// seen to take more than 15. The wait only runs this long when Chrome is
// alive and silent: one that exits is reported as soon as it does.
const chromeStartTimeout = 60 * time.Second

// startChrome starts the Chrome at path, killed when the test ends, and
// returns its DevTools port once the endpoint answers. An error carries the
// tail of what Chrome wrote to stderr, which is where it says why.
func startChrome(t *testing.T, path string, timeout time.Duration) (int, error) {
	t.Helper()
	port := freePort(t)
	cmd := exec.Command(path,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox", // CI containers have no user namespace for Chrome's own.
		"--no-first-run",
		"--disable-extensions",
		"--disable-background-networking",
		"--user-data-dir="+t.TempDir(),
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"about:blank",
	)
	stderr := &tailBuffer{max: 2048}
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start chrome: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	// Ready when the DevTools endpoint answers.
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	deadline := time.After(timeout)
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return port, nil
		}
		select {
		case werr := <-exited:
			exited <- werr // for the cleanup
			return 0, fmt.Errorf("chrome exited before answering on port %d (%v); stderr:\n%s", port, werr, stderr)
		case <-deadline:
			return 0, fmt.Errorf("chrome did not come up on port %d within %v: %v; stderr:\n%s", port, timeout, err, stderr)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// tailBuffer keeps the last max bytes written to it. Chrome logs freely to
// stderr while it runs; only the end is worth putting in a failure.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (w *tailBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	if over := len(w.b) - w.max; over > 0 {
		w.b = w.b[over:]
	}
	return len(p), nil
}

func (w *tailBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// A Chrome that dies on start is reported at once, with what it said,
// rather than after the whole start timeout.
func TestBrowserAChromeThatExitsIsReportedAtOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in Chrome is a shell script")
	}
	fake := t.TempDir() + "/chrome"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'no display, giving up' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	_, err := startChrome(t, fake, chromeStartTimeout)
	if err == nil {
		t.Fatal("a Chrome that exited was reported as started")
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("the exit took %v to report; it should not wait out the timeout", took)
	}
	if !strings.Contains(err.Error(), "no display, giving up") {
		t.Errorf("the error does not carry Chrome's stderr: %v", err)
	}
}

// ---- A page ---------------------------------------------------------------

// page is one tab, driven over its DevTools websocket. It records every
// console error and uncaught exception the tab raises, because "no errors"
// is one of the things worth asserting and nobody remembers to look.
type page struct {
	t  *testing.T
	ws *websocket.Conn

	mu      sync.Mutex
	seq     int
	replies map[int]chan json.RawMessage

	errMu  sync.Mutex
	errors []string
}

type cdpMessage struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (b *browser) newPage() *page {
	t := b.t
	t.Helper()

	req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/json/new?about:blank", b.port), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open tab: %v", err)
	}
	defer resp.Body.Close()
	var target struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
		t.Fatalf("decode tab: %v", err)
	}

	ws, _, err := websocket.DefaultDialer.Dial(target.WebSocketDebuggerURL, nil)
	if err != nil {
		t.Fatalf("dial devtools: %v", err)
	}
	p := &page{t: t, ws: ws, replies: map[int]chan json.RawMessage{}}
	t.Cleanup(func() { ws.Close() })
	go p.read()

	for _, domain := range []string{"Runtime.enable", "Log.enable", "Page.enable", "Network.enable"} {
		p.call(domain, nil)
	}
	return p
}

// read routes replies to whoever is waiting and keeps every error the tab
// reports, for as long as the tab lives.
func (p *page) read() {
	for {
		_, data, err := p.ws.ReadMessage()
		if err != nil {
			return
		}
		var m cdpMessage
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID != 0 {
			p.mu.Lock()
			ch, ok := p.replies[m.ID]
			delete(p.replies, m.ID)
			p.mu.Unlock()
			if ok {
				if m.Error != nil {
					ch <- json.RawMessage(fmt.Sprintf(`{"__error":%q}`, m.Error.Message))
				} else {
					ch <- m.Result
				}
			}
			continue
		}
		switch m.Method {
		case "Runtime.exceptionThrown":
			var ev struct {
				ExceptionDetails struct {
					Text      string `json:"text"`
					Exception struct {
						Description string `json:"description"`
					} `json:"exception"`
				} `json:"exceptionDetails"`
			}
			_ = json.Unmarshal(m.Params, &ev)
			p.noteError("exception: " + firstLine(ev.ExceptionDetails.Exception.Description, ev.ExceptionDetails.Text))
		case "Log.entryAdded":
			var ev struct {
				Entry struct {
					Level string `json:"level"`
					Text  string `json:"text"`
				} `json:"entry"`
			}
			_ = json.Unmarshal(m.Params, &ev)
			if ev.Entry.Level == "error" {
				p.noteError("console: " + ev.Entry.Text)
			}
		}
	}
}

func firstLine(s, fallback string) string {
	if s == "" {
		s = fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (p *page) noteError(s string) {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	p.errors = append(p.errors, s)
}

// Errors is everything the tab has complained about so far.
func (p *page) Errors() []string {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return append([]string(nil), p.errors...)
}

// call sends one DevTools command and waits for its reply.
func (p *page) call(method string, params any) json.RawMessage {
	p.t.Helper()
	p.mu.Lock()
	p.seq++
	id := p.seq
	ch := make(chan json.RawMessage, 1)
	p.replies[id] = ch
	p.mu.Unlock()

	if err := p.ws.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		p.t.Fatalf("%s: write: %v", method, err)
	}
	select {
	case reply := <-ch:
		var failed struct {
			Error string `json:"__error"`
		}
		if json.Unmarshal(reply, &failed) == nil && failed.Error != "" {
			p.t.Fatalf("%s: %s", method, failed.Error)
		}
		return reply
	case <-time.After(15 * time.Second):
		p.t.Fatalf("%s: no reply in 15s", method)
		return nil
	}
}

// Eval runs an expression in the page and returns its value. A promise is
// awaited; an exception is a test failure, with the expression in the
// message so it can be pasted into a DevTools console and looked at.
func (p *page) Eval(expr string) any {
	p.t.Helper()
	reply := p.call("Runtime.evaluate", map[string]any{
		"expression":    expr,
		"awaitPromise":  true,
		"returnByValue": true,
	})
	var out struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(reply, &out); err != nil {
		p.t.Fatalf("Eval: decode: %v", err)
	}
	if out.ExceptionDetails != nil {
		p.t.Fatalf("Eval threw %s\n  in: %s", firstLine(out.ExceptionDetails.Exception.Description, out.ExceptionDetails.Text), expr)
	}
	return out.Result.Value
}

// Number and String read an Eval result in the type the assertion wants, so
// a test line stays a test line rather than a type assertion.
func (p *page) Number(expr string) float64 {
	p.t.Helper()
	switch v := p.Eval(expr).(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	default:
		p.t.Fatalf("%s: got %T, want a number", expr, v)
		return 0
	}
}

func (p *page) String(expr string) string {
	p.t.Helper()
	v, ok := p.Eval(expr).(string)
	if !ok {
		p.t.Fatalf("%s: got %T, want a string", expr, p.Eval(expr))
	}
	return v
}

// Strings reads a JS array of strings.
func (p *page) Strings(expr string) []string {
	p.t.Helper()
	raw, ok := p.Eval(expr).([]any)
	if !ok {
		p.t.Fatalf("%s: want an array", expr)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// Navigate loads a URL the way the address bar would, and waits for the page
// to be complete — which, with app.js deferred, means its script has run.
func (p *page) Navigate(url string) {
	p.t.Helper()
	p.call("Page.navigate", map[string]any{"url": url})
	p.WaitFor(`document.readyState === "complete"`)
}

// Click presses the first element the selector finds, the way a reader would.
// Not a synthetic event on the document: the element's own click, so every
// listener between it and the document sees the same thing a finger sends.
func (p *page) Click(selector string) {
	p.t.Helper()
	found := p.Eval(fmt.Sprintf(`(() => { const el = document.querySelector(%q); if (!el) return false; el.click(); return true; })()`, selector))
	if found != true {
		p.t.Fatalf("Click: nothing matches %q", selector)
	}
}

// WaitFor polls until the expression is truthy. Polling rather than sleeping
// is the whole difference between a suite that is reliable and one that is
// tuned to one machine's speed.
func (p *page) WaitFor(expr string) {
	p.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if v := p.Eval(expr); v != nil && v != false && v != "" && v != float64(0) {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("WaitFor: still false after 8s: %s", expr)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// Press sends one key down and up as a keyboard would. A dispatched keydown
// is not the same thing — the browser's own focus movement and default
// actions happen only for a real press — and the overlay's shortcut and
// Esc are what a reader actually presses. The code is the physical key
// ("KeyK", "Escape"); modifiers are the protocol's bits, 2 for Ctrl.
func (p *page) Press(key, code string, modifiers int) {
	p.t.Helper()
	for _, kind := range []string{"keyDown", "keyUp"} {
		p.call("Input.dispatchKeyEvent", map[string]any{
			"type": kind, "key": key, "code": code, "modifiers": modifiers,
		})
	}
}

// Type puts text into whatever has focus, with the input events a keyboard
// would fire — which is what a debounce is listening for.
func (p *page) Type(text string) {
	p.t.Helper()
	p.call("Input.insertText", map[string]any{"text": text})
}

// ClickAt presses the mouse at a point in the viewport, so what is hit is
// whatever the page has stacked there — the one question a selector cannot
// answer.
func (p *page) ClickAt(x, y int) {
	p.t.Helper()
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		p.call("Input.dispatchMouseEvent", map[string]any{
			"type": kind, "x": x, "y": y, "button": "left", "clickCount": 1,
		})
	}
}

// Viewport sets the window the page is laid out in. Widths that matter here
// are the two the stylesheet has breakpoints between: a phone, and a desktop
// with the rail out.
func (p *page) Viewport(width, height int, mobile bool) {
	p.t.Helper()
	p.call("Emulation.setDeviceMetricsOverride", map[string]any{
		"width": width, "height": height, "deviceScaleFactor": 1, "mobile": mobile,
	})
}

// Cookies gives the tab a session, from the cookies the Go-side login helpers
// return. The server is the same one, so the session is real.
func (p *page) Cookies(origin string, cookies []*http.Cookie) {
	p.t.Helper()
	for _, c := range cookies {
		p.call("Network.setCookie", map[string]any{
			"name": c.Name, "value": c.Value, "url": origin,
		})
	}
}

// Path is where the page is now.
func (p *page) Path() string { return p.String("location.pathname") }

// ScrollY is how far down the page is.
func (p *page) ScrollY() float64 { return p.Number("window.scrollY") }

// ---- The application ------------------------------------------------------

const (
	phoneWidth   = 390
	desktopWidth = 1280
)

// site is the application listening on a real port, with a signed-in,
// enrolled admin and a board with players on it: the state most screens
// need, built once per test from the same helpers the rest of the suite uses.
type site struct {
	t       *testing.T
	srv     *Server
	base    string
	cookies []*http.Cookie
}

func newSite(t *testing.T) *site {
	t.Helper()
	srv := testServer(t)
	seedBoard(t, srv)
	seedLogin(t, srv, "browser@example.tld", true)

	_, cookies := login(t, srv, "browser@example.tld", testPassword)
	_, cookies = enrol(t, srv, cookies)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &site{t: t, srv: srv, base: ts.URL, cookies: cookies}
}

// open is a tab on this site, signed in, at the given width.
func (s *site) open(b *browser, width int) *page {
	s.t.Helper()
	p := b.newPage()
	p.Viewport(width, 900, width < 500)
	p.Cookies(s.base, s.cookies)
	return p
}

// barHrefs is every view the bar offers on the page that is open — the pages
// a reader can reach from anywhere. Read from the page, not listed here, so a
// view added later is tested without anyone remembering to add it.
//
// The capsule of pages is hidden on a phone, where the page menu carries the
// same list; the links are in the markup at either width, so one read covers
// both.
func barHrefs(p *page) []string {
	p.t.Helper()
	return p.Strings(`[...document.querySelectorAll(".bar-pages a.bar-page[href]")].map(a => a.getAttribute("href"))`)
}

// follow presses the bar's link to a view and waits until the page is there:
// the page in the capsule on a wide window, or the row in the page menu,
// opened first, on a phone. The way a reader moves between views at each
// width.
func follow(p *page, href string) {
	p.t.Helper()
	// Opening the menu and pressing the row happen in one evaluation, and
	// the <main> that was on screen is marked before the press: the switch
	// is done when that node is gone, not when the address matches. The
	// address is no witness on its own — a link to the page already open
	// matches before its fetch has landed, and the next press would then
	// find the menu it just opened swapped away. What ends a switch is the
	// old content leaving, whichever script does the swapping.
	ok := p.Eval(fmt.Sprintf(`(() => {
		const visible = a => a.getAttribute("href") === %q && a.getClientRects().length;
		let link = [...document.querySelectorAll(".bar-pages a[href]")].find(visible);
		if (!link) {
			const menu = document.querySelector("details.bar-menu");
			if (menu) menu.open = true;
			link = [...document.querySelectorAll(".bar-menu-panel a[href]")].find(visible);
		}
		if (!link) return false;
		const main = document.querySelector("main");
		if (main) main.__stale = true;
		link.click(); return true; })()`, href))
	if ok != true {
		p.t.Fatalf("follow: no visible link in the bar for %s", href)
	}
	// /players opens on the leader, so the address it lands on is a page
	// under it rather than the link's own.
	p.WaitFor(fmt.Sprintf(`!(document.querySelector("main") || {}).__stale
		&& (location.pathname === %q || location.pathname.startsWith(%q + "/"))`, href, href))
}

// ---- The assertions -------------------------------------------------------

// Following a link never reloads the document.
//
// The whole promise of switching pages in place. A counter set on window
// survives a switch and does not survive a load, so it is the one honest
// witness.
func TestBrowserFollowingALinkNeverReloads(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")
	p.Eval(`window.__alive = 1; true`)

	for _, href := range barHrefs(p) {
		follow(p, href)
		if alive := p.Number(`window.__alive || 0`); alive != 1 {
			t.Errorf("%s: the document was reloaded (window.__alive = %v)", href, alive)
		}
	}
	// A link inside a page, not only the rail: the first player on the board.
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; true`)
	p.Click(".board-card a.player")
	p.WaitFor(`location.pathname.startsWith("/players/")`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("a player link reloaded the document")
	}
}

// Back restores the URL and the scroll position.
//
// The switcher pushes real history entries and restores scroll on popstate;
// a focus call that scrolled was undoing the restoration a line later.
func TestBrowserBackRestoresURLAndScroll(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/today")

	// Somewhere long enough to scroll. Found rather than named.
	var from string
	for _, href := range barHrefs(p) {
		follow(p, href)
		if p.Number(`document.documentElement.scrollHeight - window.innerHeight`) > 300 {
			from = p.Path()
			break
		}
	}
	if from == "" {
		t.Skip("no view is tall enough to scroll at phone width with this data")
	}

	p.Eval(`window.scrollTo(0, 240); true`)
	p.WaitFor(`window.scrollY >= 230`)
	left := p.ScrollY()

	// Away, to any other view.
	var to string
	for _, href := range barHrefs(p) {
		if !strings.HasPrefix(from, href) {
			to = href
			break
		}
	}
	follow(p, to)
	if got := p.ScrollY(); got != 0 {
		t.Errorf("a switched-in page opened at scrollY %v, want 0", got)
	}

	p.Eval(`history.back(); true`)
	p.WaitFor(fmt.Sprintf(`location.pathname === %q`, from))
	p.WaitFor(fmt.Sprintf(`Math.abs(window.scrollY - %v) <= 2`, left))
}

// A switched-in page starts at the top, with the bar on screen.
//
// Focusing the main region scrolled it into view, and the bar above it is
// sticky, so every page arrived scrolled down by exactly the bar's height —
// on every view tall enough to scroll, which was five of the seven.
func TestBrowserASwitchedInPageStartsAtTheTop(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/today")

	for _, href := range barHrefs(p) {
		follow(p, href)
		if got := p.ScrollY(); got != 0 {
			t.Errorf("%s: opened at scrollY %v, want 0", href, got)
		}
		if bar := p.Number(`document.querySelector(".topbar").getBoundingClientRect().bottom`); bar <= 0 {
			t.Errorf("%s: the bar is scrolled off the top (bottom edge at %v)", href, bar)
		}
	}
}

// The title and its subtitle are in the same place, at the same size, on
// every view — a player's name, whose page has a pill row, included.
//
// Two bugs lived here. A glyph in front of a menu title pushed it 45px in.
// Then a line-height of 1.2 on the same title, against the default on the
// others, moved its glyphs 4px and the subtitle under it 8px while the box
// tops still matched — invisible to anything that reads the markup.
func TestBrowserTheTitleDoesNotMoveBetweenViews(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	// Where the title sits in the head it belongs to, and how it is set.
	// Every view names itself in a page head on the canvas; the board, still
	// being reworked, in its card's head.
	const probe = `(() => {
		const h = document.querySelector("main h1");
		if (!h) return "{}";
		const head = h.closest(".page-head, .card");
		const r = h.getBoundingClientRect(), c = head.getBoundingClientRect(), s = getComputedStyle(h);
		return JSON.stringify({
			kind: head.classList.contains("page-head") ? "page" : "card",
			at: Math.round(r.left - c.left) + " " + Math.round(r.top - c.top),
			set: [s.fontSize, s.fontWeight, s.lineHeight, s.letterSpacing].join(" "),
		});
	})()`
	type title struct{ Kind, At, Set string }

	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(b, width)
		p.Navigate(site.base + "/leaderboard")

		got := map[string]title{}
		for _, href := range barHrefs(p) {
			follow(p, href)
			var t2 title
			_ = json.Unmarshal([]byte(p.String(probe)), &t2)
			got[p.Path()] = t2
		}
		if len(got) < 3 {
			t.Fatalf("width %d: only %d views measured", width, len(got))
		}
		// Within a kind of head, the title is the same thing in the same
		// place. The two kinds are compared apart: a page head sits on the
		// canvas and a card's inside its card, by design.
		first := map[string]string{}
		for path, g := range got {
			if g.Kind == "" {
				t.Errorf("width %d: %s has no title", width, path)
				continue
			}
			key := g.Kind + " " + g.At + " " + g.Set
			if want, ok := first[g.Kind]; !ok {
				first[g.Kind] = key
			} else if key != want {
				t.Errorf("width %d: %s is laid out %q, but another %s head is %q", width, path, key, g.Kind, want)
			}
		}
	}
}

// Today's two lists are laid out on one rhythm, and the five chips leave the
// name room.
//
// The form row carries five score chips and two figures either side of the
// name, all at fixed widths, so the name is what gives when the row is
// short of room — on a phone it was down to one letter before the widths
// were measured. On a wide window the lists stand side by side, level row
// for row; on a phone they are two tabs, one list at a time, and the form
// list is measured once its tab is chosen. Nothing that reads the markup
// can see any of that; both languages are checked.
func TestBrowserTodaysTwoListsShareARhythm(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	const probe = `(() => {
		const q = s => document.querySelector(s);
		const box = e => e.getBoundingClientRect();
		const shown = s => box(q(s)).height > 0;
		return JSON.stringify({
			name: Math.round(box(q(".form-row .form-name")).width),
			result: Math.round(box(q(".result-row")).height),
			form: Math.round(box(q(".form-row")).height),
			results: shown(".today-results"),
			forms: shown(".today-form"),
			sideBySide: box(q(".today-results")).top === box(q(".today-form")).top,
		});
	})()`
	type layout struct {
		Name, Result, Form int
		Results, Forms     bool
		SideBySide         bool
	}
	measure := func(p *page, width int, lang string) layout {
		var got layout
		if err := json.Unmarshal([]byte(p.String(probe)), &got); err != nil {
			t.Fatalf("width %d %s: %v", width, lang, err)
		}
		return got
	}

	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(b, width)
		for _, lang := range []string{"en", "sv"} {
			p.Navigate(site.base + "/today?lang=" + lang)
			got := measure(p, width, lang)
			if width == desktopWidth {
				if !got.SideBySide || !got.Results || !got.Forms {
					t.Errorf("width %d %s: the lists are not side by side: %+v", width, lang, got)
				}
				if got.Result != got.Form {
					t.Errorf("width %d %s: a results row is %dpx tall and a form row %dpx", width, lang, got.Result, got.Form)
				}
			} else {
				if !got.Results || got.Forms {
					t.Errorf("width %d %s: the phone does not open on the results alone: %+v", width, lang, got)
				}
				p.Click(`label[for="today-tab-form"]`)
				got = measure(p, width, lang)
				if got.Results || !got.Forms {
					t.Errorf("width %d %s: the Form tab does not show the form alone: %+v", width, lang, got)
				}
			}
			if got.Name < 56 {
				t.Errorf("width %d %s: the form row leaves the name %dpx", width, lang, got.Name)
			}
		}
	}
}

// Opening the list of who has not filed leaves the day's headline where it
// was.
//
// The list opens from the progress count under the headline. Were the hero
// to centre its contents, or the list to open in the flow above the count,
// the reader would press a small control and the headline would move.
// Nothing that reads the markup can see that.
func TestBrowserOpeningTheMissingListLeavesTheHeadlineStill(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	const top = `document.querySelector(".today-lead").getBoundingClientRect().top`
	before := p.Number(top)
	p.Click(".today-out > summary")
	p.WaitFor(`document.querySelector(".today-out").open`)
	if after := p.Number(top); after != before {
		t.Errorf("the headline moved from %v to %v when the list opened", before, after)
	}
}

// On a phone the month's headline goes through to Months wherever it is
// pressed, not only on its chevron.
//
// A player's report: the chevron alone was too small a target for a thumb.
// The press is at the headline's text, the part furthest from the chevron.
func TestBrowserTheMonthsHeadlineIsOneTarget(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/today")
	if p.Number(`document.querySelectorAll(".today-standing .standing-line").length`) == 0 {
		t.Fatal("no month's headline on /today to press")
	}
	want := p.String(`document.querySelector(".standing-go").getAttribute("href")`)

	x := int(p.Number(`(r => r.left + 4)(document.querySelector(".standing-line").getBoundingClientRect())`))
	y := int(p.Number(`(r => r.top + r.height / 2)(document.querySelector(".standing-line").getBoundingClientRect())`))
	p.ClickAt(x, y)
	p.WaitFor(fmt.Sprintf(`location.pathname === %q || location.pathname.startsWith(%q + "/")`, want, want))
}

// The About panel covers what is under it.
//
// It is drawn from inside the account menu, which floats over the page as
// glass. A backdrop-filter on the menu would have made it the box the
// fixed panel is placed in, and the panel would have opened inside the menu;
// and anything positioned in the page could paint over a panel drawn from a
// layer below it. What is on top at a point is the one thing a selector
// cannot say.
func TestBrowserTheAboutPanelCoversThePage(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	p.Click("details.account > summary")
	p.WaitFor(`document.querySelector("details.account").open`)
	p.Click(".account-menu details.about > summary")
	p.WaitFor(`document.querySelector(".account-menu details.about").open`)

	// Centred on the window, not on the menu it opened from. The window's
	// visible width, clientWidth: innerWidth counts a classic scrollbar,
	// which CI's Chrome draws and a Mac's does not, and a fixed panel is
	// centred on what is left beside it.
	if off := p.Number(`(() => { const r = document.querySelector(".account-menu .about-panel").getBoundingClientRect();
		return Math.abs((r.left + r.right) / 2 - document.documentElement.clientWidth / 2); })()`); off > 2 {
		t.Errorf("the About panel is %vpx off the middle of the window", off)
	}

	// Every element of the page whose centre lies under the panel, and
	// whether the panel is what is hit there.
	showing := p.Strings(`(() => {
		const panel = document.querySelector(".account-menu .about-panel");
		const box = panel.getBoundingClientRect();
		const out = [];
		for (const el of document.querySelectorAll("main *")) {
			const r = el.getBoundingClientRect();
			if (!r.width || !r.height) continue;
			const x = r.left + r.width / 2, y = r.top + r.height / 2;
			if (x < box.left || x > box.right || y < box.top || y > box.bottom) continue;
			const hit = document.elementFromPoint(x, y);
			if (!panel.contains(hit)) out.push(el.className + ": " + el.textContent.trim().slice(0, 20));
		}
		return out;
	})()`)
	if len(showing) > 0 {
		t.Errorf("%d elements of the page show through the About panel: %v", len(showing), showing)
	}
}

// Esc closes a popup, one per press and the innermost first, and focus
// that was inside it lands back on what opened it. About opened from the
// account menu closes the menu with it.
//
// A <details> opens and closes with no script, but not from the keyboard
// without first finding its summary again — which, for Help drawn over the
// whole page, is behind the panel being closed.
func TestBrowserEscClosesPopupsInnermostFirst(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/leaderboard")

	const menuOpen = `document.querySelector("details.account").open`
	const helpOpen = `document.querySelector(".account-menu details.about").open`
	p.Click("details.account > summary")
	p.WaitFor(menuOpen)
	p.Click(".account-menu details.about > summary")
	p.WaitFor(helpOpen)
	p.Eval(`document.querySelector(".account-menu .about-link").focus(); true`)

	if p.Eval(`getComputedStyle(document.querySelector(".account-menu")).visibility`) != "hidden" {
		t.Errorf("the account menu still shows under the About panel")
	}

	// About takes the menu it opened from with it, and focus lands on the
	// menu's summary rather than being lost with the menu.
	p.Press("Escape", "Escape", 0)
	p.WaitFor(`!` + helpOpen)
	p.WaitFor(`!` + menuOpen)
	if p.Eval(`document.activeElement === document.querySelector("details.account > summary")`) != true {
		t.Errorf("closing About did not put focus back on the account menu's summary")
	}
	if path := p.Path(); path != "/leaderboard" {
		t.Errorf("Esc navigated to %s", path)
	}

	// An open menu with no About in it closes on Esc as before.
	p.Click("details.account > summary")
	p.WaitFor(menuOpen)
	p.Press("Escape", "Escape", 0)
	p.WaitFor(`!` + menuOpen)
}

// No view raises a console error or an uncaught exception.
//
// Every view the rail offers, every admin section, and a player's page —
// gathered from the pages themselves, so a screen added later is covered.
func TestBrowserNoConsoleErrorsOnAnyView(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	seen := map[string]bool{}
	visit := func(href string) {
		if seen[href] {
			return
		}
		seen[href] = true
		p.Navigate(site.base + href)
		if errs := p.Errors(); len(errs) > 0 {
			t.Errorf("%s: %s", href, strings.Join(errs, "; "))
			p.errMu.Lock()
			p.errors = nil
			p.errMu.Unlock()
		}
	}
	for _, href := range barHrefs(p) {
		visit(href)
	}
	// Inside the admin area, every section the bar lists.
	p.Navigate(site.base + "/admin/settings")
	for _, href := range p.Strings(`[...document.querySelectorAll(".pills a")].map(a => a.getAttribute("href"))`) {
		visit(href)
	}
	// And one player, by way of the roster.
	p.Navigate(site.base + "/players")
	visit(p.Path())
}

// A link to a page that does not exist shows the error frame in place — no
// rail, no reload — and Back brings the rail back.
//
// The body is swapped whole rather than a region inside it partly for
// this: an error page is chrome for a stranger and carries no navigation,
// and a region swap would have left the rail standing around "there is
// nothing at this address". No page carries such a link on purpose, so one
// is put there; following it is what a reader with a stale link from the
// group chat does.
func TestBrowserAMissingPageKeepsTheBarOff(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; true`)

	// htmx boosts what it has processed. Everything the server renders and
	// everything htmx swaps in is; a link a test writes into the page is
	// not, so it is handed over the way app.js hands over what it inserts.
	p.Eval(`document.querySelector("main").insertAdjacentHTML("afterbegin",
		'<a id="__missing" href="/no-such-page">nowhere</a>');
		htmx.process(document.getElementById("__missing")); true`)
	p.Click("#__missing")
	p.WaitFor(`location.pathname === "/no-such-page"`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Fatalf("the document was reloaded on the way to the error page")
	}
	if n := p.Number(`document.querySelectorAll(".topbar").length`); n != 0 {
		t.Errorf("the error page carries the application's bar")
	}
	if h := p.String(`(document.querySelector("h1") || {}).textContent || ""`); h == "" {
		t.Errorf("the error page has no heading")
	}

	p.Eval(`history.back(); true`)
	p.WaitFor(`location.pathname === "/leaderboard" && !!document.querySelector(".topbar")`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("going back from the error page reloaded the document")
	}
}

// On a phone the page menu opens from the capsule, closes on a press
// anywhere outside it, and gives way to another menu.
//
// A press outside is app.js's — a <details> closes on its own summary and
// nothing else — so it is pressed at a point on the page, well clear of the
// menu, the way a finger would.
func TestBrowserThePageMenuClosesOnAPressOutsideIt(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/leaderboard")

	const isOpen = `document.querySelector("details.bar-menu").open`
	p.Click("details.bar-menu > summary")
	p.WaitFor(isOpen)
	if n := p.Number(`[...document.querySelectorAll(".bar-menu-panel a[href]")].filter(a => a.getClientRects().length).length`); n != 5 {
		t.Fatalf("the open page menu shows %v pages, want 5", n)
	}

	// The bottom-right corner: the menu hangs from the top left.
	w, h := int(p.Number(`innerWidth`)), int(p.Number(`innerHeight`))
	p.ClickAt(w-8, h-8)
	p.WaitFor(`!` + isOpen)
	if path := p.Path(); path != "/leaderboard" {
		t.Errorf("the press outside the menu went to %s", path)
	}

	// Opening another menu closes it: they share a name.
	p.Click("details.bar-menu > summary")
	p.WaitFor(isOpen)
	p.Click("details.account > summary")
	p.WaitFor(`document.querySelector("details.account").open`)
	if p.Eval(isOpen) == true {
		t.Errorf("the page menu stayed open behind the account menu")
	}
}

// The search overlay opens from the keyboard, answers as you type, and Esc
// puts focus back where it came from.
//
// The one behaviour the repository's instructions named as having nothing
// to fall back to and no test: it is script or nothing, and the shortcut
// is the whole point. A hit is then a link like any other, so following
// one switches the page rather than reloading it.
func TestBrowserTheSearchOverlayAnswersTheKeyboard(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; true`)
	name := p.String(`document.querySelector(".board-card a.player").textContent.trim()`)

	const shown = `!document.getElementById("search-overlay").hidden`
	const hits = `document.querySelectorAll("#search-overlay .search-hit").length > 0`

	p.Press("k", "KeyK", 2) // Ctrl+K; the shortcut takes either modifier.
	p.WaitFor(shown)
	if p.Eval(`document.activeElement === document.querySelector(".search-overlay-input")`) != true {
		t.Errorf("the overlay opened without focusing its input")
	}
	p.Type(name)
	p.WaitFor(hits)

	p.Press("Escape", "Escape", 0)
	p.WaitFor(`!` + shown)
	if p.Eval(`document.activeElement === document.querySelector(".bar-search")`) != true {
		t.Errorf("closing the overlay did not put focus back on the search button")
	}
	if path := p.Path(); path != "/leaderboard" {
		t.Errorf("the overlay navigated to %s", path)
	}

	p.Press("k", "KeyK", 2)
	p.WaitFor(shown)
	p.Type(name)
	p.WaitFor(hits)
	p.Click("#search-overlay .search-hit")
	p.WaitFor(`location.pathname.startsWith("/players/")`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("following a search hit reloaded the document")
	}
}

// Following a theme link changes the theme without a reload.
//
// The theme lives on <html>, outside the body the switcher replaces, so a
// switch has to carry it across by hand — the kind of line nothing in the
// markup pins. The attribute moves at the press now, ahead of the page, so
// its changing no longer says the page has arrived: the picker's marker is
// read only once the old content has gone, which is what a switch is.
func TestBrowserAThemeLinkChangesTheThemeInPlace(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; document.getElementById("main").__old = true; true`)

	before := p.String(`document.documentElement.dataset.theme || ""`)
	href := p.String(`document.querySelector(".seg a.seg-opt:not(.on)").getAttribute("href")`)
	p.Click(`.seg a.seg-opt:not(.on)`)
	p.WaitFor(fmt.Sprintf(`(document.documentElement.dataset.theme || "") !== %q`, before))
	p.WaitFor(`document.getElementById("main").__old !== true`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("the theme link reloaded the document")
	}
	if on := p.String(`document.querySelector(".seg a.seg-opt.on").getAttribute("href")`); on != href {
		t.Errorf("the theme picker marks %s, not the %s just chosen", on, href)
	}
}

// After a switch, focus lands somewhere a reader can use.
//
// The element that was pressed is gone with the body it was in, and focus
// left on a departing node falls to the body — a keyboard back at the top
// of the page with nothing to say it moved. Two controls are a place in
// the page rather than a step out of it and keep focus there: a page in the
// bar focuses the same page in the new bar, and a pill focuses the pill for
// the page that arrived. (A ranking rule redraws only the board, and keeps
// its own focus; see TestBrowserARankingRuleRedrawsTheBoardUnderTheMenu.)
// Everything else lands on the main region. None of this is in the markup.
func TestBrowserFocusLandsSomewhereUsefulAfterASwitch(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	// The active element's href, or what it is when it has none.
	const active = `(() => { const a = document.activeElement; if (!a) return "nothing";
		return a.getAttribute("href") || a.id || a.tagName.toLowerCase(); })()`

	for _, href := range barHrefs(p) {
		follow(p, href)
		p.WaitFor(fmt.Sprintf(`%s === %q`, active, href))
	}

	// A link inside the page: the main region.
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`document.querySelector("main").__stale = true; true`)
	p.Click(".board-card a.player")
	p.WaitFor(`location.pathname.startsWith("/players/") && !(document.querySelector("main") || {}).__stale`)
	p.WaitFor(active + ` === "main"`)

	// A pill: the pill for the page that arrived, so the next Tab moves on
	// along the row from where the reader is.
	p.Navigate(site.base + "/players/harda")
	p.Eval(`document.querySelector("main").__stale = true; true`)
	p.Click(".pills a.pill:not(.on)")
	p.WaitFor(`!(document.querySelector("main") || {}).__stale`)
	p.WaitFor(`document.activeElement === document.querySelector(".pills a.pill.on")`)

	// An admin tab, the same way.
	p.Navigate(site.base + "/admin/settings")
	p.Eval(`document.querySelector("main").__stale = true; true`)
	p.Click(".admin-tabs a.admin-tab:not(.on)")
	p.WaitFor(`!(document.querySelector("main") || {}).__stale`)
	p.WaitFor(`document.activeElement === document.querySelector(".admin-tabs a.admin-tab.on")`)
}

// The bar is glass that frosts the page under it, and nothing is named for
// the view transition.
//
// The two are one test because the second is what breaks the first. An
// element with a view-transition-name is a backdrop root: a backdrop-filter
// inside it sees only what is inside it, so a named bar frosts its own empty
// box and the page shows through it sharp — which is how it first shipped.
// A tidy-up that names the bar again, to hold it still, would pass every
// test that reads the stylesheet. So this asks the browser what it made of
// both.
func TestBrowserTheBarIsGlassAndNothingIsNamed(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	// <html> is "root" by the browser's own stylesheet: the whole window,
	// which is the one thing meant to fade.
	named := p.Strings(`[...document.querySelectorAll("body *")]
		.filter(el => { const n = getComputedStyle(el).viewTransitionName; return n && n !== "none"; })
		.map(el => el.tagName.toLowerCase() + "." + el.className)`)
	if len(named) > 0 {
		t.Errorf("named for the view transition, which stops the glass inside them frosting: %v", named)
	}
	for _, sel := range []string{".bar-pages", ".bar-search", "details.account > summary"} {
		filter := p.String(fmt.Sprintf(`(() => { const s = getComputedStyle(document.querySelector(%q), "::before");
			return s.backdropFilter || s.webkitBackdropFilter || ""; })()`, sel))
		if !strings.Contains(filter, "blur(") {
			t.Errorf("%s is not frosted: backdrop-filter %q", sel, filter)
		}
	}
	// And the bar is its pieces: nothing behind them across the window.
	if bg := p.String(`getComputedStyle(document.querySelector(".topbar")).backgroundColor`); bg != "rgba(0, 0, 0, 0)" {
		t.Errorf("the bar has a ground of its own, %s, where the page should show through", bg)
	}
}

// Nothing fixed spans the top of the window. A full-width fixed layer there
// — a scroll edge band behind the bar was one — turned the area under an
// iPhone's clock solid in Safari, where the page should run up under it.
// The bar's pieces are inset from both edges, so they are not one. Asked of
// every element and its two pseudo-elements, scrolled, on a phone and a
// wide window.
func TestBrowserNothingFixedSpansTheTopOfTheWindow(t *testing.T) {
	site := newSite(t)
	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(newBrowser(t), width)
		p.Viewport(width, 360, width < 500)
		p.Navigate(site.base + "/leaderboard")
		p.Eval(`window.scrollTo(0, 120); true`)
		p.WaitFor(`window.scrollY >= 100`)
		spans := p.Strings(`(() => {
			const W = document.documentElement.clientWidth, out = [];
			for (const el of document.querySelectorAll("body *")) {
				if (!el.checkVisibility()) continue;
				for (const pseudo of [null, "::before", "::after"]) {
					const s = getComputedStyle(el, pseudo);
					if (s.position !== "fixed" || s.display === "none" || (pseudo && s.content === "none")) continue;
					const left = parseFloat(s.left), top = parseFloat(s.top);
					const w = pseudo ? parseFloat(s.width) : el.getBoundingClientRect().width;
					const y = pseudo ? top : el.getBoundingClientRect().top;
					if (w >= W - 1 && (pseudo ? left <= 0 : el.getBoundingClientRect().left <= 0) && y < 100) {
						out.push(el.tagName.toLowerCase() + "." + el.className + (pseudo || ""));
					}
				}
			}
			return out;
		})()`)
		if len(spans) > 0 {
			t.Errorf("width %d: fixed across the top of the window, where Safari fills the status bar with it: %v", width, spans)
		}
	}
}

// The page scrolls under the bar rather than stopping at an edge below it:
// there is no strip reserved for it. Scrolled, what is under the gap between
// the capsule and the search field is the page.
func TestBrowserThePageScrollsUnderTheBar(t *testing.T) {
	site := newSite(t)
	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(newBrowser(t), width)
		// Short enough that the board scrolls.
		p.Viewport(width, 360, width < 500)
		p.Navigate(site.base + "/leaderboard")
		p.Eval(`window.scrollTo(0, 120); true`)
		p.WaitFor(`window.scrollY >= 100`)
		hit := p.String(`(() => {
			const bar = document.querySelector(".topbar").getBoundingClientRect();
			const lead = [...document.querySelectorAll(".bar-pages, .bar-menu")].find(e => e.getClientRects().length).getBoundingClientRect();
			const el = document.elementFromPoint(lead.right + 4, bar.top + bar.height / 2);
			return el ? (el.closest("main") ? "main" : el.closest(".topbar") ? "bar" : el.tagName.toLowerCase()) : "nothing";
		})()`)
		if hit != "main" {
			t.Errorf("width %d: beside the capsule, scrolled, the press lands on %q, want the page", width, hit)
		}
	}
}

// Nothing sits at the bottom of a phone's window: that is where Safari keeps
// its address bar and toolbar, and a control there would be under them.
// Only what is drawn counts: About's sheet rises from the bottom once it is
// opened, as the design has it, and lies in a closed <details> until then.
// A toast hangs under the bar instead of floating over the foot, as the
// design would have it.
func TestBrowserTheBottomOfAPhoneIsEmpty(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	for _, path := range []string{"/today", "/leaderboard", "/players", "/admin/pending", "/settings?notice=name"} {
		p.Navigate(site.base + path)
		pinned := p.Strings(`[...document.querySelectorAll("body *")].filter(el => {
			const s = getComputedStyle(el);
			if (s.position !== "fixed" && s.position !== "sticky") return false;
			if (!el.checkVisibility()) return false;
			const r = el.getBoundingClientRect();
			return r.width && r.height && r.bottom > innerHeight - 120 && r.top < innerHeight;
		}).map(el => el.tagName.toLowerCase() + "." + el.className)`)
		if len(pinned) > 0 {
			t.Errorf("%s pins %v to the bottom of a phone", path, pinned)
		}
	}
}

// A toast's close is a finger's width, and pressing it twice stays on the
// page. On a phone the toast hangs over the admin head, where the way back
// to the admin area is: the second press, or one that lands as the toast
// goes, falls on whatever is under the close, and that must not be a link
// stretched across the row past its words.
func TestBrowserClosingAToastStaysOnThePage(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/admin/players/harda?notice=invited")
	got := p.String(`(() => { const r = document.querySelector(".toast-close").getBoundingClientRect();
		return [r.width, r.height, r.left + r.width / 2, r.top + r.height / 2].map(Math.round).join(" "); })()`)
	var w, h, x, y int
	fmt.Sscan(got, &w, &h, &x, &y)
	if w < 44 || h < 44 {
		t.Errorf("the toast's close is %dx%d, want at least 44x44", w, h)
	}
	p.ClickAt(x, y)
	p.WaitFor(`!document.querySelector(".toast")`)
	// The swap's view transition covers the page until it ends, and a
	// press before then hits nothing; wait for the page to be under the
	// finger again.
	p.WaitFor(fmt.Sprintf(`document.elementFromPoint(%d, %d) !== document.documentElement`, x, y))
	p.ClickAt(x, y)
	// Long enough for a link's swap to have landed, had it been one.
	time.Sleep(300 * time.Millisecond)
	if path := p.String(`location.pathname`); path != "/admin/players/harda" {
		t.Errorf("pressing where the close was took the page to %s", path)
	}
}

// The bar fits its window in every language: the capsule of pages and the
// search control never meet, and the account stays on screen. The labels
// are the page names in both languages, and "Topplista" is not
// "Leaderboard".
func TestBrowserTheBarFitsInEveryLanguage(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)
	// The two tightest widths are just above each breakpoint: 1181px is the
	// narrowest with the search field at full width, 861px the narrowest with
	// every view in the capsule.
	for _, width := range []int{desktopWidth, 1181, 861} {
		p := site.open(b, width)
		for _, lang := range []string{"en", "sv"} {
			p.Navigate(site.base + "/leaderboard?lang=" + lang)
			got := p.String(`(() => {
				const r = s => document.querySelector(s).getBoundingClientRect();
				const pages = r(".bar-pages"), search = r(".bar-search"), account = r("details.account > summary");
				return [Math.round(search.left - pages.right), Math.round(innerWidth - account.right)].join(" ");
			})()`)
			var gap, edge int
			fmt.Sscan(got, &gap, &edge)
			if gap < 12 {
				t.Errorf("width %d %s: the capsule and search are %dpx apart", width, lang, gap)
			}
			if edge < 0 {
				t.Errorf("width %d %s: the account is %dpx off the window", width, lang, -edge)
			}
		}
	}
}

// The pill for the page that is open is in view when the page arrives.
//
// A row of names wider than a phone arrives scrolled to its start, which
// can leave the player asked for off the right edge of the row that is
// meant to say where you are. app.js scrolls the row — the row, and not the
// page, which scrollIntoView would have moved too.
func TestBrowserTheCurrentPillIsInView(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), 320)
	// The last of the roster, so the row has somewhere to scroll to.
	p.Navigate(site.base + "/players")
	last := p.String(`[...document.querySelectorAll(".pills a.pill")].pop().getAttribute("href")`)
	p.Navigate(site.base + last)
	p.WaitFor(`!!document.querySelector(".pills a.pill.on")`)
	if wide := p.Eval(`(() => { const r = document.querySelector(".pills"); return r.scrollWidth > r.clientWidth; })()`); wide != true {
		t.Skip("the roster fits a 320px row; nothing to scroll")
	}
	got := p.String(`(() => {
		const row = document.querySelector(".pills").getBoundingClientRect();
		const pill = document.querySelector(".pills a.pill.on").getBoundingClientRect();
		return (pill.left >= row.left - 1 && pill.right <= row.right + 1 ? "in view" : "out of view") + " " + window.scrollY;
	})()`)
	if got != "in view 0" {
		t.Errorf("the current pill is %s (want in view, with the page not scrolled)", got)
	}
}

// A reader who has asked for reduced motion gets no view transition at all.
//
// The stylesheet zeroes the animation, but a transition the browser starts
// still pauses the page to take its pictures; app.js cancels it before that
// on htmx:beforeTransition. Emulated here, with the browser's own function
// counted rather than the animation observed, because "not started" is the
// claim.
func TestBrowserReducedMotionSkipsTheTransition(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.call("Emulation.setEmulatedMedia", map[string]any{
		"features": []map[string]string{{"name": "prefers-reduced-motion", "value": "reduce"}},
	})
	p.Navigate(site.base + "/today")
	if reduced := p.Eval(`matchMedia("(prefers-reduced-motion: reduce)").matches`); reduced != true {
		t.Skip("the browser does not emulate prefers-reduced-motion")
	}
	p.Eval(`(() => {
		window.__transitions = 0;
		const start = document.startViewTransition;
		if (!start) return false;
		document.startViewTransition = function () { window.__transitions++; return start.apply(this, arguments); };
		document.getElementById("main").__old = true;
		return true;
	})()`)

	p.Click(`.bar-pages a.bar-page[href="/leaderboard"]`)
	p.WaitFor(`document.getElementById("main").__old !== true && location.pathname === "/leaderboard"`)
	if n := p.Number(`window.__transitions`); n != 0 {
		t.Errorf("a switch under reduced motion started %v view transitions", n)
	}
}

// Posting a form does not reload the document either.
//
// The old switcher took links only; htmx boosts forms as well, so signing
// out, saving a name or answering a question all arrive without the flash.
// Sign out is the form on every page: it posts, the server redirects to
// the sign-in card, and the card is swapped in with the same window.
func TestBrowserSubmittingAFormDoesNotReload(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/settings/account")
	p.Eval(`window.__alive = 1; true`)

	p.Eval(`document.querySelector("details.account").open = true; true`)
	p.Click("details.account form button")
	p.WaitFor(`location.pathname === "/" && !!document.querySelector(".auth-frame")`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("signing out reloaded the document")
	}
	if n := p.Number(`document.querySelectorAll(".topbar").length`); n != 0 {
		t.Errorf("the sign-in card arrived with the application's bar")
	}
}

// A result that lands while Today or the board is open appears on the page
// without a reload.
//
// The stream carries a mark, the page fetches itself again and swaps its
// content: what a reader sees is a name that was not there arriving in the
// day's list, and the board redrawing, with the same window throughout and
// nothing in the console.
func TestBrowserAResultAppearsWithoutAReload(t *testing.T) {
	site := newSite(t)
	site.srv.live.interval = 100 * time.Millisecond
	current := wordle.PuzzleForDate(time.Now())
	b := newBrowser(t)

	// Today: the player who had not filed appears in the results.
	p := site.open(b, desktopWidth)
	p.Navigate(site.base + "/today")
	p.Eval(`window.__alive = 1; true`)
	if p.Eval(`[...document.querySelectorAll(".result-name")].some(a => a.textContent.trim() === "Lapsed")`) == true {
		t.Fatal("the player this test files for has already filed today")
	}
	file(t, site.srv, "lapsed", current, 4)
	p.WaitFor(`[...document.querySelectorAll(".result-name")].some(a => a.textContent.trim() === "Lapsed")`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Error("the result arrived by reloading the document")
	}
	if errs := p.Errors(); len(errs) > 0 {
		t.Errorf("console: %s", strings.Join(errs, "; "))
	}

	// The board: its content is redrawn, and the page is otherwise as it was.
	q := site.open(b, desktopWidth)
	q.Navigate(site.base + "/leaderboard")
	q.Eval(`window.__alive = 1; document.querySelector("main .board-card").__stale = true; true`)
	file(t, site.srv, "lapsed", current-1, 3)
	q.WaitFor(`!(document.querySelector("main .board-card") || {}).__stale`)
	if alive := q.Number(`window.__alive || 0`); alive != 1 {
		t.Error("the board redrew by reloading the document")
	}
	if path := q.Path(); path != "/leaderboard" {
		t.Errorf("the board's address changed to %s", path)
	}
	if errs := q.Errors(); len(errs) > 0 {
		t.Errorf("console: %s", strings.Join(errs, "; "))
	}
}

// Visiting Today again and again leaves one stream open, not one per visit.
//
// The browser keeps a page navigated away from in its back-forward cache,
// stream and all; six such pages and there is no connection left for the
// next request to this host, and the seventh visit never loads. The
// stream is closed as its page is hidden, and this counts what the server
// still holds after each visit.
func TestBrowserVisitingTodayAgainKeepsOneStream(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	for i := 0; i < 8; i++ {
		p.Navigate(fmt.Sprintf("%s/today?n=%d", site.base, i))
		// The stream opens once the page has run its script; give the old
		// page's close and the new page's open a moment to land.
		p.WaitFor(`!!document.querySelector("[sse-connect]")`)
		deadline := time.Now().Add(2 * time.Second)
		for {
			site.srv.live.mu.Lock()
			n := len(site.srv.live.subs)
			site.srv.live.mu.Unlock()
			if n <= 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("after visit %d the server holds %d streams for one tab", i+1, n)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// The eyebrow over a player's name fits a phone, and carries the rank and
// the last-played date.
//
// Rank and last-played together once ran past a phone's bar and were
// cropped mid-word; the eyebrow is short enough now to keep both.
func TestBrowserThePlayerSubtitleFitsOnAPhone(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	const probe = `(() => {
		const h = document.querySelector(".page-eyebrow");
		if (!h) return "none";
		return (h.scrollWidth > h.clientWidth ? "cropped" : "fits") + " | " + h.innerText.trim();
	})()`

	p := site.open(b, phoneWidth)
	p.Navigate(site.base + "/players")
	got := p.String(probe)
	if !strings.HasPrefix(got, "fits | ") {
		t.Errorf("phone: the eyebrow is %q", got)
	}
	if !strings.Contains(got, "·") {
		t.Errorf("phone: the eyebrow lost the last-played date: %q", got)
	}

	q := site.open(b, desktopWidth)
	q.Navigate(site.base + "/players")
	if got := q.String(probe); !strings.Contains(got, "·") {
		t.Errorf("desktop: the eyebrow lost the last-played date: %q", got)
	}
}

// On a phone the sign-in page is the form and nothing else: the group's
// figures and the note about the group chat stay on a wide screen, where
// there is room beside the form, and the way out of a forgotten password
// moves under the button. What is shown at a width is not in the markup.
func TestBrowserThePhoneSignInIsTheFormAlone(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)
	shown := func(p *page, sel string) bool {
		return p.Eval(fmt.Sprintf(`(() => { const el = document.querySelector(%q); return !!el && el.getClientRects().length > 0; })()`, sel)) == true
	}
	for _, width := range []int{phoneWidth, desktopWidth} {
		p := b.newPage()
		p.Viewport(width, 900, width < 500)
		p.Navigate(site.base + "/")
		phone := width == phoneWidth
		for sel, want := range map[string]bool{
			".signin-aside":       !phone,
			".signin-note":        !phone,
			".signin-help-wide":   !phone,
			".signin-help-narrow": phone,
			".signin-form":        true,
		} {
			if got := shown(p, sel); got != want {
				t.Errorf("width %d: %s shown = %v, want %v", width, sel, got, want)
			}
		}
	}
}

// Pressing a name on the grid picks out that player's column and dims the
// rest; pressing it again lets it go. It is radio buttons and a stylesheet,
// no script — and which cells end up dimmed is the one thing no reading of
// the markup can tell.
func TestBrowserTheGridPicksOutAColumn(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/grid")

	const opacities = `(() => {
		const col = i => [...document.querySelectorAll(".grid tbody td.gc-" + i + " .grid-tile")].map(e => getComputedStyle(e).opacity);
		return JSON.stringify({first: [...new Set(col(0))], second: [...new Set(col(1))]});
	})()`
	type seen struct{ First, Second []string }
	read := func() seen {
		var s seen
		// The opacity eases over .15s; wait for it to land.
		time.Sleep(300 * time.Millisecond)
		if err := json.Unmarshal([]byte(p.String(opacities)), &s); err != nil {
			t.Fatalf("reading opacities: %v", err)
		}
		return s
	}

	if got := read(); len(got.First) != 1 || got.First[0] != "1" || len(got.Second) != 1 || got.Second[0] != "1" {
		t.Fatalf("before a press the columns are %+v, want every tile at full strength", got)
	}
	p.Click(`th.gc-0 label.hi-on`)
	if got := read(); len(got.First) != 1 || got.First[0] != "1" || len(got.Second) != 1 || got.Second[0] == "1" {
		t.Errorf("with the first column picked the columns are %+v, want it full and the second dimmed", got)
	}
	p.Click(`th.gc-0 label.hi-off`)
	if got := read(); len(got.Second) != 1 || got.Second[0] != "1" {
		t.Errorf("pressing the name again left the columns at %+v, want them all back", got)
	}
}

// A rule ticked in the ranking menu redraws the board under the menu and
// nothing else: the menu is the same element, still open; the address is
// the board's own, not the form's; nothing cross-fades — Safari drew the
// fade without the menu's frosted glass — and nothing reloads. Neither a
// rule nor the reset leaves a toast. A rule set from the keyboard keeps the focus on its
// box, though the menu's rows are redrawn with the board.
func TestBrowserARankingRuleRedrawsTheBoardUnderTheMenu(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/leaderboard")
	// The number of view transitions started since the page loaded.
	p.Eval(`(() => { const start = document.startViewTransition.bind(document);
		window.__fades = 0;
		document.startViewTransition = (...a) => { window.__fades++; return start(...a); }; return true; })()`)
	p.Eval(`window.__alive = 1;
		const menu = document.querySelector("details.ranking");
		menu.open = true; menu.__same = true;
		document.querySelector("#board-view").__stale = true; true`)

	p.Click(`label:has(#rule-missed)`)
	p.WaitFor(`!(document.querySelector("#board-view") || {}).__stale`)
	p.WaitFor(`location.search === "?missed=1"`)
	if p.Eval(`(() => { const m = document.querySelector("details.ranking"); return !!(m && m.__same && m.open); })()`) != true {
		t.Error("ticking a rule replaced or shut the ranking menu")
	}
	if p.Eval(`document.querySelector("#rule-missed").checked`) != true {
		t.Error("the rule just ticked is not ticked in the redrawn menu")
	}
	if got := p.String(`document.querySelector(".ranking-state").textContent`); got != "Custom" {
		t.Errorf("the menu's button says %q after a rule changed, want Custom", got)
	}
	if p.Number(`window.__alive || 0`) != 1 {
		t.Error("ticking a rule reloaded the document")
	}
	if n := p.Number(`window.__fades`); n != 0 {
		t.Errorf("ticking a rule cross-faded the page (%v transitions)", n)
	}
	if p.Eval(`!!document.querySelector(".toast")`) != false {
		t.Error("a rule left a toast")
	}

	// From the keyboard: the box keeps the focus through the redraw.
	p.Eval(`document.querySelector("#board-view").__stale = true; document.querySelector("#rule-mode").focus(); true`)
	p.Press(" ", "Space", 0)
	p.WaitFor(`!(document.querySelector("#board-view") || {}).__stale`)
	p.WaitFor(`location.search === "?missed=1&mode=hard" || location.search === "?mode=hard&missed=1"`)
	if got := p.String(`document.activeElement ? document.activeElement.id : ""`); got != "rule-mode" {
		t.Errorf("focus went to %q after a rule set from the keyboard, want rule-mode", got)
	}

	// The reset: the menu stays and the rules go back, with no toast — the
	// menu shows every rule at its default.
	p.Eval(`document.querySelector("#board-view").__stale = true; true`)
	p.Click(".ranking-reset")
	p.WaitFor(`!(document.querySelector("#board-view") || {}).__stale`)
	if p.Eval(`!!document.querySelector(".toast")`) != false {
		t.Error("the reset left a toast")
	}
	if p.Eval(`(() => { const m = document.querySelector("details.ranking"); return !!(m && m.__same && m.open); })()`) != true {
		t.Error("the reset replaced or shut the ranking menu")
	}
	if p.Eval(`document.querySelector("#rule-missed").checked || document.querySelector("#rule-mode").checked`) != false {
		t.Error("the reset left a rule ticked")
	}
	if p.Number(`window.__alive || 0`) != 1 {
		t.Error("the reset reloaded the document")
	}
}

// The board's other controls redraw it in place as the ranking menu does:
// the range, a column's sort, a row's ⇄ and the head-to-head's close. The
// head is the same element throughout, what depends on the query comes
// along (the eyebrow, the menu's hidden fields), nothing reloads and
// nothing cross-fades.
func TestBrowserTheBoardsControlsRedrawItInPlace(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`(() => { const start = document.startViewTransition.bind(document);
		window.__fades = 0;
		document.startViewTransition = (...a) => { window.__fades++; return start(...a); };
		window.__alive = 1;
		document.querySelector(".page-head").__same = true; return true; })()`)
	// press follows a control and checks the board came back under the
	// same head, at the address the control names.
	press := func(selector string) {
		t.Helper()
		want := p.String(fmt.Sprintf(`new URL(document.querySelector(%q).href).search`, selector))
		p.Eval(`document.querySelector("#board-view").__stale = true; true`)
		p.Click(selector)
		p.WaitFor(`!(document.querySelector("#board-view") || {}).__stale`)
		if got := p.String(`location.search`); got != want {
			t.Errorf("%s: the address is %q, want %q", selector, got, want)
		}
		if p.Eval(`!!(document.querySelector(".page-head") || {}).__same`) != true {
			t.Errorf("%s replaced the page head", selector)
		}
	}

	press("#board-ranges a:not(.on)")
	if got := p.String(`document.querySelector("#board-eyebrow").textContent`); !strings.Contains(got, "90") {
		t.Errorf("the eyebrow says %q after the range changed to 90 days", got)
	}
	if p.Eval(`!!document.querySelector('#ranking-panel input[type=hidden][name=range][value="90"]')`) != true {
		t.Error("the ranking form does not carry the range just chosen")
	}

	press("#sort-games")
	if got := p.String(`document.querySelector("#sort-games").closest("[aria-sort]").getAttribute("aria-sort")`); got == "none" {
		t.Error("the column just sorted by is not marked as sorted")
	}

	press(".b-rows a.b-cmp")
	if p.Eval(`!!document.querySelector(".h2h")`) != true {
		t.Error("pressing a row's ⇄ did not open the head-to-head")
	}
	press("#h2h-close")
	if p.Eval(`!!document.querySelector(".h2h")`) != false {
		t.Error("closing the head-to-head left it open")
	}

	if p.Number(`window.__alive || 0`) != 1 {
		t.Error("a board control reloaded the document")
	}
	if n := p.Number(`window.__fades`); n != 0 {
		t.Errorf("a board control cross-faded the page (%v transitions)", n)
	}
}

// The grid's window and its inactive players' switch redraw the grid in
// place, as the board's controls do: the head stays, the eyebrow follows
// the window, nothing reloads or cross-fades, and the address is the one
// the control names.
func TestBrowserTheGridsControlsRedrawItInPlace(t *testing.T) {
	site := newSite(t)
	// Somebody who played this week and has since left the group, so the
	// inactive players' switch has someone to show.
	ctx := context.Background()
	gone, err := store.CreatePlayer(ctx, site.srv.db, store.SystemActor(), "Gone", "gone")
	if err != nil {
		t.Fatal(err)
	}
	seedResult(t, site.srv, gone.ID, currentPuzzle()-2, 4, false)
	left := false
	if _, err := store.UpdatePlayer(ctx, site.srv.db, store.SystemActor(), gone.ID, store.PlayerUpdate{Active: &left}); err != nil {
		t.Fatal(err)
	}
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/grid")
	p.Eval(`(() => { const start = document.startViewTransition.bind(document);
		window.__fades = 0;
		document.startViewTransition = (...a) => { window.__fades++; return start(...a); };
		window.__alive = 1;
		document.querySelector(".page-head").__same = true; return true; })()`)
	press := func(selector string) {
		t.Helper()
		want := p.String(fmt.Sprintf(`new URL(document.querySelector(%q).href).search`, selector))
		p.Eval(`document.querySelector("#grid-view").__stale = true; true`)
		p.Click(selector)
		p.WaitFor(`!(document.querySelector("#grid-view") || {}).__stale`)
		if got := p.String(`location.search`); got != want {
			t.Errorf("%s: the address is %q, want %q", selector, got, want)
		}
		if p.Eval(`!!(document.querySelector(".page-head") || {}).__same`) != true {
			t.Errorf("%s replaced the page head", selector)
		}
	}

	press("#grid-inactive")
	if p.Eval(`document.querySelector("#grid-inactive").classList.contains("on")`) != true {
		t.Error("the inactive players' switch is not on after it was pressed")
	}
	if p.Eval(`!!document.querySelector('.grid th[title="Gone"]')`) != true {
		t.Error("the inactive player is not in the grid after the switch")
	}

	before := p.String(`document.querySelector("#grid-eyebrow").textContent`)
	press("#grid-tools .head-seg a:not(.on)")
	if after := p.String(`document.querySelector("#grid-eyebrow").textContent`); after == before {
		t.Errorf("the eyebrow still says %q after the window changed", after)
	}

	if p.Number(`window.__alive || 0`) != 1 {
		t.Error("a grid control reloaded the document")
	}
	if n := p.Number(`window.__fades`); n != 0 {
		t.Errorf("a grid control cross-faded the page (%v transitions)", n)
	}
}

// Choosing a month redraws the months page in place: its pill, its column
// in the season, a tile in the season. The page's view is replaced, the
// document is not, nothing cross-fades, and the address is the one the
// control names.
func TestBrowserChoosingAMonthRedrawsThePageInPlace(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/months")
	p.Eval(`(() => { const start = document.startViewTransition.bind(document);
		window.__fades = 0;
		document.startViewTransition = (...a) => { window.__fades++; return start(...a); };
		window.__alive = 1; document.querySelector("main").__same = true; return true; })()`)
	for _, selector := range []string{"a.month-pill:not(.on)", "a.season-col:not(.on)", "a.season-tile:not(.on)"} {
		want := p.String(fmt.Sprintf(`new URL(document.querySelector(%q).href).search`, selector))
		p.Eval(`document.querySelector("#months-view").__stale = true; true`)
		p.Click(selector)
		p.WaitFor(`!(document.querySelector("#months-view") || {}).__stale`)
		if got := p.String(`location.search`); got != want {
			t.Errorf("%s: the address is %q, want %q", selector, got, want)
		}
		if p.Eval(`!!(document.querySelector("main") || {}).__same`) != true {
			t.Errorf("%s replaced the whole page rather than its view", selector)
		}
	}
	if p.Number(`window.__alive || 0`) != 1 {
		t.Error("choosing a month reloaded the document")
	}
	if n := p.Number(`window.__fades`); n != 0 {
		t.Errorf("choosing a month cross-faded the page (%v transitions)", n)
	}
}

// A view a control redraws in place is a wrapper for htmx to replace, not a
// box: the sections in it are spaced as the page's other sections are, by
// the main region's gap. A wrapper that boxed them drew them touching.
func TestBrowserAViewSpacesItsSectionsAsThePageDoes(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	for _, path := range []string{"/leaderboard?cmp=harda", "/months"} {
		p.Navigate(site.base + path)
		bad := p.Strings(`(() => {
			const main = document.querySelector("main");
			const gap = parseFloat(getComputedStyle(main).rowGap);
			const view = main.querySelector("[id$='-view']");
			if (!view) return ["no view on the page"];
			const kids = [...view.children].filter(el => el.getClientRects().length);
			if (kids.length < 2) return ["fewer than two sections in the view to measure"];
			const bad = [];
			for (let i = 1; i < kids.length; i++) {
				const d = kids[i].getBoundingClientRect().top - kids[i - 1].getBoundingClientRect().bottom;
				if (Math.abs(d - gap) > 1) bad.push(kids[i - 1].className + " to " + kids[i].className + ": " + d + "px, want " + gap);
			}
			return bad; })()`)
		for _, b := range bad {
			t.Errorf("%s: %s", path, b)
		}
	}
}

// A link that stays on the page swaps it and leaves the reader where they
// were: another player, a pair to compare, the range, a ranking rule, a month
// from the season. htmx would scroll a boosted swap to the top, which is
// right for a step to another page — and that still happens, from the same
// table, for a player's name.
func TestBrowserALinkToTheSamePageKeepsTheScroll(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)

	press := func(path, selector string) (before, after float64) {
		t.Helper()
		p.Navigate(site.base + path)
		p.Eval(`window.__alive = 1; window.scrollTo(0, Math.min(600, document.documentElement.scrollHeight - innerHeight)); true`)
		before = p.ScrollY()
		ok := p.Eval(fmt.Sprintf(`(() => {
			const link = document.querySelector(%q);
			if (!link) return false;
			document.querySelectorAll("main, main [id$='-view']").forEach(el => { el.__stale = true; });
			link.click(); return true; })()`, selector))
		if ok != true {
			t.Fatalf("%s: nothing matches %s", path, selector)
		}
		// The whole page swapped, or only the view a control redraws.
		p.WaitFor(`[...document.querySelectorAll("main, main [id$='-view']")].some(el => !el.__stale)`)
		if alive := p.Number(`window.__alive || 0`); alive != 1 {
			t.Errorf("%s %s: the document was reloaded", path, selector)
		}
		return before, p.ScrollY()
	}

	for _, tt := range []struct{ path, selector string }{
		{"/players/harda", ".pills a.pill:not(.on)"},
		{"/leaderboard", "a.b-cmp"},
		{"/leaderboard", ".head-seg a:not(.on)"},
		// Counting missed days rather than hard mode only, which would leave
		// the seeded board too short to have scrolled at all.
		{"/leaderboard", `label:has(#rule-missed)`},
		{"/grid", ".head-seg a:not(.on)"},
		{"/months", "a.season-tile:not(.on)"},
	} {
		before, after := press(tt.path, tt.selector)
		if before < 100 {
			t.Errorf("%s: not far enough down to tell (scrollY %v)", tt.path, before)
			continue
		}
		if after < before/2 {
			t.Errorf("%s %s: scrolled from %v to %v; want it to stay put", tt.path, tt.selector, before, after)
		}
	}

	if _, after := press("/months", ".season-name a.player"); after != 0 {
		t.Errorf("a player's name from the season opened at scrollY %v, want the top", after)
	}
}

// Focus placed after a swap is for the keyboard, and so is its ring. A pill
// pressed with the mouse keeps the focus for the next Tab but is not left
// circled — Safari draws a ring for any focus a script sets — while the same
// pill reached from the keyboard shows where the reader is.
func TestBrowserAPressedPillIsNotLeftCircled(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	const ring = `getComputedStyle(document.activeElement).outlineStyle`
	const onPill = `document.activeElement === document.querySelector(".pills a.pill.on")`

	p.Navigate(site.base + "/players/harda")
	x := int(p.Number(`(() => { const r = document.querySelector(".pills a.pill:not(.on)").getBoundingClientRect(); return r.left + r.width / 2; })()`))
	y := int(p.Number(`(() => { const r = document.querySelector(".pills a.pill:not(.on)").getBoundingClientRect(); return r.top + r.height / 2; })()`))
	p.Eval(`document.querySelector("main").__stale = true; true`)
	p.ClickAt(x, y)
	p.WaitFor(`!(document.querySelector("main") || {}).__stale`)
	p.WaitFor(onPill)
	if got := p.Eval(ring); got != "none" {
		t.Errorf("a pill pressed with the mouse is left with a %v outline", got)
	}

	p.Navigate(site.base + "/players/harda")
	p.Eval(`document.querySelector(".pills a.pill:not(.on)").focus(); document.querySelector("main").__stale = true; true`)
	p.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "text": "\r", "windowsVirtualKeyCode": 13})
	p.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13})
	p.WaitFor(`!(document.querySelector("main") || {}).__stale`)
	p.WaitFor(onPill)
	if got := p.Eval(ring); got == "none" {
		t.Error("a pill reached from the keyboard shows no ring")
	}
}

// A reader's settings are as wide as any other page's cards, and the pending
// page's empty state draws its glyph in the middle of its circle.
//
// The settings stack once capped itself narrower than the page, so its
// cards stood apart from every other page's; and the shared card gutter
// padded the empty state's icon circle too, leaving it less room than the
// glyph, which then spilled off to the right. Both are only visible in
// the laid-out page.
func TestBrowserSettingsCardsAreAsWideAsTheRest(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	const widths = `JSON.stringify([...document.querySelectorAll("main .card")].map(c => Math.round(c.getBoundingClientRect().width)))`
	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(b, width)
		p.Navigate(site.base + "/admin/settings")
		var ref []int
		_ = json.Unmarshal([]byte(p.String(widths)), &ref)
		if len(ref) == 0 {
			t.Fatalf("width %d: /admin/settings has no card to measure against", width)
		}
		p.Navigate(site.base + "/settings")
		var got []int
		_ = json.Unmarshal([]byte(p.String(widths)), &got)
		if len(got) == 0 {
			t.Fatalf("width %d: /settings has no cards", width)
		}
		for i, w := range got {
			if w != ref[0] {
				t.Errorf("width %d: settings card %d is %dpx wide, other pages' cards are %dpx", width, i, w, ref[0])
			}
		}
	}
}

func TestBrowserTheEmptyPendingGlyphIsCentred(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	const probe = `(() => {
		const icon = document.querySelector(".admin-empty-icon");
		if (!icon) return "";
		const c = icon.getBoundingClientRect(), g = icon.querySelector("svg").getBoundingClientRect();
		return JSON.stringify([c.left + c.width / 2 - (g.left + g.width / 2), c.top + c.height / 2 - (g.top + g.height / 2)]);
	})()`
	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(b, width)
		p.Navigate(site.base + "/admin/pending")
		raw := p.String(probe)
		if raw == "" {
			t.Fatalf("width %d: /admin/pending shows no empty state", width)
		}
		var off [2]float64
		_ = json.Unmarshal([]byte(raw), &off)
		if math.Abs(off[0]) > 0.5 || math.Abs(off[1]) > 0.5 {
			t.Errorf("width %d: the glyph is %.1fpx, %.1fpx off its circle's centre", width, off[0], off[1])
		}
	}
}
