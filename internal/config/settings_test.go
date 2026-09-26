package config

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

// find returns the named setting, failing when the list does not carry it —
// a variable this installation reads and does not show is the failure this
// page exists to prevent.
func find(t *testing.T, settings []Setting, name string) Setting {
	t.Helper()
	for _, s := range settings {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s is not among the %d settings shown", name, len(settings))
	return Setting{}
}

// The point of the page is that it can be read by an admin who is not in the
// container. The point of this test is that reading it never hands them a
// secret back.
func TestSecretsAreReportedWithoutBeingShown(t *testing.T) {
	cfg := &Config{
		TOTPKey:       bytes.Repeat([]byte{0x2a}, 32),
		AdminPassword: "correct horse battery staple",
		SMTP:          SMTP{Pass: "hunter2"},
	}

	settings := cfg.Settings(nil)
	for _, name := range []string{"TOTP_KEY", "ADMIN_PASSWORD", "SMTP_PASS"} {
		got := find(t, settings, name)
		if got.Kind != SettingSecret {
			t.Errorf("%s: kind = %v, want SettingSecret", name, got.Kind)
		}
		if got.Value != "" {
			t.Errorf("%s carries a value (%q); a secret is reported, never shown", name, got.Value)
		}
	}

	// And the whole list, in case a new row is added carrying one directly.
	for _, s := range settings {
		for _, secret := range []string{"correct horse", "hunter2", "\x2a\x2a\x2a"} {
			if s.Value != "" && strings.Contains(s.Value, secret) {
				t.Errorf("%s = %q, which contains a secret", s.Name, s.Value)
			}
		}
	}
}

// Absent is a state of its own, not an empty value: most of these are
// optional, and "not set" is the answer an admin is looking for.
func TestAnAbsentVariableIsReportedAsUnset(t *testing.T) {
	settings := (&Config{}).Settings(nil)

	for _, name := range []string{"APP_URL", "SMTP_HOST", "TRUSTED_PROXIES", "ADMIN_EMAIL", "PENDING_RETENTION"} {
		if got := find(t, settings, name); got.Kind != SettingUnset {
			t.Errorf("%s: kind = %v, want SettingUnset", name, got.Kind)
		}
	}
	// A switch is never unset: off is a state, not an absence.
	if got := find(t, settings, "DEMO_MODE"); got.Kind != SettingOff {
		t.Errorf("DEMO_MODE: kind = %v, want SettingOff", got.Kind)
	}
}

func TestValuesInForceAreShown(t *testing.T) {
	_, block, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	cfg := &Config{
		AppURL:           "https://wordle.example.tld",
		TrustedProxies:   []*net.IPNet{block},
		PendingRetention: 720 * time.Hour,
		LogLevel:         slog.LevelDebug,
		DemoMode:         true,
	}

	settings := cfg.Settings(nil)
	for _, tt := range []struct{ name, want string }{
		{"APP_URL", "https://wordle.example.tld"},
		{"TRUSTED_PROXIES", "10.0.0.0/8"},
		{"PENDING_RETENTION", "720h0m0s"},
		{"LOG_LEVEL", "debug"},
	} {
		got := find(t, settings, tt.name)
		if got.Kind != SettingValue || got.Value != tt.want {
			t.Errorf("%s = %q (kind %v), want %q as a value", tt.name, got.Value, got.Kind, tt.want)
		}
	}
	if got := find(t, settings, "DEMO_MODE"); got.Kind != SettingOn {
		t.Errorf("DEMO_MODE: kind = %v, want SettingOn", got.Kind)
	}
}

// With no bridge, the two variables that would turn it on are the answer.
// The three that only shape a running bridge are left out rather than shown
// at their defaults, which would say the process is doing something it is
// not.
func TestNoBridgeShowsOnlyTheTwoThatTurnItOn(t *testing.T) {
	settings := (&Config{}).Settings(nil)

	for _, name := range []string{"SIGNAL_ACCOUNT", "SIGNAL_GROUP_ID"} {
		if got := find(t, settings, name); got.Kind != SettingUnset {
			t.Errorf("%s: kind = %v, want SettingUnset", name, got.Kind)
		}
	}
	for _, s := range settings {
		switch s.Name {
		case "SIGNAL_ANNOUNCE_MONTHS", "SIGNAL_LOCALE", "SIGNAL_API_URL",
			"SIGNAL_REPLIES", "LLM_MODEL", "LLM_URL":
			t.Errorf("%s is shown with no bridge configured", s.Name)
		}
	}
}

// The replies and their model are bridge settings like the announcements:
// shown with a bridge, at their defaults when nothing set them.
func TestRepliesAndTheModelAreListedWithTheBridge(t *testing.T) {
	settings := (&Config{}).Settings(&Bridge{
		SignalAccount: "+46700000000", SignalGroupID: "Zm9vYmFyYmF6",
		Replies: true, LLMModel: DefaultLLMModel, LLMURL: DefaultLLMURL,
	})
	if got := find(t, settings, "SIGNAL_REPLIES"); got.Kind != SettingOn || !got.Default {
		t.Errorf("SIGNAL_REPLIES: kind = %v, default = %v; want on, as a default", got.Kind, got.Default)
	}
	if got := find(t, settings, "LLM_MODEL"); got.Value != DefaultLLMModel || !got.Default {
		t.Errorf("LLM_MODEL = %q (default %v), want the default model, marked as a default", got.Value, got.Default)
	}
	if got := find(t, settings, "LLM_URL"); got.Value != DefaultLLMURL {
		t.Errorf("LLM_URL = %q, want the compose default", got.Value)
	}
}

// The account is a phone number and the group id identifies a private group.
// Both are shown whole here, same as on Diagnostics: only an admin reaches
// this screen, and Diagnostics already carries them in the clear.
func TestTheSignalIdentifiersAreShownInFull(t *testing.T) {
	settings := (&Config{}).Settings(&Bridge{
		SignalAccount:  "+46700000000",
		SignalGroupID:  "Zm9vYmFyYmF6",
		AnnounceMonths: true,
		AnnounceLocale: "sv",
		SignalAPIURL:   DefaultSignalAPIURL,
	})

	if got := find(t, settings, "SIGNAL_ACCOUNT"); got.Value != "+46700000000" {
		t.Errorf("SIGNAL_ACCOUNT = %q, want the number in full", got.Value)
	}
	group := find(t, settings, "SIGNAL_GROUP_ID")
	if group.Value != "Zm9vYmFyYmF6" {
		t.Errorf("SIGNAL_GROUP_ID = %q, want the id in full", group.Value)
	}
	if !group.Mono {
		t.Error("SIGNAL_GROUP_ID is not drawn in the monospace face, where 0 and O differ")
	}

	// And the three that shape a running bridge are now worth showing.
	if got := find(t, settings, "SIGNAL_ANNOUNCE_MONTHS"); got.Kind != SettingOn {
		t.Errorf("SIGNAL_ANNOUNCE_MONTHS: kind = %v, want SettingOn", got.Kind)
	}
	if got := find(t, settings, "SIGNAL_API_URL"); got.Value != DefaultSignalAPIURL {
		t.Errorf("SIGNAL_API_URL = %q, want the default in force", got.Value)
	}
}

// A default is a third thing, and the screen used to show it as the first.
// "Not set" and a value somebody typed are easy to tell apart; a default sits
// between them — nothing was chosen, and yet something is in force — and
// reporting it as a choice hides the question an admin is usually here to
// ask, which is whether anybody has set this at all.
func TestADefaultedValueSaysSo(t *testing.T) {
	cfg := &Config{LogLevel: slog.LevelInfo}

	t.Setenv("LOG_LEVEL", "")
	t.Setenv("DEMO_MODE", "")
	settings := cfg.Settings(nil)
	for _, name := range []string{"LOG_LEVEL", "DEMO_MODE", "TZ"} {
		if got := find(t, settings, name); !got.Default {
			t.Errorf("%s is not marked as a default, though nothing set it", name)
		}
	}
	// Still reported, because it is what the process is doing: a default is
	// not an absence.
	if got := find(t, settings, "LOG_LEVEL"); got.Kind != SettingValue || got.Value != "info" {
		t.Errorf("LOG_LEVEL = %q (kind %v), want the default in force", got.Value, got.Kind)
	}
	if got := find(t, settings, "DEMO_MODE"); got.Kind != SettingOff {
		t.Errorf("DEMO_MODE: kind = %v, want SettingOff", got.Kind)
	}

	// Set it, and it stops being a default — the value is the same either
	// way, so the flag is the only thing carrying the difference.
	t.Setenv("LOG_LEVEL", "info")
	if got := find(t, cfg.Settings(nil), "LOG_LEVEL"); got.Default {
		t.Error("LOG_LEVEL is marked a default though the environment sets it")
	}
}

// Nothing set and nothing in force: there is no default to name.
func TestAnUnsetValueIsNotADefault(t *testing.T) {
	for _, s := range (&Config{}).Settings(nil) {
		if s.Kind == SettingUnset && s.Default {
			t.Errorf("%s is both unset and defaulted", s.Name)
		}
	}
}
