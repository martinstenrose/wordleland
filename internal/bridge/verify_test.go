package bridge

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// signalStub answers the two endpoints the verifier asks about.
func signalStub(t *testing.T, accounts []string, groups string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`["` + strings.Join(accounts, `","`) + `"]`))
	})
	mux.HandleFunc("/v1/groups/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(groups))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const oneGroup = `[{"name":"Wordle","internal_id":"c2FtcGxlLWdyb3VwLWlk","id":"group.c2FtcGxl"}]`

// The whole point: a configuration that would connect and receive nothing
// is named as broken, rather than looking like a quiet group.
func TestVerifyNamesAMisconfiguration(t *testing.T) {
	for _, tt := range []struct {
		name        string
		account     string
		group       string
		wantOK      bool
		wantProblem string
	}{
		{
			name:    "everything matches",
			account: "+46700000000",
			group:   "c2FtcGxlLWdyb3VwLWlk",
			wantOK:  true,
		},
		{
			// The failure that reached production. YAML ate the plus.
			name:        "account without its leading plus",
			account:     "46700000000",
			group:       "c2FtcGxlLWdyb3VwLWlk",
			wantProblem: "SIGNAL_ACCOUNT",
		},
		{
			name:        "an account signal-cli does not have",
			account:     "+46700000001",
			group:       "c2FtcGxlLWdyb3VwLWlk",
			wantProblem: "SIGNAL_ACCOUNT",
		},
		{
			name:        "a group the account is not in",
			account:     "+46700000000",
			group:       "bm90LXRoZS1ncm91cA",
			wantProblem: "SIGNAL_GROUP_ID",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := signalStub(t, []string{"+46700000000"}, oneGroup)

			got, err := newVerifier(srv.URL, tt.account, tt.group).check(context.Background())
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if got.OK() != tt.wantOK {
				t.Errorf("OK() = %v, want %v (problem: %s)", got.OK(), tt.wantOK, got.Problem)
			}
			if tt.wantProblem != "" && !strings.Contains(got.Problem, tt.wantProblem) {
				t.Errorf("problem = %q, want it to name %s", got.Problem, tt.wantProblem)
			}
			if tt.wantOK && got.Problem != "" {
				t.Errorf("a working configuration reported a problem: %s", got.Problem)
			}
		})
	}
}

// The account is a phone number. Naming the variable is enough to act on;
// printing the value puts it in the log and on an admin page.
func TestVerifyDoesNotEchoTheNumber(t *testing.T) {
	srv := signalStub(t, []string{"+46700000000"}, oneGroup)

	got, err := newVerifier(srv.URL, "+46700000009", "c2FtcGxlLWdyb3VwLWlk").check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if strings.Contains(got.Problem, "46700000009") {
		t.Errorf("the configured number was echoed into the problem: %s", got.Problem)
	}
}

// signal-cli being unreachable is not a misconfiguration, and must not be
// reported as one: at startup it usually means "not up yet".
func TestVerifyDistinguishesUnreachableFromWrong(t *testing.T) {
	v := newVerifier("http://127.0.0.1:1", "+46700000000", "c2FtcGxlLWdyb3VwLWlk")

	got, err := v.check(context.Background())
	if err == nil {
		t.Fatal("an unreachable signal-cli reported a verdict")
	}
	if got.Done {
		t.Error("an unreachable signal-cli produced a completed verification")
	}
}

// countingLogger records how many times each level was written.
type countingLogger struct {
	mu     sync.Mutex
	counts map[slog.Level]int
	last   string
}

func (c *countingLogger) Enabled(context.Context, slog.Level) bool { return true }
func (c *countingLogger) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *countingLogger) WithGroup(string) slog.Handler            { return c }
func (c *countingLogger) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[slog.Level]int{}
	}
	c.counts[r.Level]++
	c.last = r.Message
	return nil
}
func (c *countingLogger) count(l slog.Level) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[l]
}

// A verdict that has not changed is not worth a line. An hourly "still
// fine" teaches a reader to skip these; an hourly "still broken" buries the
// one that said it first.
func TestReportSpeaksOnlyOnChange(t *testing.T) {
	h := &countingLogger{}
	b := &Bridge{health: newHealth(time.Now), logger: slog.New(h)}

	bad := Verification{Done: true, Problem: "SIGNAL_ACCOUNT is not registered"}
	good := Verification{Done: true, AccountOK: true, GroupOK: true}

	b.report(bad, Verification{})
	if got := h.count(slog.LevelError); got != 1 {
		t.Fatalf("first failure logged %d errors, want 1", got)
	}

	// Same verdict, three more times.
	for i := 0; i < 3; i++ {
		b.report(bad, b.health.snapshot().Verification)
	}
	if got := h.count(slog.LevelError); got != 1 {
		t.Errorf("an unchanged failure logged %d errors, want it to stay 1", got)
	}

	// Recovery is a change, and worth saying.
	b.report(good, b.health.snapshot().Verification)
	if got := h.count(slog.LevelInfo); got != 1 {
		t.Errorf("recovery logged %d info lines, want 1", got)
	}
	if !strings.Contains(h.last, "works again") {
		t.Errorf("recovery message = %q, want it to say the config works again", h.last)
	}

	// And back to broken is a change again.
	b.report(bad, b.health.snapshot().Verification)
	if got := h.count(slog.LevelError); got != 2 {
		t.Errorf("a new failure logged %d errors, want 2", got)
	}
}

// The verdict reaches Status either way, whether or not it was logged.
func TestReportAlwaysRecordsTheVerdict(t *testing.T) {
	b := &Bridge{health: newHealth(time.Now), logger: slog.New(&countingLogger{})}

	good := Verification{Done: true, AccountOK: true, GroupOK: true}
	b.report(good, Verification{})
	if !b.health.snapshot().Verification.OK() {
		t.Fatal("a passing verification did not reach Status")
	}

	bad := Verification{Done: true, Problem: "the group changed"}
	b.report(bad, b.health.snapshot().Verification)
	st := b.health.snapshot()
	if st.Verification.OK() {
		t.Error("a later failure did not reach Status")
	}
	if st.Verification.Problem != "the group changed" {
		t.Errorf("Problem = %q, want the new one", st.Verification.Problem)
	}
}
