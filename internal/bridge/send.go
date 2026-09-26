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

// seenReaction is what the bot puts on a question the moment it has read
// it: the answer takes seconds, and a chat with no sign of life for
// seconds reads as a bot that did not hear.
const seenReaction = "👀"

// Sender posts one text message to the configured group.
type Sender func(ctx context.Context, text string) error

// Client is the bridge's side of signal-cli-rest-api for the one group:
// posting to it, and the two small signs that a question has been seen.
type Client struct {
	base      string
	account   string
	recipient string
	http      *http.Client
}

// NewClient builds a Client for the group. It does not connect.
func NewClient(apiURL, account, groupID string) (*Client, error) {
	base, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("signal api url: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("signal api url must be http or https")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	return &Client{
		base:      base.String(),
		account:   account,
		recipient: sendGroupPrefix + base64.StdEncoding.EncodeToString([]byte(groupID)),
		http:      &http.Client{Timeout: sendTimeout},
	}, nil
}

// NewSender builds a Sender that posts through signal-cli-rest-api's
// /v2/send: the Client's Send as a bare function, for the announcers.
func NewSender(apiURL, account, groupID string) (Sender, error) {
	c, err := NewClient(apiURL, account, groupID)
	if err != nil {
		return nil, err
	}
	return c.Send, nil
}

// Send posts one text message to the group.
func (c *Client) Send(ctx context.Context, text string) error {
	return c.call(ctx, http.MethodPost, "/v2/send", struct {
		Message    string   `json:"message"`
		Number     string   `json:"number"`
		Recipients []string `json:"recipients"`
	}{Message: text, Number: c.account, Recipients: []string{c.recipient}})
}

// Seen reacts 👀 to a message, which Signal shows on the message itself.
// A reaction targets the original by its author and its id, the sender's
// timestamp; the author is given as the account UUID the bridge already
// keys everything by.
func (c *Client) Seen(ctx context.Context, m Message) error {
	return c.call(ctx, http.MethodPost, "/v1/reactions/"+url.PathEscape(c.account), struct {
		Reaction     string `json:"reaction"`
		Recipient    string `json:"recipient"`
		TargetAuthor string `json:"target_author"`
		Timestamp    int64  `json:"timestamp"`
	}{Reaction: seenReaction, Recipient: c.recipient, TargetAuthor: m.SenderUUID, Timestamp: m.ID})
}

// Typing starts or stops the typing indicator in the group. Signal's
// clients show it for about fifteen seconds after the last start, so a
// caller that is still working repeats the start; see filer.showPresence.
func (c *Client) Typing(ctx context.Context, on bool) error {
	method := http.MethodPut
	if !on {
		method = http.MethodDelete
	}
	return c.call(ctx, method, "/v1/typing-indicator/"+url.PathEscape(c.account), struct {
		Recipient string `json:"recipient"`
	}{Recipient: c.recipient})
}

// call sends one JSON request and reports any status outside success.
func (c *Client) call(ctx context.Context, method, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
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
}
