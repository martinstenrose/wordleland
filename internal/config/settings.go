package config

import (
	"os"
	"strings"
	"time"
)

// SettingKind says how a setting should be read, rather than how it should be
// worded. The words belong to whoever is rendering — they are shown to a
// reader in their own language, and this package has no translator and should
// not grow one.
type SettingKind int

const (
	// SettingUnset: the variable is not set. For most of these that is a
	// valid deployment rather than a fault — see the field comments in
	// Config and Bridge.
	SettingUnset SettingKind = iota
	// SettingValue: Value carries what is in force.
	SettingValue
	// SettingSecret: set, and deliberately not shown. Reading it back is
	// not something an admin needs and not something a screen should hold.
	SettingSecret
	// SettingOn and SettingOff: a switch, whose state is the whole value.
	SettingOn
	SettingOff
)

// Setting is one resolved configuration value as the admin area shows it:
// what the environment variable is called, what it came to, and whether it
// can be shown at all.
//
// It reports what is *in force*, not what was in the environment. Where a
// variable is absent and something is defaulted — the Signal API URL, the
// announcement locale — the default is what a reader needs to see, because
// that is what the process is doing.
type Setting struct {
	// Name is the environment variable, which is also how it is written in
	// the README and in the installation's .env. It is not translated: it
	// is an identifier, and an admin comparing this screen against a file
	// needs the two to match character for character.
	Name  string
	Value string
	Kind  SettingKind

	// Mono asks for the monospace face: an identifier or a key, where
	// telling 0 from O matters, rather than prose.
	Mono bool

	// Default marks a value nobody chose: the variable is not set, and what
	// is shown is what the code falls back to.
	//
	// It is a third thing, and the screen was showing it as the first. "Not
	// set" and a value somebody typed are easy to tell apart; a default sits
	// between them — nothing was chosen, and yet something is in force — and
	// reporting it as a choice hides the question an admin is usually here
	// to ask, which is whether anyone has set this at all.
	Default bool
}

// orDefault marks a setting whose value came from the code rather than from
// the environment. The environment is read again here rather than recorded at
// load time: it cannot change while the process runs, and Config keeps only
// the resolved value, having thrown away where it came from.
func orDefault(s Setting, env string) Setting {
	s.Default = strings.TrimSpace(os.Getenv(env)) == ""
	return s
}

// Settings is every variable this installation reads, in the order the admin
// area lists them: the origin and the clock first, then the secrets that have
// to be in place, then mail, then housekeeping, then the bridge.
//
// b is the bridge's configuration, nil when no bridge is configured — which
// is a valid deployment. Its settings come from there rather than from Config
// because that is where they are loaded; see LoadBridge.
func (c *Config) Settings(b *Bridge) []Setting {
	out := []Setting{
		text("APP_URL", c.AppURL),
		orDefault(Setting{Name: "TZ", Value: localZone(), Kind: SettingValue}, "TZ"),
		secret("TOTP_KEY", len(c.TOTPKey) > 0),
		text("TRUSTED_PROXIES", proxyList(c)),

		text("SMTP_HOST", c.SMTP.Host),
		text("SMTP_PORT", c.SMTP.Port),
		text("SMTP_USER", c.SMTP.User),
		secret("SMTP_PASS", c.SMTP.Pass != ""),
		text("SMTP_FROM", c.SMTP.From),

		text("PENDING_RETENTION", retention(c.PendingRetention)),
		orDefault(text("LOG_LEVEL", strings.ToLower(c.LogLevel.String())), "LOG_LEVEL"),
		text("ADMIN_EMAIL", c.AdminEmail),
		secret("ADMIN_PASSWORD", c.AdminPassword != ""),
		orDefault(toggle("DEMO_MODE", c.DemoMode), "DEMO_MODE"),
	}

	// With no bridge the two variables that turn it on are the answer, and
	// the three that shape it have nothing to shape: showing their defaults
	// would say the process is doing something it is not.
	if b == nil {
		return append(out,
			text("SIGNAL_ACCOUNT", ""),
			text("SIGNAL_GROUP_ID", ""),
		)
	}
	return append(out,
		// Masked here, in full on Diagnostics. That is not a contradiction:
		// Diagnostics exists to be compared by eye against what signal-cli
		// reports, and this screen exists to say what is configured. A phone
		// number left on a screen nobody is reading for it is personal data
		// with no reason to be there.
		Setting{Name: "SIGNAL_ACCOUNT", Value: maskTail(b.SignalAccount, 4), Kind: SettingValue},
		Setting{Name: "SIGNAL_GROUP_ID", Value: maskTail(b.SignalGroupID, 0), Kind: SettingValue, Mono: true},
		orDefault(toggle("SIGNAL_ANNOUNCE_MONTHS", b.AnnounceMonths), "SIGNAL_ANNOUNCE_MONTHS"),
		orDefault(text("SIGNAL_LOCALE", b.AnnounceLocale), "SIGNAL_LOCALE"),
		orDefault(text("SIGNAL_API_URL", b.SignalAPIURL), "SIGNAL_API_URL"),
	)
}

// text is a plain value, or unset when it is empty.
func text(name, value string) Setting {
	if value == "" {
		return Setting{Name: name, Kind: SettingUnset}
	}
	return Setting{Name: name, Value: value, Kind: SettingValue}
}

func secret(name string, set bool) Setting {
	if !set {
		return Setting{Name: name, Kind: SettingUnset}
	}
	return Setting{Name: name, Kind: SettingSecret}
}

func toggle(name string, on bool) Setting {
	if on {
		return Setting{Name: name, Kind: SettingOn}
	}
	return Setting{Name: name, Kind: SettingOff}
}

// localZone is the clock this installation keeps. TZ is not read by Load —
// the Go runtime reads it for us — so what matters here is the zone that came
// out of it, which is also what a reader would check a puzzle date against.
func localZone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	// Unset: the container's own zone, usually UTC. time.Local names itself
	// "Local" then, which says nothing, so the abbreviation in force stands
	// in for it.
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	zone, _ := time.Now().Zone()
	return zone
}

func proxyList(c *Config) string {
	out := make([]string, 0, len(c.TrustedProxies))
	for _, n := range c.TrustedProxies {
		out = append(out, n.String())
	}
	return strings.Join(out, ", ")
}

// retention renders the hold on unclaimed results. Zero is unlimited, which
// is the default, and is reported as unset rather than as "0s" — that is what
// it is, and the screen's own note explains what unset means.
func retention(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

// maskTail keeps the first keep characters and bullets the rest, so the shape
// of a value survives without the value doing. An empty string stays empty:
// unset is unset, and bulleting nothing would claim otherwise.
func maskTail(s string, keep int) string {
	if s == "" {
		return ""
	}
	if keep > len(s) {
		keep = len(s)
	}
	return s[:keep] + strings.Repeat("•", len(s)-keep)
}
