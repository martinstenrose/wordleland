package config

import (
	"strings"
	"testing"
)

// bridgeEnv is a complete, valid environment; tests override one field
// at a time so each failure is isolated.
func bridgeEnv() map[string]string {
	return map[string]string{
		"SIGNAL_API_URL": "http://signal-cli-rest-api:8080",
		// A shape a real account has: E.164, no leading zero after the sign.
		// The previous placeholder could not have been a real number, which
		// is fine as a fixture and useless as one once the format is
		// checked.
		"SIGNAL_ACCOUNT":  "+46700000000",
		"SIGNAL_GROUP_ID": "c2FtcGxlLWdyb3VwLWlk",
	}
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range []string{
		"SIGNAL_API_URL", "SIGNAL_ACCOUNT", "SIGNAL_GROUP_ID",
		"SIGNAL_ANNOUNCE_MONTHS", "SIGNAL_LOCALE",
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

// Announcing is on the moment the bridge is configured at all: that is the
// behaviour asked for, and the env var is the exception, not the rule.
func TestLoadBridgeAnnounceMonthsDefaultsOn(t *testing.T) {
	setEnv(t, bridgeEnv())

	cfg, err := LoadBridge()
	if err != nil {
		t.Fatalf("LoadBridge() failed: %v", err)
	}
	if !cfg.AnnounceMonths {
		t.Error("AnnounceMonths = false, want true by default")
	}
	if cfg.AnnounceLocale != "en" {
		t.Errorf("AnnounceLocale = %q, want the default %q", cfg.AnnounceLocale, "en")
	}
}

func TestLoadBridgeAnnounceMonthsCanBeDisabled(t *testing.T) {
	env := bridgeEnv()
	env["SIGNAL_ANNOUNCE_MONTHS"] = "false"
	env["SIGNAL_LOCALE"] = "sv"
	setEnv(t, env)

	cfg, err := LoadBridge()
	if err != nil {
		t.Fatalf("LoadBridge() failed: %v", err)
	}
	if cfg.AnnounceMonths {
		t.Error("AnnounceMonths = true, want false with SIGNAL_ANNOUNCE_MONTHS=false")
	}
	if cfg.AnnounceLocale != "sv" {
		t.Errorf("AnnounceLocale = %q, want %q", cfg.AnnounceLocale, "sv")
	}
}

func TestLoadBridgeRejectsABadAnnounceMonthsValue(t *testing.T) {
	env := bridgeEnv()
	env["SIGNAL_ANNOUNCE_MONTHS"] = "sometimes"
	setEnv(t, env)

	_, err := LoadBridge()
	if err == nil {
		t.Fatal("LoadBridge() accepted a non-boolean SIGNAL_ANNOUNCE_MONTHS")
	}
	if !strings.Contains(err.Error(), "SIGNAL_ANNOUNCE_MONTHS") {
		t.Errorf("error = %v, want it to name SIGNAL_ANNOUNCE_MONTHS", err)
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

// The failure this exists to prevent reached production: an account without
// its leading + connects to signal-cli-rest-api, stays connected, and
// matches no registered account for ever, silently.
func TestBridgeRejectsANonE164Account(t *testing.T) {
	for _, tt := range []struct {
		name    string
		account string
		wantErr bool
	}{
		{"E.164", "+46700000000", false},
		{"no leading plus", "46700000000", true},
		{"spaces", "+46 70 000 00 00", true},
		{"hyphens", "+46-700-000-00", true},
		{"empty after the plus", "+", true},
		{"leading zero after the plus", "+0700000000", true},
		{"not a number", "wordlebot", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := bridgeEnv()
			env["SIGNAL_ACCOUNT"] = tt.account
			setEnv(t, env)

			_, err := LoadBridge()
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadBridge() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "SIGNAL_ACCOUNT") {
				t.Errorf("error does not name the variable: %v", err)
			}
		})
	}
}

// The message has to say what to do, because the value looks correct in
// every place an operator would check it.
func TestBridgeAccountErrorMentionsTheYAMLTrap(t *testing.T) {
	env := bridgeEnv()
	env["SIGNAL_ACCOUNT"] = "46700000000"
	setEnv(t, env)

	_, err := LoadBridge()
	if err == nil {
		t.Fatal("an account without a leading + was accepted")
	}
	for _, want := range []string{"E.164", "quote it", "integer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
