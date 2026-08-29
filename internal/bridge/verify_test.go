package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
