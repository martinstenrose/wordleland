package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Bridge is the Signal bridge's resolved configuration.
//
// The bridge is optional. Its settings are required together or not at all:
// configured means it runs, absent means the app serves without it. That is
// what lets a fresh deployment come up and import history before Signal is
// linked, which used to be impossible — it was its own container
// and crash-looped until every variable was set.
//
// Half-configured is still refused. A deployment that sets the account but
// not the group would start, watch nothing, and look exactly like one that
// is working.
type Bridge struct {
	// SignalAPIURL is the signal-cli-rest-api base URL.
	SignalAPIURL string
	// SignalAccount is the registered number the linked device belongs to.
	SignalAccount string

	// SignalGroupID is the group to watch.
	//
	// This is the bare base64 internal_id from /v1/groups, not the
	// "group.<base64>" form the same endpoint reports as id. Comparing
	// against the wrong one matches no messages at all and looks exactly
	// like the bot receiving nothing, so the value is checked at boot
	// rather than left to fail silently at runtime.
	SignalGroupID string
}

// groupIDPrefix is the form that is easy to copy by mistake.
const groupIDPrefix = "group."

// DefaultSignalAPIURL is fixed by the compose file rather than configured:
// it is a service name in it joined to the port that image always exposes,
// so it cannot change without editing that file, at which point editing
// this constant is no harder. Overridable for running outside compose, the
// same escape hatch the -db flag is.
const DefaultSignalAPIURL = "http://signal-cli-rest-api:8080"

// LoadBridge reads the bridge's environment.
//
// A nil config and a nil error means the bridge is not configured, which is
// not a failure. Every problem is reported together: a fresh deployment
// usually has more than one variable wrong.
func LoadBridge() (*Bridge, error) {
	cfg := &Bridge{
		SignalAPIURL:  envOr("SIGNAL_API_URL", DefaultSignalAPIURL),
		SignalAccount: strings.TrimSpace(os.Getenv("SIGNAL_ACCOUNT")),
		SignalGroupID: strings.TrimSpace(os.Getenv("SIGNAL_GROUP_ID")),
	}

	// Nothing configured: the bridge is off, which is a valid deployment.
	if cfg.SignalAccount == "" && cfg.SignalGroupID == "" {
		return nil, nil
	}

	var problems []string
	if cfg.SignalAccount == "" {
		problems = append(problems, "SIGNAL_ACCOUNT: required when SIGNAL_GROUP_ID is set")
	}

	// Still validated: it has a default, but an override can be wrong.
	if err := checkHTTPURL(cfg.SignalAPIURL); err != nil {
		problems = append(problems, "SIGNAL_API_URL: "+err.Error())
	}

	switch {
	case cfg.SignalGroupID == "":
		problems = append(problems, "SIGNAL_GROUP_ID: required when SIGNAL_ACCOUNT is set")
	case strings.HasPrefix(cfg.SignalGroupID, groupIDPrefix):
		// Caught here because the alternative is a bot that connects,
		// reports itself healthy and matches nothing for ever.
		problems = append(problems, fmt.Sprintf(
			"SIGNAL_GROUP_ID: looks like the %q form reported as 'id' by /v1/groups; "+
				"use the bare base64 'internal_id' instead, which is what arrives on messages",
			groupIDPrefix))
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must be an http or https URL, got %q", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("must include a host, got %q", raw)
	}
	return nil
}

// envOr reads a variable, falling back when it is unset or blank. Blank
// counts as unset: a compose file that passes ${VAR} for a variable nobody
// set delivers an empty string, not an absent one.
func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
