package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/martinstenrose/wordleland/internal/bridge"
)

// fakeBridge reports whatever a test wants.
type fakeBridge struct {
	alive  bool
	reason string
	status bridge.Status
}

func (b fakeBridge) Alive() (bool, string) { return b.alive, b.reason }
func (b fakeBridge) Status() bridge.Status { return b.status }

// The probe answers one question: would restarting help? Since the services
// merged, failing it takes the board down too, so a bridge that is merely
// disconnected must not fail it — signal-cli being unreachable is not fixed
// by bouncing the application, and the retry loop is already handling it.
func TestHealthzIsLivenessNotReachability(t *testing.T) {
	tests := []struct {
		name   string
		bridge Bridge
		want   int
	}{
		{
			name:   "no bridge configured",
			bridge: nil,
			want:   http.StatusOK,
		},
		{
			name:   "bridge running and connected",
			bridge: fakeBridge{alive: true, status: bridge.Status{Connected: true, OK: true}},
			want:   http.StatusOK,
		},
		{
			// The one that matters. Restarting cannot reach signal-cli.
			name: "bridge running but disconnected",
			bridge: fakeBridge{alive: true, status: bridge.Status{
				Connected: false, OK: false, Reason: "disconnected from signal for 9m",
			}},
			want: http.StatusOK,
		},
		{
			// Results are being lost, which is bad — but it is the
			// diagnostics page's business, not a restart's.
			name: "bridge running but losing results",
			bridge: fakeBridge{alive: true, status: bridge.Status{
				Connected: true, OK: false, Dropped: 3,
			}},
			want: http.StatusOK,
		},
		{
			// The supervisor gave up. A restart is exactly right.
			name:   "bridge stopped",
			bridge: fakeBridge{alive: false, reason: "panicked 5 times in 5m0s"},
			want:   http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testServer(t)
			srv.SetBridge(tt.bridge)

			rec := fetchAs(t, srv, "/healthz", nil)
			if rec.Code != tt.want {
				t.Errorf("/healthz = %d, want %d (body %q)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

// A stopped bridge has to say why, or the container goes unhealthy with
// nothing to explain it.
func TestHealthzExplainsAStoppedBridge(t *testing.T) {
	srv := testServer(t)
	srv.SetBridge(fakeBridge{alive: false, reason: "panicked 5 times in 5m0s"})

	rec := fetchAs(t, srv, "/healthz", nil)
	if body := rec.Body.String(); !strings.Contains(body, "panicked") {
		t.Errorf("body = %q, want it to name the reason", body)
	}
}

// A typed nil must not look like a configured bridge: it satisfies the
// interface and would panic on first use.
func TestSetBridgeNormalisesATypedNil(t *testing.T) {
	srv := testServer(t)
	var none *bridge.Supervisor
	srv.SetBridge(none)

	if rec := fetchAs(t, srv, "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d with a nil supervisor, want %d", rec.Code, http.StatusOK)
	}
}

// The page exists for one failure above all: a bridge that is connected,
// answering, and delivering nothing because the group is wrong or the
// senders are unclaimed. So freshness must be on it, and must lead.
func TestDiagnosticsLeadsWithFreshness(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	srv.SetBridge(fakeBridge{alive: true, status: bridge.Status{
		Connected: true, OK: true, LastMessage: time.Now().Add(-2 * time.Hour),
	}})

	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()

	last := strings.Index(body, "Last result")
	conn := strings.Index(body, "Connection")
	if last < 0 {
		t.Fatal("the page does not report when a result last arrived")
	}
	if conn >= 0 && conn < last {
		t.Error("connection state is shown above freshness; a wrong group is green there and stale here")
	}
	if !strings.Contains(body, "Newest puzzle on the board") {
		t.Error("the page does not say how current the board is")
	}
	if strings.Contains(body, "%!") {
		t.Error("the page renders a formatting error")
	}
}

// Held results are the quiet failure. A bridge working perfectly against
// senders nobody has claimed puts nothing on the board.
func TestDiagnosticsSurfacesHeldResults(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	holdPending(t, srv, "unclaimed-1", "", 1894, 4)
	holdPending(t, srv, "unclaimed-1", "", 1895, 3)

	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
	if !strings.Contains(body, "Held for unclaimed senders") {
		t.Error("held results are not reported")
	}
	if !strings.Contains(body, "2 results") {
		t.Error("the count of held results is missing")
	}
}

// No bridge is a deployment choice, not a fault, and must not read as one.
func TestDiagnosticsWithNoBridge(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)

	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
	if !strings.Contains(body, "No Signal bridge is configured") {
		t.Error("the page does not say the bridge is absent")
	}
	// Nothing about a connection that does not exist.
	if strings.Contains(body, "reconnecting") {
		t.Error("an absent bridge is described as reconnecting")
	}
}

// Admin-only, like the rest of the area.
func TestDiagnosticsIsAdminOnly(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	member := seedLogin(t, srv, "member@example.tld", false)

	if got := fetchAs(t, srv, "/admin/diagnostics", signIn(t, srv, member.ID)).Code; got != http.StatusNotFound {
		t.Errorf("as a member = %d, want 404", got)
	}
}

// Merging the services lost the signal that came to you — an unhealthy
// container. A page you have to open is not a replacement, so whatever is
// wrong follows the admin around the area.
func TestAdminWarningFollowsTheAdminAround(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	holdPending(t, srv, "unclaimed-1", "", 1894, 4)

	for _, path := range []string{"/admin/players", "/admin/pending", "/admin/activity"} {
		body := fetchAs(t, srv, path, session).Body.String()
		if !strings.Contains(body, "held for a sender nobody has claimed") {
			t.Errorf("%s shows no warning about held results", path)
		}
		if !strings.Contains(body, `href="/admin/diagnostics"`) {
			t.Errorf("%s warns without linking to the detail", path)
		}
	}

	// Not on the diagnostics page itself, which already says it in full.
	body := fetchAs(t, srv, "/admin/diagnostics", session).Body.String()
	if strings.Contains(body, "held for a sender nobody has claimed") {
		t.Error("the diagnostics page repeats its own warning back at itself")
	}
}

// A healthy deployment must be quiet, or the warning becomes wallpaper.
func TestNoAdminWarningWhenNothingIsWrong(t *testing.T) {
	srv := testServer(t)
	seedBoard(t, srv)
	_, session := adminSession(t, srv)
	srv.SetBridge(fakeBridge{alive: true, status: bridge.Status{Connected: true, OK: true}})

	body := fetchAs(t, srv, "/admin/players", session).Body.String()
	if strings.Contains(body, `class="note warn"`) {
		t.Error("a healthy deployment shows a warning")
	}
}
