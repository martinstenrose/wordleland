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
// read pages off the rail and the switcher rather than from a list, and they
// assert what a reader would notice — "no reload", "same place", "still
// here" — rather than how the script achieves it. Swap the script for
// another and these are the parity net.

import (
	"encoding/json"
	"fmt"
	"io"
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
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start chrome: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Ready when the DevTools endpoint answers.
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chrome did not come up on port %d: %v", port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &browser{t: t, port: port}
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

// railHrefs is every view the rail offers on the page that is open — the
// pages a reader can reach from anywhere. Read from the page, not listed here,
// so a view added later is tested without anyone remembering to add it.
//
// A view is an address. The rail also carries links that are this page with
// one parameter changed — collapsing itself, the About panel's privacy link —
// and those are controls, not places; anything with a query string or under
// /privacy is left out.
func railHrefs(p *page) []string {
	p.t.Helper()
	// The rail and the drawer carry the same rows; one copy is enough.
	return p.Strings(`[...new Set([...document.querySelectorAll(".sidebar a[href]")]
		.map(a => a.getAttribute("href"))
		.filter(h => h.startsWith("/") && !h.includes("?") && !h.startsWith("/privacy")))]`)
}

// follow presses the rail row for a view and waits until the page is there,
// opening the drawer first where the rail is hidden. The way a reader moves
// between views on a phone.
func follow(p *page, href string) {
	p.t.Helper()
	// Opening the drawer and pressing the row happen in one evaluation, and
	// the <main> that was on screen is marked before the press: the switch
	// is done when that node is gone, not when the address matches. The
	// address is no witness on its own — a row for the page already open
	// matches before its fetch has landed, and the next press would then
	// find the drawer it just opened swapped away. What ends a switch is
	// the old content leaving, whichever script does the swapping.
	ok := p.Eval(fmt.Sprintf(`(() => {
		[...document.querySelectorAll("details.drawer")].forEach(d => d.open = true);
		const row = [...document.querySelectorAll(".sidebar a[href], .drawer a[href]")]
			.find(a => a.getAttribute("href") === %q && a.getClientRects().length);
		if (!row) return false;
		const main = document.querySelector("main");
		if (main) main.__stale = true;
		row.click(); return true; })()`, href))
	if ok != true {
		p.t.Fatalf("follow: no visible rail row for %s", href)
	}
	// /players opens on the leader, so the address it lands on is a page
	// under it rather than the row's own.
	p.WaitFor(fmt.Sprintf(`!(document.querySelector("main") || {}).__stale
		&& (location.pathname === %q || location.pathname.startsWith(%q + "/"))`, href, href))
}

// ---- The assertions -------------------------------------------------------

// Following a link never reloads the document.
//
// The page switcher's whole promise. A counter set on window survives a
// switch and does not survive a load, so it is the one honest witness.
func TestBrowserFollowingALinkNeverReloads(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")
	p.Eval(`window.__alive = 1; true`)

	for _, href := range railHrefs(p) {
		follow(p, href)
		if alive := p.Number(`window.__alive || 0`); alive != 1 {
			t.Errorf("%s: the document was reloaded (window.__alive = %v)", href, alive)
		}
	}
	// A link inside a page, not only the rail: the first player on the board.
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; true`)
	p.Click(".board a.player")
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
	for _, href := range railHrefs(p) {
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
	for _, href := range railHrefs(p) {
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

	for _, href := range railHrefs(p) {
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
// every view — a title that is a menu included.
//
// Two bugs lived here. A glyph in front of a menu title pushed it 45px in.
// Then a line-height of 1.2 on the same title, against the default on the
// others, moved its glyphs 4px and the subtitle under it 8px while the box
// tops still matched — invisible to anything that reads the markup.
func TestBrowserTheTitleDoesNotMoveBetweenViews(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	const probe = `(() => {
		const card = document.querySelector("main section.card");
		const box = el => { if (!el) return "none";
			const r = el.getBoundingClientRect(), c = card.getBoundingClientRect(), s = getComputedStyle(el);
			return [Math.round(r.left - c.left), Math.round(r.top - c.top), s.fontSize, s.fontWeight, s.lineHeight, s.letterSpacing].join(" "); };
		return box(document.querySelector("main h1")) + " | " + box(document.querySelector("main .kicker"));
	})()`

	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(b, width)
		p.Navigate(site.base + "/leaderboard")

		got := map[string]string{}
		for _, href := range railHrefs(p) {
			// Today is the one view that does not name itself: it leads with
			// the date and sets the day's result larger than a page name,
			// because it is not one. Recorded in docs/decisions.md.
			if href == "/today" || href == "/" {
				continue
			}
			follow(p, href)
			got[p.Path()] = p.String(probe)
		}
		if len(got) < 3 {
			t.Fatalf("width %d: only %d views measured", width, len(got))
		}
		var want, wantPath string
		for path, g := range got {
			if want == "" {
				want, wantPath = g, path
				continue
			}
			if g != want {
				t.Errorf("width %d: %s is laid out\n  %s\nbut %s is\n  %s", width, path, g, wantPath, want)
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
// were measured, and at 1000px with the rail out it had nothing at all.
// Nothing that reads the markup can see any of that. Column labels are
// clipped rather than wrapped, for the same reason, so a label wider than
// its column is silently cut; every one of the five languages is checked.
func TestBrowserTodaysTwoListsShareARhythm(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	const probe = `(() => {
		const q = s => document.querySelector(s);
		const box = e => e.getBoundingClientRect();
		const clipped = [...document.querySelectorAll(".result-row.head > *, .form-row.head > *")]
			.filter(e => e.scrollWidth > e.clientWidth + 1).map(e => e.textContent.trim());
		return JSON.stringify({
			name: Math.round(box(q(".form-row:not(.head) .form-name")).width),
			result: Math.round(box(q(".result-row:not(.head)")).height),
			form: Math.round(box(q(".form-row:not(.head)")).height),
			sideBySide: box(q(".today-results")).top === box(q(".today-form")).top,
			clipped,
		});
	})()`
	type layout struct {
		Name, Result, Form int
		SideBySide         bool
		Clipped            []string
	}

	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(b, width)
		for _, lang := range []string{"en", "sv", "de", "it", "es"} {
			p.Navigate(site.base + "/today?lang=" + lang)
			var got layout
			if err := json.Unmarshal([]byte(p.String(probe)), &got); err != nil {
				t.Fatalf("width %d %s: %v", width, lang, err)
			}
			if got.Name < 56 {
				t.Errorf("width %d %s: the form row leaves the name %dpx", width, lang, got.Name)
			}
			if got.Result != got.Form {
				t.Errorf("width %d %s: a results row is %dpx tall and a form row %dpx", width, lang, got.Result, got.Form)
			}
			if got.SideBySide != (width == desktopWidth) {
				t.Errorf("width %d %s: side by side = %v", width, lang, got.SideBySide)
			}
			if len(got.Clipped) > 0 {
				t.Errorf("width %d %s: column labels wider than their column: %v", width, lang, got.Clipped)
			}
		}
	}
}

// Opening the list of who has not filed leaves the day's headline where it
// was.
//
// On a wide screen the header's two groups stand side by side. Aligned to
// the bottom, the left one slid down when the right one grew: the reader
// pressed a small control on the right and the headline on the left moved.
// Nothing that reads the markup can see which edge a row is aligned on.
func TestBrowserOpeningTheMissingListLeavesTheHeadlineStill(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	const top = `document.querySelector(".today-headline h1").getBoundingClientRect().top`
	before := p.Number(top)
	p.Click(".today-out > summary")
	p.WaitFor(`document.querySelector(".today-out").open`)
	if after := p.Number(top); after != before {
		t.Errorf("the headline moved from %v to %v when the list opened", before, after)
	}
}

// The enrolment dialog opens over the settings screen, holds focus, and
// closes without going anywhere.
//
// Setting a secret up is a step in what the reader is doing on that screen,
// not a place to go; a dialog that let Tab wander out of it into the page
// behind would be worse than the page it replaced.
func TestBrowserTheEnrolmentDialogStaysOnThePage(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/settings/security")
	p.Eval(`window.__alive = 1; true`)
	start := p.Path()

	p.Click("a[data-modal]")
	p.WaitFor(`!!document.querySelector(".modal-card")`)
	if got := p.Path(); got != start {
		t.Fatalf("opening the dialog navigated to %s", got)
	}
	if p.Eval(`document.querySelector(".modal-card").contains(document.activeElement)`) != true {
		t.Error("focus did not move into the dialog")
	}

	// Tab from the last control wraps to the first, and Shift+Tab back.
	p.Eval(`(() => { const f = [...document.querySelector(".modal-card").querySelectorAll('a[href], button, input')]
		.filter(e => e.getClientRects().length); f[f.length - 1].focus(); return true; })()`)
	p.Eval(`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Tab", bubbles: true})); true`)
	if p.Eval(`(() => { const f = [...document.querySelector(".modal-card").querySelectorAll('a[href], button, input')]
		.filter(e => e.getClientRects().length); return document.activeElement === f[0]; })()`) != true {
		t.Error("Tab from the last control did not wrap to the first: focus can leave the dialog")
	}

	p.Click(".modal-card a.btn.secondary") // Cancel
	p.WaitFor(`!document.querySelector(".modal-card")`)
	if got := p.Path(); got != start {
		t.Errorf("closing the dialog navigated to %s", got)
	}
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Error("the dialog's round trip reloaded the document")
	}
	if p.Eval(`document.documentElement.style.overflow`) != "" {
		t.Error("the page behind is still scroll-locked after the dialog closed")
	}
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
	for _, href := range railHrefs(p) {
		visit(href)
	}
	// Inside the admin area, every section the bar lists.
	p.Navigate(site.base + "/admin/settings")
	for _, href := range p.Strings(`[...document.querySelectorAll(".switcher-panel a")].map(a => a.getAttribute("href"))`) {
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
func TestBrowserAMissingPageKeepsTheRailOff(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; true`)

	p.Eval(`document.querySelector("main").insertAdjacentHTML("afterbegin",
		'<a id="__missing" href="/no-such-page">nowhere</a>'); true`)
	p.Click("#__missing")
	p.WaitFor(`location.pathname === "/no-such-page"`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Fatalf("the document was reloaded on the way to the error page")
	}
	if n := p.Number(`document.querySelectorAll(".sidebar, .drawer, .topbar").length`); n != 0 {
		t.Errorf("the error page carries %v pieces of the application's frame", n)
	}
	if h := p.String(`(document.querySelector("h1") || {}).textContent || ""`); h == "" {
		t.Errorf("the error page has no heading")
	}

	p.Eval(`history.back(); true`)
	p.WaitFor(`location.pathname === "/leaderboard" && !!document.querySelector(".sidebar")`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("going back from the error page reloaded the document")
	}
}

// On a phone the drawer opens over the page, closes on its own backdrop,
// and gives way to another menu.
//
// The backdrop is the summary itself, stretched over the page, so a press
// on the dim is a press on the control that opened it — no script, no
// listener. That is also the one arrangement here a selector cannot vouch
// for: what is at a point on the screen is what a finger would hit, so a
// press at a point is what closes it.
func TestBrowserTheDrawerClosesOnItsBackdrop(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/leaderboard")

	const isOpen = `document.querySelector("details.drawer").open`
	p.Click("details.drawer > summary")
	p.WaitFor(isOpen)
	if n := p.Number(`[...document.querySelectorAll(".drawer a[href]")].filter(a => a.getClientRects().length).length`); n == 0 {
		t.Fatalf("the open drawer shows no rows")
	}
	if ov := p.String(`getComputedStyle(document.body).overflow`); ov != "hidden" {
		t.Errorf("the page behind the open drawer still scrolls (overflow %q)", ov)
	}

	// The bottom-right corner: the panel is on the left and the bar along
	// the top, so this is the dim and nothing else.
	w, h := int(p.Number(`innerWidth`)), int(p.Number(`innerHeight`))
	p.ClickAt(w-8, h-8)
	p.WaitFor(`!` + isOpen)
	if ov := p.String(`getComputedStyle(document.body).overflow`); ov == "hidden" {
		t.Errorf("the page is still held still after the drawer closed")
	}

	// Opening another menu closes it: they share a name.
	p.Click("details.drawer > summary")
	p.WaitFor(isOpen)
	p.Click("details.account > summary")
	p.WaitFor(`document.querySelector("details.account").open`)
	if p.Eval(isOpen) == true {
		t.Errorf("the drawer stayed open behind the account menu")
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
	name := p.String(`document.querySelector(".board a.player").textContent.trim()`)

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
	if p.Eval(`document.activeElement === document.querySelector(".search-btn")`) != true {
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
// markup pins.
func TestBrowserAThemeLinkChangesTheThemeInPlace(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; true`)

	before := p.String(`document.documentElement.dataset.theme || ""`)
	href := p.String(`document.querySelector(".theme-track a.theme-opt:not(.on)").getAttribute("href")`)
	p.Click(`.theme-track a.theme-opt:not(.on)`)
	p.WaitFor(fmt.Sprintf(`(document.documentElement.dataset.theme || "") !== %q`, before))
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("the theme link reloaded the document")
	}
	if on := p.String(`document.querySelector(".theme-track a.theme-opt.on").getAttribute("href")`); on != href {
		t.Errorf("the theme picker marks %s, not the %s just chosen", on, href)
	}
}
