package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// verifyTimeout bounds one attempt. signal-cli answers these from local
// state, so a slow answer means it is still starting rather than thinking.
const (
	verifyTimeout = 5 * time.Second
	// The account and group lists come from an internal service, but they are
	// still remote input. One MiB is far beyond a plausible Signal roster and
	// keeps a broken or compromised peer from growing memory without bound.
	maxVerifyResponseSize = 1 << 20
)

// Verification is what the bridge could confirm about its own configuration
// by asking signal-cli.
//
// It exists because the two ways to misconfigure this are both silent. A
// wrong account and a wrong group each produce a websocket that connects,
// stays connected, reports healthy and receives nothing at all — for ever,
// with no error logged anywhere by anyone. Config validation catches a value
// that is the wrong shape; only signal-cli can say whether a well-formed
// value is the right one.
type Verification struct {
	// Done is false while the check has not completed — signal-cli starts
	// after the app often enough that "not yet" is an ordinary state and
	// must not read as a failure.
	Done bool
	At   time.Time

	AccountOK bool
	GroupOK   bool

	// GroupName is the name signal-cli reports for the matched group, set
	// only when GroupOK. It is the half of the check a reader can actually
	// recognise: an id that matches proves the strings are equal, a name
	// proves the account can see the group somebody meant.
	GroupName string

	// Problem is empty when everything matched, and otherwise says what is
	// wrong in terms of the variable to change.
	Problem string
}

// OK reports whether the configuration was confirmed against signal-cli.
func (v Verification) OK() bool { return v.Done && v.AccountOK && v.GroupOK }

// group is the part of /v1/groups this cares about.
type group struct {
	Name       string `json:"name"`
	InternalID string `json:"internal_id"`
}

// verifier asks signal-cli whether this configuration can possibly work.
type verifier struct {
	apiURL  string
	account string
	groupID string
	client  *http.Client
}

func newVerifier(apiURL, account, groupID string) *verifier {
	return &verifier{
		apiURL:  strings.TrimRight(apiURL, "/"),
		account: account,
		groupID: groupID,
		client:  &http.Client{Timeout: verifyTimeout},
	}
}

// check asks signal-cli about the account and the group.
//
// A transport error is returned as an error rather than a Verification: it
// means signal-cli could not be reached, which is a different thing from
// the configuration being wrong and is usually temporary at startup.
func (v *verifier) check(ctx context.Context) (Verification, error) {
	accounts, err := v.accounts(ctx)
	if err != nil {
		return Verification{}, err
	}

	result := Verification{Done: true, At: time.Now()}
	for _, a := range accounts {
		if a == v.account {
			result.AccountOK = true
			break
		}
	}
	if !result.AccountOK {
		// Deliberately does not print the configured value back: it is a
		// phone number, and the count plus the named variable is enough to
		// act on. The shape check in config already catches a missing +.
		result.Problem = fmt.Sprintf(
			"SIGNAL_ACCOUNT is not registered with signal-cli, which knows about %d account(s). "+
				"The bridge will connect and receive nothing", len(accounts))
		return result, nil
	}

	groups, err := v.groups(ctx)
	if err != nil {
		return Verification{}, err
	}
	for _, g := range groups {
		if g.InternalID == v.groupID {
			result.GroupOK, result.GroupName = true, g.Name
			break
		}
	}
	if !result.GroupOK {
		names := make([]string, 0, len(groups))
		for _, g := range groups {
			names = append(names, g.Name)
		}
		result.Problem = fmt.Sprintf(
			"SIGNAL_GROUP_ID does not match any group this account is in (%d found: %s). "+
				"It must be the bare base64 internal_id from /v1/groups",
			len(groups), strings.Join(names, ", "))
	}
	return result, nil
}

func (v *verifier) accounts(ctx context.Context) ([]string, error) {
	var accounts []string
	err := v.get(ctx, v.apiURL+"/v1/accounts", &accounts)
	return accounts, err
}

func (v *verifier) groups(ctx context.Context) ([]group, error) {
	var groups []group
	err := v.get(ctx, v.apiURL+"/v1/groups/"+url.PathEscape(v.account), &groups)
	return groups, err
}

func (v *verifier) get(ctx context.Context, endpoint string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", endpoint, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVerifyResponseSize+1))
	if err != nil {
		return fmt.Errorf("%s: %w", endpoint, err)
	}
	if len(body) > maxVerifyResponseSize {
		return fmt.Errorf("%s: response exceeds %d bytes", endpoint, maxVerifyResponseSize)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%s: %w", endpoint, err)
	}
	return nil
}
