package config

import (
	"strings"
	"testing"
)

// bridgeEnv is a complete, valid environment; tests override one field
// at a time so each failure is isolated.
func bridgeEnv() map[string]string {
	return map[string]string{
		"SIGNAL_API_URL":  "http://signal-cli-rest-api:8080",
		"SIGNAL_ACCOUNT":  "+00000000000",
		"SIGNAL_GROUP_ID": "c2FtcGxlLWdyb3VwLWlk",
	}
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range []string{
		"SIGNAL_API_URL", "SIGNAL_ACCOUNT", "SIGNAL_GROUP_ID",
	} {
		t.Setenv(k, env[k])
	}
}

func TestLoadBridge(t *testing.T) {
	setEnv(t, bridgeEnv())

	cfg, err := LoadBridge()
	if err != nil {
		t.Fatalf("LoadBridge() failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("a fully configured bridge loaded as nil")
	}
	if cfg.SignalGroupID != "c2FtcGxlLWdyb3VwLWlk" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestLoadBridgeRequiresEverything(t *testing.T) {
	// The bridge has no useful degraded mode: without any one of these
	// it cannot receive, cannot tell which group to watch, or cannot
	// deliver.
	// SIGNAL_API_URL is not here: it is fixed by the compose file and
	// defaults in code, so leaving it unset is the normal case.
	for _, missing := range []string{
		"SIGNAL_ACCOUNT", "SIGNAL_GROUP_ID",
	} {
		t.Run(missing, func(t *testing.T) {
			env := bridgeEnv()
			env[missing] = ""
			setEnv(t, env)

			_, err := LoadBridge()
			if err == nil {
				t.Fatalf("LoadBridge() succeeded without %s", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error = %v, want it to name %s", err, missing)
			}
		})
	}
}

// The mistake this catches is invisible at runtime: /v1/groups reports both
// an "id" of the form group.<base64> and an "internal_id" of the bare
// base64, and only the second matches what arrives on a message. Configure
// the wrong one and the bot connects, reports itself healthy, and matches
// nothing for ever.
func TestLoadBridgeRejectsThePrefixedGroupID(t *testing.T) {
	env := bridgeEnv()
	env["SIGNAL_GROUP_ID"] = "group.c2FtcGxlLWdyb3VwLWlk"
	setEnv(t, env)

	_, err := LoadBridge()
	if err == nil {
		t.Fatal("LoadBridge() accepted the group.<base64> form")
	}
	if !strings.Contains(err.Error(), "internal_id") {
		t.Errorf("error = %v, want it to name the field to use instead", err)
	}
}

func TestLoadBridgeRejectsBadURLs(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"SIGNAL_API_URL", "signal-cli-rest-api:8080"},
		{"SIGNAL_API_URL", "ftp://signal-cli-rest-api"},
		{"SIGNAL_API_URL", "not a url at all"},
		{"SIGNAL_API_URL", "http://"},
	} {
		t.Run(tc.key+" "+tc.value, func(t *testing.T) {
			env := bridgeEnv()
			env[tc.key] = tc.value
			setEnv(t, env)

			_, err := LoadBridge()
			if err == nil {
				t.Fatalf("LoadBridge() accepted %s=%q", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error = %v, want it to name %s", err, tc.key)
			}
		})
	}
}

// A fresh deployment usually has more than one variable wrong.
func TestLoadBridgeReportsEveryProblem(t *testing.T) {
	setEnv(t, map[string]string{"SIGNAL_GROUP_ID": "group.abc"})

	_, err := LoadBridge()
	if err == nil {
		t.Fatal("LoadBridge() succeeded on an empty environment")
	}
	for _, want := range []string{
		"SIGNAL_ACCOUNT", "internal_id",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %s:\n%v", want, err)
		}
	}
}

// Both service URLs are fixed by the compose file, so they default rather
// than being configured. An override still works, for running the
// bridge outside compose, and is still validated.
func TestBridgeServiceURLDefaults(t *testing.T) {
	env := bridgeEnv()
	env["SIGNAL_API_URL"] = ""
	setEnv(t, env)

	cfg, err := LoadBridge()
	if err != nil {
		t.Fatalf("LoadBridge() failed with the URL unset: %v", err)
	}
	if cfg.SignalAPIURL != DefaultSignalAPIURL {
		t.Errorf("SignalAPIURL = %q, want %q", cfg.SignalAPIURL, DefaultSignalAPIURL)
	}

	// The default names a compose service, so a rename there without a
	// change here would leave the bridge talking to nothing.
	if !strings.Contains(DefaultSignalAPIURL, "signal-cli-rest-api") {
		t.Error("the default does not name the signal-cli-rest-api service")
	}
}

func TestBridgeServiceURLOverrideIsValidated(t *testing.T) {
	env := bridgeEnv()
	env["SIGNAL_API_URL"] = "not-a-url"
	setEnv(t, env)

	if _, err := LoadBridge(); err == nil {
		t.Fatal("LoadBridge() accepted a bad SIGNAL_API_URL override")
	} else if !strings.Contains(err.Error(), "SIGNAL_API_URL") {
		t.Errorf("error = %v, want it to name SIGNAL_API_URL", err)
	}
}

// The bridge is optional. Nothing configured is a deployment that serves the
// board and waits for Signal to be linked later — which is how a fresh
// install imports its history before there is anything to bridge.
func TestBridgeIsOptional(t *testing.T) {
	setEnv(t, map[string]string{})

	cfg, err := LoadBridge()
	if err != nil {
		t.Fatalf("an unconfigured bridge is not an error: %v", err)
	}
	if cfg != nil {
		t.Errorf("config = %+v, want nil when nothing is set", cfg)
	}
}

// Half-configured is still refused: it would start, watch nothing, and look
// exactly like a deployment that is working.
func TestBridgeRefusesHalfAConfiguration(t *testing.T) {
	for _, only := range []string{"SIGNAL_ACCOUNT", "SIGNAL_GROUP_ID"} {
		t.Run(only, func(t *testing.T) {
			env := map[string]string{only: bridgeEnv()[only]}
			setEnv(t, env)

			_, err := LoadBridge()
			if err == nil {
				t.Fatalf("LoadBridge() accepted only %s", only)
			}
			if !strings.Contains(err.Error(), "required when") {
				t.Errorf("error = %v, want it to say which other variable is needed", err)
			}
		})
	}
}
