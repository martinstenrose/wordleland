package config

import (
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

// validKey is 32 bytes base64-encoded, the only length loadTOTPKey accepts.
const validKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string // substring; empty means success expected
		check   func(*testing.T, *Config)
	}{
		{
			name: "minimal",
			env:  map[string]string{"TOTP_KEY": validKey},
			check: func(t *testing.T, c *Config) {
				if c.DBPath != DefaultDBPath {
					t.Errorf("DBPath = %q, want %q", c.DBPath, DefaultDBPath)
				}
				if len(c.TOTPKey) != totpKeyLen {
					t.Errorf("TOTPKey length = %d, want %d", len(c.TOTPKey), totpKeyLen)
				}
				if c.PendingRetention != 0 {
					t.Errorf("PendingRetention = %v, want 0 (unlimited)", c.PendingRetention)
				}
				if c.LogLevel != slog.LevelInfo {
					t.Errorf("LogLevel = %v, want info when LOG_LEVEL is unset", c.LogLevel)
				}
			},
		},
		{
			name: "LOG_LEVEL debug",
			env:  map[string]string{"TOTP_KEY": validKey, "LOG_LEVEL": "debug"},
			check: func(t *testing.T, c *Config) {
				if c.LogLevel != slog.LevelDebug {
					t.Errorf("LogLevel = %v, want debug", c.LogLevel)
				}
			},
		},
		{
			name: "LOG_LEVEL is case-insensitive",
			env:  map[string]string{"TOTP_KEY": validKey, "LOG_LEVEL": "WARN"},
			check: func(t *testing.T, c *Config) {
				if c.LogLevel != slog.LevelWarn {
					t.Errorf("LogLevel = %v, want warn", c.LogLevel)
				}
			},
		},
		{
			// A typo that silently fell back to a default would disable the
			// exact logging this variable exists to turn on, so it fails
			// startup and names what would have worked instead.
			name:    "LOG_LEVEL unrecognised",
			env:     map[string]string{"TOTP_KEY": validKey, "LOG_LEVEL": "verbose"},
			wantErr: "LOG_LEVEL: must be one of debug, info, warn, error",
		},
		{
			name:    "missing TOTP_KEY",
			env:     map[string]string{},
			wantErr: "TOTP_KEY: required",
		},
		{
			name:    "TOTP_KEY not base64",
			env:     map[string]string{"TOTP_KEY": "not base64!!"},
			wantErr: "not valid base64",
		},
		{
			name:    "TOTP_KEY wrong length",
			env:     map[string]string{"TOTP_KEY": "c2hvcnQ="},
			wantErr: "must decode to 32 bytes, got 5",
		},
		{
			//: APP_URL is required whenever mail can actually be sent,
			// because emailed links must be absolute and cannot be derived
			// from the request Host header.
			name: "SMTP configured without APP_URL",
			env: map[string]string{
				"TOTP_KEY":  validKey,
				"SMTP_HOST": "smtp.example.tld",
				"SMTP_FROM": "wordle@example.tld",
			},
			wantErr: "APP_URL: required when SMTP is configured",
		},
		{
			name: "SMTP unconfigured needs no APP_URL",
			env:  map[string]string{"TOTP_KEY": validKey},
			check: func(t *testing.T, c *Config) {
				if c.SMTP.Configured() {
					t.Error("SMTP.Configured() = true, want false")
				}
			},
		},
		{
			name: "SMTP configured with APP_URL",
			env: map[string]string{
				"TOTP_KEY":  validKey,
				"SMTP_HOST": "smtp.example.tld",
				"SMTP_FROM": "wordle@example.tld",
				"APP_URL":   "https://wordle.example.tld",
			},
			check: func(t *testing.T, c *Config) {
				if !c.SMTP.Configured() {
					t.Error("SMTP.Configured() = false, want true")
				}
				if c.AppURL != "https://wordle.example.tld" {
					t.Errorf("AppURL = %q", c.AppURL)
				}
			},
		},
		{
			name:    "APP_URL with a path",
			env:     map[string]string{"TOTP_KEY": validKey, "APP_URL": "https://example.tld/wordle"},
			wantErr: "must be an origin with no path",
		},
		{
			name: "APP_URL trailing slash is not a path",
			env:  map[string]string{"TOTP_KEY": validKey, "APP_URL": "https://example.tld/"},
			check: func(t *testing.T, c *Config) {
				if c.AppURL != "https://example.tld" {
					t.Errorf("AppURL = %q, want the trailing slash trimmed", c.AppURL)
				}
			},
		},
		{
			name:    "APP_URL without a scheme",
			env:     map[string]string{"TOTP_KEY": validKey, "APP_URL": "wordle.example.tld"},
			wantErr: "must be an http or https URL",
		},
		{
			name: "PENDING_RETENTION parsed",
			env:  map[string]string{"TOTP_KEY": validKey, "PENDING_RETENTION": "720h"},
			check: func(t *testing.T, c *Config) {
				if c.PendingRetention != 720*time.Hour {
					t.Errorf("PendingRetention = %v, want 720h", c.PendingRetention)
				}
			},
		},
		{
			name:    "PENDING_RETENTION negative",
			env:     map[string]string{"TOTP_KEY": validKey, "PENDING_RETENTION": "-1h"},
			wantErr: "must be positive",
		},
		{
			// Every problem is reported together, so a fresh deployment does
			// not need one restart per mistake.
			name:    "problems are reported together",
			env:     map[string]string{"PENDING_RETENTION": "nonsense"},
			wantErr: "TOTP_KEY",
			check:   nil,
		},
		{
			name: "DEMO_MODE unset defaults to false",
			env:  map[string]string{"TOTP_KEY": validKey},
			check: func(t *testing.T, c *Config) {
				if c.DemoMode {
					t.Error("DemoMode = true, want false")
				}
			},
		},
		{
			name: "DEMO_MODE true",
			env:  map[string]string{"TOTP_KEY": validKey, "DEMO_MODE": "true"},
			check: func(t *testing.T, c *Config) {
				if !c.DemoMode {
					t.Error("DemoMode = false, want true")
				}
			},
		},
		{
			name:    "DEMO_MODE not a bool",
			env:     map[string]string{"TOTP_KEY": validKey, "DEMO_MODE": "yes please"},
			wantErr: "DEMO_MODE: must be true or false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			// Clear anything the test did not set, so a variable leaking in
			// from the environment cannot change the result.
			for _, k := range []string{
				"TOTP_KEY", "APP_URL", "TRUSTED_PROXIES", "PENDING_RETENTION",
				"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM",
				"ADMIN_EMAIL", "ADMIN_PASSWORD", "DEMO_MODE", "LOG_LEVEL",
			} {
				if _, ok := tt.env[k]; !ok {
					t.Setenv(k, "")
				}
			}

			cfg, err := Load("")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() succeeded, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestLoadMultipleProblemsReported(t *testing.T) {
	t.Setenv("TOTP_KEY", "")
	t.Setenv("PENDING_RETENTION", "nonsense")
	t.Setenv("TRUSTED_PROXIES", "not-a-cidr")

	_, err := Load("")
	if err == nil {
		t.Fatal("Load() succeeded, want an error")
	}
	for _, want := range []string{"TOTP_KEY", "PENDING_RETENTION", "TRUSTED_PROXIES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %s; got:\n%v", want, err)
		}
	}
}

func TestLoadDBPathFlagOverrides(t *testing.T) {
	t.Setenv("TOTP_KEY", validKey)

	cfg, err := Load("/tmp/local.db")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.DBPath != "/tmp/local.db" {
		t.Errorf("DBPath = %q, want the -db flag value", cfg.DBPath)
	}
}

// The demo CLI verb needs only this one flag, and must not be made to
// satisfy the rest of the app's configuration just to check it.
func TestDemoModeStandalone(t *testing.T) {
	t.Setenv("TOTP_KEY", "")

	on, err := DemoMode()
	if err != nil {
		t.Fatalf("DemoMode() failed: %v", err)
	}
	if on {
		t.Error("DemoMode() = true with DEMO_MODE unset")
	}

	t.Setenv("DEMO_MODE", "true")
	on, err = DemoMode()
	if err != nil {
		t.Fatalf("DemoMode() failed: %v", err)
	}
	if !on {
		t.Error("DemoMode() = false with DEMO_MODE=true")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantCount int
		wantErr   string
		contains  string // an address that must fall inside a parsed block
	}{
		{name: "empty", raw: "", wantCount: 0},
		{name: "single CIDR", raw: "10.0.0.0/8", wantCount: 1, contains: "10.1.2.3"},
		{
			name:      "bare address becomes a single-host block",
			raw:       "172.20.0.5",
			wantCount: 1,
			contains:  "172.20.0.5",
		},
		{
			name:      "several entries with surrounding space",
			raw:       " 10.0.0.0/8 , 192.168.0.0/16 ",
			wantCount: 2,
			contains:  "192.168.1.1",
		},
		{name: "IPv6 CIDR", raw: "fd00::/8", wantCount: 1, contains: "fd00::1"},
		{name: "bare IPv6", raw: "fd00::1", wantCount: 1, contains: "fd00::1"},
		{name: "garbage", raw: "not-a-cidr", wantErr: "neither an IP address nor a CIDR"},
		{name: "bad mask", raw: "10.0.0.0/99", wantErr: "not a valid CIDR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nets, err := parseTrustedProxies(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTrustedProxies(%q) failed: %v", tt.raw, err)
			}
			if len(nets) != tt.wantCount {
				t.Fatalf("got %d blocks, want %d", len(nets), tt.wantCount)
			}
			if tt.contains == "" {
				return
			}
			ip := net.ParseIP(tt.contains)
			var found bool
			for _, n := range nets {
				if n.Contains(ip) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no parsed block contains %s", tt.contains)
			}
		})
	}
}

// Either both are set or neither: half-configured would start, create
// nobody, and give no reason why nobody can log in.
func TestBootstrapAdminConfig(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
		wantErr  string
	}{
		{name: "neither set"},
		{name: "both set", email: "admin@example.tld", password: "correct horse battery staple"},
		{
			name: "email without password", email: "admin@example.tld",
			wantErr: "ADMIN_PASSWORD: required when ADMIN_EMAIL is set",
		},
		{
			name: "password without email", password: "correct horse battery staple",
			wantErr: "ADMIN_EMAIL: required when ADMIN_PASSWORD is set",
		},
		{
			name: "password too short", email: "admin@example.tld", password: "short",
			wantErr: "at least 12 characters",
		},
		{
			name: "not an email address", email: "admin", password: "correct horse battery staple",
			wantErr: "is not an email address",
		},
		{
			// This address ends up in a To: header, so a newline in it is a
			// header of somebody else's choosing. "Contains an @" let it past.
			name:     "address carrying a header break",
			email:    "admin@example.tld\nBcc: attacker@example.tld",
			password: "correct horse battery staple",
			wantErr:  "is not an email address",
		},
		{
			// A display name here would go on the message unasked, and this
			// field holds an address.
			name: "address with a display name", email: "Someone <admin@example.tld>",
			password: "correct horse battery staple",
			wantErr:  "is not an email address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOTP_KEY", validKey)
			t.Setenv("ADMIN_EMAIL", tt.email)
			t.Setenv("ADMIN_PASSWORD", tt.password)
			for _, k := range []string{"APP_URL", "TRUSTED_PROXIES", "PENDING_RETENTION",
				"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM"} {
				t.Setenv(k, "")
			}

			cfg, err := Load("")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() succeeded, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}
			if tt.email != "" && cfg.AdminEmail != tt.email {
				t.Errorf("AdminEmail = %q, want %q", cfg.AdminEmail, tt.email)
			}
		})
	}
}

func TestBootstrapAdminEmailIsNormalized(t *testing.T) {
	t.Setenv("TOTP_KEY", validKey)
	t.Setenv("ADMIN_EMAIL", "  Admin@Example.TLD ")
	t.Setenv("ADMIN_PASSWORD", "correct horse battery staple")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.AdminEmail != "admin@example.tld" {
		t.Errorf("AdminEmail = %q, want it normalized", cfg.AdminEmail)
	}
}
