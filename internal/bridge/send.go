package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sendTimeout bounds one outbound call to signal-cli-rest-api, so a hung
// server cannot stall the worker that also files results: sending and
// filing share one goroutine, see filer.handle.
const sendTimeout = 15 * time.Second

// sendGroupPrefix is how a group is addressed when sending, which differs
// from receiving.
//
// SIGNAL_GROUP_ID holds the bare base64 internal_id used on received
// messages. signal-cli-rest-api 0.100's /v2/send expects that entire string
// base64-encoded once more and prefixed with "group.". Build that transport
// representation here so the operator only configures the canonical id.
const sendGroupPrefix = "group."

// Sender posts one text message to the configured group.
type Sender func(ctx context.Context, text string) error

// NewSender builds a Sender that posts through signal-cli-rest-api's /v2/send.
func NewSender(apiURL, account, groupID string) (Sender, error) {
	base, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("signal api url: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("signal api url must be http or https")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v2/send"
	endpoint := base.String()
	recipient := sendGroupPrefix + base64.StdEncoding.EncodeToString([]byte(groupID))

	client := &http.Client{Timeout: sendTimeout}

	return func(ctx context.Context, text string) error {
		body, err := json.Marshal(struct {
			Message    string   `json:"message"`
			Number     string   `json:"number"`
			Recipients []string `json:"recipients"`
		}{Message: text, Number: account, Recipients: []string{recipient}})
		if err != nil {
			return fmt.Errorf("encode send request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build send request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("post to signal: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRemoteLen))
			// Sanitised for the same reason websocket.go sanitises signal-cli's
			// error frames: this text is written by signal-cli, not by us, and
			// can carry a phone number.
			return fmt.Errorf("signal returned %s: %s", resp.Status, sanitizeRemote(string(respBody)))
		}
		return nil
	}, nil
}
