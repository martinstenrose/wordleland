// Package config loads the app configuration from the environment.
//
// Variable names are unprefixed: each compose service declares its
// own environment block rather than sharing an env_file, so there is nothing
// for an app prefix to disambiguate against.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultDBPath is where the database always lives in the container. It is not
// configurable: a volume or bind mount decides where the file really is. The
// -db flag exists only so a binary can run outside a container.
const DefaultDBPath = "/data/db.sqlite"

// ListenAddr is fixed. Port 8080 rather than 80 because the distroless nonroot
// base runs as UID 65532, and binding below 1024 would require root or
// CAP_NET_BIND_SERVICE for no benefit.
const ListenAddr = ":8080"

// totpKeyLen is the AES-256 key length required for TOTP secret encryption.
const totpKeyLen = 32

// SMTP holds outgoing mail settings. Absent configuration is not an error: the
// email flows become unavailable and the rest of the app runs normally, which
// is a supported way to run this.
type SMTP struct {
	Host string
	Port string
	User string
	Pass string
	From string
}

// Configured reports whether enough is set to send mail at all.
func (s SMTP) Configured() bool {
	return s.Host != "" && s.From != ""
}

// Config is the app's resolved configuration.
type Config struct {
	// DBPath comes from the -db flag, defaulting to DefaultDBPath.
	DBPath string

	// AppURL is the public origin only, no path. Emailed links must be
	// absolute, and this cannot be derived from the listen address. Deriving
	// it from a request Host header would let an attacker request a reset for
	// someone else's account with a forged header and have the emailed link
	// point at a host they control, so it is configured explicitly and
	// validated at boot whenever mail can actually be sent.
	AppURL string

	// TOTPKey is the 32-byte AES-GCM key protecting TOTP secrets at rest.
	// Losing it makes every enrolled secret unrecoverable, so a bad value is
	// a fail-fast boot error rather than a surprise at first use.
	TOTPKey []byte

	// TrustedProxies bounds when X-Forwarded-For is believed. Empty means the
	// header is never consulted and the direct peer address is always used.
	TrustedProxies []*net.IPNet

	SMTP SMTP

	// PendingRetention bounds how long unclaimed results are held. Zero means
	// unlimited, which is the default. No purge job exists yet.
	PendingRetention time.Duration

	// AdminEmail and AdminPassword create the first administrator on a
	// fresh database, so a new deployment does not require running the CLI
	// before anyone can log in.
	//
	// They apply only when no user exists at all. Leaving them set is
	// therefore harmless: they cannot change an existing password, and
	// cannot bring back an account that was deliberately removed.
	AdminEmail    string
	AdminPassword string

	// DemoMode arms the `demo` CLI verb, which generates and deletes
	// synthetic data for a staging deployment with no Signal bridge. It is
	// configuration rather than a flag on the verb itself so serve can warn
	// at boot when it is set — a production instance with it on by accident
	// must say so, not arm a destructive verb silently.
	DemoMode bool
}

// Load reads configuration from the environment. dbPath comes from the -db
// flag; an empty value selects DefaultDBPath.
//
// Every problem found is reported together rather than one per run: a fresh
// deployment usually has more than one variable wrong, and fixing them one
// restart at a time is needless.
func Load(dbPath string) (*Config, error) {
	cfg := &Config{DBPath: dbPath}
	if cfg.DBPath == "" {
		cfg.DBPath = DefaultDBPath
	}

	var problems []string

	key, err := loadTOTPKey(os.Getenv("TOTP_KEY"))
	if err != nil {
		problems = append(problems, "TOTP_KEY: "+err.Error())
	}
	cfg.TOTPKey = key

	cfg.SMTP = SMTP{
		Host: os.Getenv("SMTP_HOST"),
		Port: os.Getenv("SMTP_PORT"),
		User: os.Getenv("SMTP_USER"),
		Pass: os.Getenv("SMTP_PASS"),
		From: os.Getenv("SMTP_FROM"),
	}

	appURL := strings.TrimSpace(os.Getenv("APP_URL"))
	if appURL != "" {
		normalized, err := normalizeAppURL(appURL)
		if err != nil {
			problems = append(problems, "APP_URL: "+err.Error())
		}
		cfg.AppURL = normalized
	} else if cfg.SMTP.Configured() {
		problems = append(problems,
			"APP_URL: required when SMTP is configured, because emailed links must be absolute")
	}

	proxies, err := parseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		problems = append(problems, "TRUSTED_PROXIES: "+err.Error())
	}
	cfg.TrustedProxies = proxies

	cfg.AdminEmail = NormalizeEmailAddress(os.Getenv("ADMIN_EMAIL"))
	cfg.AdminPassword = os.Getenv("ADMIN_PASSWORD")
	problems = append(problems, checkBootstrap(cfg.AdminEmail, cfg.AdminPassword)...)

	if raw := strings.TrimSpace(os.Getenv("PENDING_RETENTION")); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			problems = append(problems, "PENDING_RETENTION: "+err.Error())
		case d <= 0:
			problems = append(problems, "PENDING_RETENTION: must be positive; leave it unset for unlimited")
		default:
			cfg.PendingRetention = d
		}
	}

	demoMode, err := parseDemoMode(os.Getenv("DEMO_MODE"))
	if err != nil {
		problems = append(problems, "DEMO_MODE: "+err.Error())
	}
	cfg.DemoMode = demoMode

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

// parseDemoMode reads DEMO_MODE. Empty is false: leaving it unset is the
// ordinary, safe case and must not require typing "false" everywhere.
func parseDemoMode(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New("must be true or false")
	}
	return b, nil
}

// DemoMode reports whether DEMO_MODE is set, on its own.
//
// The `demo` CLI verb needs only this one flag and must work without the
// rest of the app's configuration — TOTP_KEY and the bootstrap pair are
// unrelated to it, and requiring them would make every demo invocation on a
// bare checkout fail for a reason that has nothing to do with demo mode.
func DemoMode() (bool, error) {
	return parseDemoMode(os.Getenv("DEMO_MODE"))
}

func loadTOTPKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("required; generate one with: head -c 32 /dev/urandom | base64")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("not valid base64: %w", err)
	}
	if len(key) != totpKeyLen {
		return nil, fmt.Errorf("must decode to %d bytes, got %d", totpKeyLen, len(key))
	}
	return key, nil
}

// normalizeAppURL enforces "origin only, no path" and returns the value
// without a trailing slash, so callers can join paths without doubling it.
func normalizeAppURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("must be an http or https URL, got %q", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("must include a host, got %q", raw)
	}
	if trimmed := strings.Trim(u.Path, "/"); trimmed != "" {
		return "", fmt.Errorf("must be an origin with no path, got path %q", u.Path)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("must be an origin with no query or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

func parseTrustedProxies(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var nets []*net.IPNet
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// A bare address is accepted as a single-host CIDR: writing
		// 10.0.0.5/32 for one proxy is easy to get subtly wrong.
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("%q is neither an IP address nor a CIDR block", entry)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid CIDR block: %w", entry, err)
		}
		nets = append(nets, network)
	}
	return nets, nil
}

// minAdminPasswordLength matches the CLI's floor, so a password set here
// cannot be weaker than one the CLI would accept.
const minAdminPasswordLength = 12

// NormalizeEmailAddress lowercases and trims an address.
func NormalizeEmailAddress(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// checkBootstrap validates the pair.
//
// Either both are set or neither is. Half-configured is refused rather than
// ignored: a deployment that sets only the address would start, create
// nobody, and give no reason why nobody can log in — which looks like the
// feature is broken rather than incomplete.
func checkBootstrap(email, password string) []string {
	switch {
	case email == "" && password == "":
		return nil
	case email == "":
		return []string{"ADMIN_EMAIL: required when ADMIN_PASSWORD is set"}
	case password == "":
		return []string{"ADMIN_PASSWORD: required when ADMIN_EMAIL is set"}
	}

	var problems []string
	if !strings.Contains(email, "@") {
		problems = append(problems, fmt.Sprintf("ADMIN_EMAIL: %q is not an email address", email))
	}
	if len([]rune(password)) < minAdminPasswordLength {
		problems = append(problems, fmt.Sprintf(
			"ADMIN_PASSWORD: must be at least %d characters", minAdminPasswordLength))
	}
	return problems
}
