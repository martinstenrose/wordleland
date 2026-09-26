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
// The whole promise of switching pages in place. A counter set on window
// survives a switch and does not survive a load, so it is the one honest
// witness.
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

// The About panel covers what is under it.
//
// It is drawn from inside the rail, and the rail is sticky, which makes it
// a stacking layer of its own: anything positioned in the page — the form
// table's rank cells — painted over the panel, so the text of the page
// showed through the text of the panel. What is on top at a point is the
// one thing a selector cannot say.
func TestBrowserTheAboutPanelCoversThePage(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	p.Click(".sidebar details.about > summary")
	p.WaitFor(`document.querySelector(".sidebar details.about").open`)

	// Every element of the page whose centre lies under the panel, and
	// whether the panel is what is hit there.
	showing := p.Strings(`(() => {
		const panel = document.querySelector(".sidebar .about-panel");
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
// that was inside it lands back on what opened it.
//
// A <details> opens and closes with no script, but not from the keyboard
// without first finding its summary again — which, for Help drawn over the
// whole page, is behind the panel being closed.
func TestBrowserEscClosesPopupsInnermostFirst(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), phoneWidth)
	p.Navigate(site.base + "/leaderboard")

	const drawerOpen = `document.querySelector("details.drawer").open`
	const helpOpen = `document.querySelector(".drawer details.about").open`
	p.Click("details.drawer > summary")
	p.WaitFor(drawerOpen)
	p.Click(".drawer details.about > summary")
	p.WaitFor(helpOpen)
	p.Eval(`document.querySelector(".drawer .about-link").focus(); true`)

	p.Press("Escape", "Escape", 0)
	p.WaitFor(`!` + helpOpen)
	if p.Eval(drawerOpen) != true {
		t.Errorf("Esc closed the drawer along with the Help panel inside it")
	}
	if p.Eval(`document.activeElement === document.querySelector(".drawer details.about > summary")`) != true {
		t.Errorf("closing Help did not put focus back on its summary")
	}

	p.Press("Escape", "Escape", 0)
	p.WaitFor(`!` + drawerOpen)
	if path := p.Path(); path != "/leaderboard" {
		t.Errorf("Esc navigated to %s", path)
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
// markup pins. The attribute moves at the press now, ahead of the page, so
// its changing no longer says the page has arrived: the picker's marker is
// read only once the old content has gone, which is what a switch is.
func TestBrowserAThemeLinkChangesTheThemeInPlace(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`window.__alive = 1; document.getElementById("main").__old = true; true`)

	// The track is in the account sheet now, so the press a reader makes is
	// two: the circle at the end of the bar, then the setting.
	p.Click(".account > summary")
	p.WaitFor(`!!document.querySelector(".account[open] .track a.theme-opt")`)

	before := p.String(`document.documentElement.dataset.theme || ""`)
	href := p.String(`document.querySelector(".account .track a.theme-opt:not(.on)").getAttribute("href")`)
	p.Click(`.account .track a.theme-opt:not(.on)`)
	p.WaitFor(fmt.Sprintf(`(document.documentElement.dataset.theme || "") !== %q`, before))
	p.WaitFor(`document.getElementById("main").__old !== true`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("the theme link reloaded the document")
	}
	if on := p.String(`document.querySelector(".account .track a.theme-opt.on").getAttribute("href")`); on != href {
		t.Errorf("the theme track marks %s, not the %s just chosen", on, href)
	}
	// And the sheet is still open, on a page the server rendered that way:
	// a reader who has just changed the theme may well want the language
	// too, and the body swap would otherwise have closed it under them.
	if shut := p.Eval(`!document.querySelector(".account[open]")`); shut != false {
		t.Error("the account sheet closed on the reader when they used it")
	}
	// Focus with it, on the setting that was pressed rather than at the top
	// of the page.
	if at := p.String(`(document.activeElement.closest(".prefs") ? "prefs" : document.activeElement.id || document.activeElement.tagName)`); at != "prefs" {
		t.Errorf("focus landed on %s, not back on the track just used", at)
	}

	// The second setting, from inside the sheet that stayed open: the whole
	// point of the first half of this test.
	p.Click(`.account .track a.lang-opt:not(.on)`)
	p.WaitFor(`document.documentElement.lang !== "en"`)
	if shut := p.Eval(`!document.querySelector(".account[open]")`); shut != false {
		t.Error("the sheet closed on the second change")
	}
}

// And the marker that keeps it open is not on anything else: a control
// outside the sheet leads to a page with the sheet shut, the way a link to
// another page should.
func TestBrowserAnOrdinaryLinkLeavesTheSheetShut(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/leaderboard")

	p.Click(".account > summary")
	p.WaitFor(`!!document.querySelector(".account[open]")`)
	p.Click(`.account .track a.theme-opt:not(.on)`)
	p.WaitFor(`!!document.querySelector(".account[open] .prefs")`)

	// From there, an ordinary navigation. The address still carries the
	// theme it was given; it must not still carry the menu.
	follow(p, "/grid")
	p.WaitFor(`location.pathname === "/grid"`)
	if open := p.Eval(`!!document.querySelector(".account[open]")`); open != false {
		t.Error("following a rail row arrived with the account sheet open")
	}
	if url := p.String(`location.search`); strings.Contains(url, "menu=") {
		t.Errorf("the menu marker followed the reader to another page: %s", url)
	}
}

// The sheet fits, and nothing in it is cropped — at a phone's width as much
// as at a desktop's.
//
// This is the measurement the artboard cannot make: the mock draws the two
// tracks at 252px with English in them, and the labels that have to fit are
// five languages' worth of "System" and "Dark" in a panel that also has to
// stay on a 390px screen with the bar's gutter either side.
func TestBrowserTheAccountSheetFitsBothWidths(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	// One tab per width, and Months rather than the board: a tab per
	// language would hold a live stream each, and the seventh would never
	// get a connection to navigate on. Not the roster either — /players
	// redirects to a player and drops the query with it.
	for _, width := range []int{phoneWidth, desktopWidth} {
		p := site.open(b, width)
		for _, lang := range []string{"en", "sv", "de", "it", "es"} {
			p.Navigate(site.base + "/months?lang=" + lang)
			// The measurement is only worth anything if the page really is
			// in the language it is being measured for.
			if got := p.String(`document.documentElement.lang`); got != lang {
				t.Fatalf("asked for %s and got a page in %s", lang, got)
			}
			p.Click(".account > summary")
			p.WaitFor(`!!document.querySelector(".account[open] .account-menu")`)

			// On screen: the panel hangs off the end of the bar, and at a
			// phone's width there is not much end to hang off.
			if off := p.String(`(() => {
				const r = document.querySelector(".account-menu").getBoundingClientRect();
				if (r.left < 0) return "left " + Math.round(r.left);
				if (r.right > window.innerWidth) return "right " + Math.round(r.right - window.innerWidth);
				return "";
			})()`); off != "" {
				t.Errorf("%s at %d: the sheet is off screen by %s", lang, width, off)
			}

			// Not cropped: a label wider than the segment holding it is the
			// one thing the artboard's English cannot tell us about.
			if cropped := p.Strings(`[...document.querySelectorAll(".account-menu .opt-label, .account-menu .lang-opt")]
				.filter(el => el.scrollWidth > el.clientWidth + 1)
				.map(el => el.textContent.trim() + " (" + el.clientWidth + " < " + el.scrollWidth + ")")`); len(cropped) > 0 {
				t.Errorf("%s at %d: cropped in the track: %v", lang, width, cropped)
			}

			// And every segment is actually pressable rather than squeezed
			// to nothing by its neighbours.
			if thin := p.Strings(`[...document.querySelectorAll(".account-menu .track a")]
				.filter(a => a.getBoundingClientRect().width < 24)
				.map(a => a.getAttribute("title") + " " + Math.round(a.getBoundingClientRect().width))`); len(thin) > 0 {
				t.Errorf("%s at %d: segments too narrow to press: %v", lang, width, thin)
			}
		}
	}
}

// After a switch, focus lands somewhere a reader can use.
//
// The element that was pressed is gone with the body it was in, and focus
// left on a departing node falls to the body — a keyboard back at the top
// of the page with nothing to say it moved. Three controls are a place in
// the page rather than a step out of it and keep focus there: a rail row
// focuses the same row in the new rail, a ranking row reopens the menu on
// that row, the section bar keeps the focus and stays shut. Everything
// else lands on the main region. None of this is in the markup.
func TestBrowserFocusLandsSomewhereUsefulAfterASwitch(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	// The active element's href, or what it is when it has none.
	const active = `(() => { const a = document.activeElement; if (!a) return "nothing";
		return a.getAttribute("href") || a.id || a.tagName.toLowerCase(); })()`

	for _, href := range railHrefs(p) {
		follow(p, href)
		p.WaitFor(fmt.Sprintf(`%s === %q`, active, href))
	}

	// A link inside the page: the main region.
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`document.querySelector("main").__stale = true; true`)
	p.Click(".board a.player")
	p.WaitFor(`location.pathname.startsWith("/players/") && !(document.querySelector("main") || {}).__stale`)
	p.WaitFor(active + ` === "main"`)

	// A ranking row: the menu was open, and the reader may have a second
	// rule to set, so it opens again on the row they chose.
	p.Navigate(site.base + "/leaderboard")
	p.Eval(`document.querySelector("details.ranking").open = true; document.querySelector("main").__stale = true; true`)
	p.Click(".ranking-panel a:not(.on)")
	p.WaitFor(`!(document.querySelector("main") || {}).__stale`)
	p.WaitFor(`document.querySelector("details.ranking").open && document.querySelector(".ranking-panel").contains(document.activeElement)`)

	// The section bar: the reader asked for a page, not the list again, so
	// the menu stays shut and the bar keeps the focus.
	p.Navigate(site.base + "/admin/settings")
	p.Eval(`document.querySelector("main").__stale = true; true`)
	p.Click(".switcher-panel a:not(.on)")
	p.WaitFor(`!(document.querySelector("main") || {}).__stale`)
	p.WaitFor(`document.activeElement === document.querySelector("details.switcher > summary") && !document.querySelector("details.switcher").open`)
}

// Collapsing the rail does not reload the page, and the width is remembered.
//
// The control is a link to this page at the other width, and following it
// with no script is the whole feature. With script it must still not cost
// a reload — and the server must still be told, because the next page load
// reads the cookie and nothing else.
func TestBrowserCollapsingTheRailIsRememberedWithoutAReload(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")
	p.Eval(`window.__alive = 1; true`)

	before := p.String(`document.documentElement.dataset.sidebar || ""`)
	p.Click(".nav-collapse")
	p.WaitFor(fmt.Sprintf(`(document.documentElement.dataset.sidebar || "") !== %q`, before))
	after := p.String(`document.documentElement.dataset.sidebar || ""`)
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("collapsing the rail reloaded the document")
	}

	// A fresh load comes back at the width just chosen. Polled, because the
	// request that tells the server may still be in flight.
	deadline := time.Now().Add(4 * time.Second)
	for {
		p.Navigate(site.base + "/leaderboard")
		if got := p.String(`document.documentElement.dataset.sidebar || ""`); got == after {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a fresh load still shows the rail %q after choosing %q", before, after)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The rail takes its new width the moment the collapse control is pressed,
// before the server has answered.
//
// The width is an attribute on <html>, and the page that comes back carries
// it — that alone is correct, and on a local network quick, but the rail's
// width transition runs on the node the swap is about to replace, so the
// motion went with the round trip. app.js sets the attribute at the press,
// off the parameter the link already carries, and the reply confirms it.
// Every reply is held here for long enough to look through the gap: the
// attribute and the width have to have moved while the request is still
// out, and to still be there once it is back.
func TestBrowserTheRailTakesItsNewWidthBeforeTheServerAnswers(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")
	p.Eval(`window.__alive = 1; document.getElementById("main").__old = true; true`)
	p.Eval(`(() => {
		const send = XMLHttpRequest.prototype.send;
		XMLHttpRequest.prototype.send = function () {
			const args = arguments;
			setTimeout(() => send.apply(this, args), 600);
		};
		return true;
	})()`)

	before := p.String(`document.documentElement.dataset.sidebar || ""`)
	width := p.Number(`document.querySelector(".sidebar").getBoundingClientRect().width`)
	p.Click(".nav-collapse")

	p.WaitFor(fmt.Sprintf(`(document.documentElement.dataset.sidebar || "") !== %q`, before))
	p.WaitFor(fmt.Sprintf(`document.querySelector(".sidebar").getBoundingClientRect().width < %v`, width))
	if old := p.Eval(`document.getElementById("main").__old === true`); old != true {
		t.Fatalf("the reply landed before the rail moved; nothing was checked ahead of it")
	}
	after := p.String(`document.documentElement.dataset.sidebar || ""`)

	// The reply confirms rather than reverses it.
	p.WaitFor(`document.getElementById("main").__old !== true`)
	if got := p.String(`document.documentElement.dataset.sidebar || ""`); got != after {
		t.Errorf("the rail was %q ahead of the reply and %q after it", after, got)
	}
	if alive := p.Number(`window.__alive || 0`); alive != 1 {
		t.Errorf("collapsing the rail reloaded the document")
	}
}

// The bar and the rail are named for the view transition, and so is the
// content — but only inside the shell.
//
// A switch cross-fades the content while the bar and the rail hold still;
// which element is which is three names in the stylesheet, and a tidy-up
// that dropped one would put the whole window back into the cross-fade
// that read as a reload. The sign-in card's <main> is deliberately not
// named: naming it would morph the page well into the card on sign-out.
func TestBrowserTheBarAndTheRailAreNamedAndTheContentIsToo(t *testing.T) {
	site := newSite(t)
	p := site.open(newBrowser(t), desktopWidth)
	p.Navigate(site.base + "/today")

	name := func(selector string) string {
		return p.String(fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).viewTransitionName || ""`, selector))
	}
	for selector, want := range map[string]string{".topbar": "topbar", ".sidebar": "rail", "#main": "content"} {
		if got := name(selector); got != want {
			t.Errorf("%s is named %q for the view transition, want %q", selector, got, want)
		}
	}

	p.Eval(`document.querySelector("details.account").open = true; true`)
	p.Click("details.account form button")
	p.WaitFor(`!!document.querySelector(".auth-frame")`)
	if got := name("#main"); got != "none" && got != "" {
		t.Errorf("the sign-in frame's main region is named %q; it should cross-fade as part of the page", got)
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

	p.Click(`.sidebar a.nav-row[href="/leaderboard"]`)
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
	if n := p.Number(`document.querySelectorAll(".sidebar, .topbar").length`); n != 0 {
		t.Errorf("the sign-in card arrived with %v pieces of the application's frame", n)
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
	q.Eval(`window.__alive = 1; document.querySelector("main > section").__stale = true; true`)
	file(t, site.srv, "lapsed", current-1, 3)
	q.WaitFor(`!(document.querySelector("main > section") || {}).__stale`)
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

// The line under a player's name fits the bar on a phone, and a wider screen
// still carries the last-played date.
//
// Rank and last-played together ran past a phone's bar and were cropped
// mid-word; a phone drops the date rather than cut the line.
func TestBrowserThePlayerSubtitleFitsOnAPhone(t *testing.T) {
	site := newSite(t)
	b := newBrowser(t)

	const probe = `(() => {
		const h = document.querySelector(".switcher-hint");
		if (!h) return "none";
		return (h.scrollWidth > h.clientWidth ? "cropped" : "fits") + " | " + h.innerText.trim();
	})()`

	p := site.open(b, phoneWidth)
	p.Navigate(site.base + "/players")
	got := p.String(probe)
	if !strings.HasPrefix(got, "fits | ") {
		t.Errorf("phone: the subtitle is %q", got)
	}
	if strings.Contains(got, "·") {
		t.Errorf("phone: the subtitle still carries the last-played date: %q", got)
	}

	q := site.open(b, desktopWidth)
	q.Navigate(site.base + "/players")
	if got := q.String(probe); !strings.Contains(got, "·") {
		t.Errorf("desktop: the subtitle lost the last-played date: %q", got)
	}
}
