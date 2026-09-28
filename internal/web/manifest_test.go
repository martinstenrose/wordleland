package web

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/store"
)

// fetchManifest reads a manifest and checks it is served as one.
func fetchManifest(t *testing.T, srv *Server, path string) webManifest {
	t.Helper()
	rec := fetchAs(t, srv, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("GET %s is served as %q", path, ct)
	}
	var m webManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("GET %s is not JSON: %v", path, err)
	}
	return m
}

// Added to the Home Screen, the app opens standalone — no browser bars —
// and starts at the root: Today with a session, sign-in without. It is a
// member's app; a share link has no manifest of its own.
func TestTheHomeScreenAppStartsAtTheRoot(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	member := fetchManifest(t, srv, "/manifest.webmanifest")
	if member.Display != "standalone" {
		t.Errorf("display = %q, want standalone", member.Display)
	}
	if member.Name != "Wordleland" || member.ShortName != "Wordleland" {
		t.Errorf("named %q / %q", member.Name, member.ShortName)
	}
	if member.StartURL != "/" || member.Scope != "/" || member.ID != "/" {
		t.Errorf("the app starts at %q in %q (id %q), want the root", member.StartURL, member.Scope, member.ID)
	}
	if code := fetchAs(t, srv, "/share/"+slug+"/manifest.webmanifest", nil).Code; code != http.StatusNotFound {
		t.Errorf("a share link's manifest = %d, want 404: the app is a member's", code)
	}

	// Every icon it names is there, full-bleed and maskable both.
	var maskable bool
	for _, icon := range member.Icons {
		rec := fetchAs(t, srv, icon.Src, nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("icon %s = %d %s", icon.Src, rec.Code, rec.Header().Get("Content-Type"))
		}
		maskable = maskable || icon.Purpose == "maskable"
	}
	if !maskable {
		t.Error("no maskable icon, so Android crops the plain one to a circle")
	}
}

// Each page names the manifest — a share view too, since installing from it
// gives the member's app — says so the older way for Safari, lets the
// page run on under the status bar, and colours the status bar with the
// canvas the manifest's splash uses.
func TestEveryPageDeclaresTheHomeScreenApp(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	slug, _, _ := store.EnsureShareSlug(context.Background(), srv.db)

	for path, manifest := range map[string]string{
		"/today":               "/manifest.webmanifest",
		"/":                    "/manifest.webmanifest",
		"/share/" + slug + "/": "/manifest.webmanifest",
	} {
		cookie := session
		if path != "/today" {
			cookie = nil
		}
		body := fetchAs(t, srv, path, cookie).Body.String()
		for _, want := range []string{
			`<link rel="manifest" href="` + manifest + `">`,
			`<meta name="apple-mobile-web-app-capable" content="yes">`,
			`<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">`,
			`<meta name="theme-color" content="` + canvasLight + `"`,
			`<meta name="theme-color" content="` + canvasDark + `" media="(prefers-color-scheme: dark)">`,
			`<link rel="apple-touch-icon" href="/static/app-icon-180.png`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", path, want)
			}
		}
	}
}

// The tab bar is the views, with search beside them, wherever there are
// views to switch between — and nowhere else, so a stranger on Privacy
// keeps the bar's way home.
func TestTheTabBarIsTheViews(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/leaderboard", session).Body.String()
	at := strings.Index(body, `<nav class="tabbar"`)
	if at < 0 {
		t.Fatal("no tab bar")
	}
	bar := body[at : at+strings.Index(body[at:], "</nav>")]
	if n := strings.Count(bar, `class="tab `) + strings.Count(bar, `class="tab"`); n != 5 {
		t.Errorf("%d tabs, want the five views", n)
	}
	if !strings.Contains(bar, `<a class="tab on" href="/leaderboard" aria-current="page">`) {
		t.Error("the current view is not the tab marked current")
	}
	if !strings.Contains(bar, `class="tab-search glass" href="/search"`) || !strings.Contains(bar, `aria-controls="search-overlay"`) {
		t.Error("the tab bar's search does not open the search overlay")
	}

	if strings.Contains(fetchAs(t, srv, "/privacy", nil).Body.String(), `class="tabbar"`) {
		t.Error("a stranger on Privacy has a tab bar and loses the bar's way home")
	}
}

// Every Home Screen icon is the mark on its plate, at the size its name
// says. A render once came out as a white square with a strip of plate at
// one edge — a browser's minimum window width cropping a small screenshot —
// and a phone put exactly that on its Home Screen; nothing that only checks
// the file exists would have seen it.
func TestTheHomeScreenIconsAreTheMark(t *testing.T) {
	t.Parallel()

	plate := color.RGBA{0xef, 0xea, 0xe6, 0xff}
	near := func(a, b color.RGBA) bool {
		d := func(x, y uint8) int { return max(int(x), int(y)) - min(int(x), int(y)) }
		return d(a.R, b.R) <= 6 && d(a.G, b.G) <= 6 && d(a.B, b.B) <= 6
	}
	at := func(img image.Image, x, y int) color.RGBA {
		r, g, b, a := img.At(x, y).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}
	for name, size := range map[string]int{
		"static/app-icon-180.png": 180, "static/app-icon-192.png": 192,
		"static/app-icon-512.png": 512, "static/app-icon-maskable-512.png": 512,
	} {
		f, err := templateFS.Open(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Errorf("%s is %dx%d, want %dx%d", name, b.Dx(), b.Dy(), size, size)
			continue
		}
		// Full-bleed: the plate reaches every corner, with nothing
		// transparent for a phone to fill with black.
		for _, c := range [][2]int{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}} {
			if got := at(img, c[0], c[1]); !near(got, plate) {
				t.Errorf("%s: corner %v is %v, not the plate", name, c, got)
			}
		}
		// The first square of the grid is green: the mark is on it. The
		// maskable icon's grid is scaled in, so look at its square's middle.
		x := size * 8 / 34
		if strings.Contains(name, "maskable") {
			x = size * 23 / 68 // 17 - 0.73*(17-8), in 34ths, halved
		}
		if got := at(img, x, x); !(int(got.G) > int(got.R)+30 && int(got.G) > int(got.B)+20) {
			t.Errorf("%s: the grid's first square at (%d,%d) is %v, not green", name, x, x, got)
		}
	}
}
