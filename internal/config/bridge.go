package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/martinstenrose/wordleland/internal/i18n"
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

	// AnnounceMonths posts the month's winner back into the group when a
	// month closes. Default on: once SIGNAL_ACCOUNT and SIGNAL_GROUP_ID are
	// set at all, this is the behaviour that was asked for. The escape
	// hatch is for someone who wants the bridge to receive without the bot
	// ever speaking in the group.
	AnnounceMonths bool

	// AnnounceDays posts the day's recap when every active player has
	// filed, or just after midnight if they have not. Its own variable
	// rather than a widening of AnnounceMonths: they post at different
	// rates — one a month against one a day — and somebody who wants the
	// month's result without a daily line in the group should be able to
	// say so without also naming the other.
	AnnounceDays bool

	// AnnounceWeeks posts the week's recap when every active player has
	// filed Sunday's puzzle, or just after midnight if they have not. Its
	// own variable for the reason AnnounceDays is one.
	AnnounceWeeks bool

	// AnnounceLocale is the language the announcements are written in.
	//
	// A signed-in reader has their own locale, stored on their account; the
	// group chat has no such thing; it is not any one member's message. So
	// this is one fixed choice for the whole deployment rather than a
	// per-recipient one, unlike everywhere else the app picks a language.
	//
	// Not validated against the loaded catalogues here: an unrecognised
	// value falls back to English where the translator is built, the same
	// graceful handling a stored user locale gets, rather than failing boot
	// over a typo in a cosmetic setting.
	AnnounceLocale string

	// Replies answers a message that mentions the bot: who is leading, how
	// somebody is doing, streaks, today. Default on, like the
	// announcements, and for the same reason. Off means a mention is
	// ordinary conversation.
	Replies bool

	// LLMURL is where the language model that reads the questions is
	// served, and LLMModel which model it serves. The model turns a question
	// in any language into a small request; the answer's figures never come
	// from it. Both have compose-file defaults, as SignalAPIURL does: the
	// service name and port the ollama image always uses, and a model small
	// enough for a CPU. The app pulls the model itself if it is missing.
	LLMURL   string
	LLMModel string
}

// Defaults for the language model, overridable for running outside compose
// or for trying another model.
const (
	DefaultLLMURL   = "http://ollama:11434"
	DefaultLLMModel = "qwen2.5:3b"
)

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
		SignalAPIURL:   envOr("SIGNAL_API_URL", DefaultSignalAPIURL),
		SignalAccount:  strings.TrimSpace(os.Getenv("SIGNAL_ACCOUNT")),
		SignalGroupID:  strings.TrimSpace(os.Getenv("SIGNAL_GROUP_ID")),
		AnnounceMonths: true,
		AnnounceDays:   true,
		AnnounceWeeks:  true,
		AnnounceLocale: envOr("SIGNAL_LOCALE", i18n.Default),
		Replies:        true,
		LLMURL:         envOr("LLM_URL", DefaultLLMURL),
		LLMModel:       envOr("LLM_MODEL", DefaultLLMModel),
	}

	// Nothing configured: the bridge is off, which is a valid deployment.
	if cfg.SignalAccount == "" && cfg.SignalGroupID == "" {
		return nil, nil
	}

	var problems []string
	switch {
	case cfg.SignalAccount == "":
		problems = append(problems, "SIGNAL_ACCOUNT: required when SIGNAL_GROUP_ID is set")
	case !e164.MatchString(cfg.SignalAccount):
		// The same failure the group id is checked for, and it reached
		// production: signal-cli-rest-api routes /v1/receive/{anything},
		// so a number without its leading + connects, stays connected, and
		// matches no registered account for ever. Nothing logs a word.
		//
		// A leading + is also what YAML eats: unquoted, +46... parses as an
		// integer and the sign is gone before the value is ever templated
		// into an environment file. The value that arrives here looks like
		// a phone number and is not one.
		problems = append(problems, fmt.Sprintf(
			"SIGNAL_ACCOUNT: %q is not an E.164 number; it must start with + and "+
				"match what /v1/accounts reports. In YAML, quote it: an unquoted "+
				"+46... is parsed as an integer and loses the +",
			cfg.SignalAccount))
	}

	// Still validated: it has a default, but an override can be wrong.
	if err := checkHTTPURL(cfg.SignalAPIURL); err != nil {
		problems = append(problems, "SIGNAL_API_URL: "+err.Error())
	}

	if value, problem := envBool("SIGNAL_ANNOUNCE_MONTHS", cfg.AnnounceMonths); problem != "" {
		problems = append(problems, problem)
	} else {
		cfg.AnnounceMonths = value
	}
	if value, problem := envBool("SIGNAL_ANNOUNCE_DAYS", cfg.AnnounceDays); problem != "" {
		problems = append(problems, problem)
	} else {
		cfg.AnnounceDays = value
	}
	if value, problem := envBool("SIGNAL_ANNOUNCE_WEEKS", cfg.AnnounceWeeks); problem != "" {
		problems = append(problems, problem)
	} else {
		cfg.AnnounceWeeks = value
	}
	if value, problem := envBool("SIGNAL_REPLIES", cfg.Replies); problem != "" {
		problems = append(problems, problem)
	} else {
		cfg.Replies = value
	}
	// Only checked when it will be used: a deployment with replies off has
	// no model, and must not be refused over the URL of one.
	if cfg.Replies {
		if err := checkHTTPURL(cfg.LLMURL); err != nil {
			problems = append(problems, "LLM_URL: "+err.Error())
		}
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

// e164 is deliberately loose about length and strict about shape: the point
// is to catch a value that cannot possibly match /v1/accounts, not to police
// numbering plans.
var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

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

// envBool reads an optional boolean override, returning the problem as text
// rather than an error so LoadBridge can report every one together. An unset
// or blank variable leaves the default alone.
func envBool(name string, fallback bool) (bool, string) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, ""
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, fmt.Sprintf("%s: %q is not a boolean", name, raw)
	}
	return v, ""
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
