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
	"strconv"
	"strings"
	"sync"
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
// It also remembers what it posted, so a reply quoting one of the bot's
// posts can be matched to words the bot actually wrote.
type Client struct {
	base      string
	account   string
	recipient string
	http      *http.Client
	sent      *sentLog
}

// sentLog is what this process posted, by Signal's id for each message —
// the timestamp /v2/send reports. A reply's quote names the message it
// replies to by that id, and its text is whatever the replying client put
// there; matching the id here is what makes the bot's post the bot's, and
// the stored text is the only text ever used. Bounded to the last sentCap
// posts, which at a few a day is months; a post from before the process
// started is simply not remembered.
type sentLog struct {
	mu    sync.Mutex
	order []int64
	text  map[int64]string
}

const sentCap = 256

func (l *sentLog) add(id int64, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.text == nil {
		l.text = map[int64]string{}
	}
	if _, seen := l.text[id]; !seen {
		l.order = append(l.order, id)
	}
	l.text[id] = text
	for len(l.order) > sentCap {
		delete(l.text, l.order[0])
		l.order = l.order[1:]
	}
}

func (l *sentLog) get(id int64) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	text, ok := l.text[id]
	return text, ok
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
		sent:      &sentLog{},
	}, nil
}

// Sent is the text of a post this process sent, by the message id Signal
// gave it, and whether it sent one with that id at all.
func (c *Client) Sent(id int64) (string, bool) { return c.sent.get(id) }

// NewSender builds a Sender that posts through signal-cli-rest-api's
// /v2/send: the Client's Send as a bare function, for the announcers.
func NewSender(apiURL, account, groupID string) (Sender, error) {
	c, err := NewClient(apiURL, account, groupID)
	if err != nil {
		return nil, err
	}
	return c.Send, nil
}

// Send posts one text message to the group, and remembers it by the id
// Signal gave it — the timestamp in /v2/send's answer.
func (c *Client) Send(ctx context.Context, text string) error {
	body, err := c.call(ctx, http.MethodPost, "/v2/send", struct {
		Message    string   `json:"message"`
		Number     string   `json:"number"`
		Recipients []string `json:"recipients"`
	}{Message: text, Number: c.account, Recipients: []string{c.recipient}})
	if err != nil {
		return err
	}
	var sent struct {
		Timestamp messageID `json:"timestamp"`
	}
	// A body that is not what was expected costs only the memory of this
	// post: the message is out, which is what Send is for.
	if json.Unmarshal(body, &sent) == nil && sent.Timestamp != 0 {
		c.sent.add(int64(sent.Timestamp), text)
	}
	return nil
}

// messageID is a Signal message id as /v2/send reports it. signal-cli-rest-api
// writes it as a JSON string ("1787400000000"), and reading it as a number
// would fail silently on every real call; both forms are accepted so a
// change of mind upstream costs nothing here either.
type messageID int64

func (m *messageID) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*m = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("message id %q: %w", s, err)
	}
	*m = messageID(n)
	return nil
}

// Seen reacts 👀 to a message, which Signal shows on the message itself.
// A reaction targets the original by its author and its id, the sender's
// timestamp; the author is given as the account UUID the bridge already
// keys everything by.
func (c *Client) Seen(ctx context.Context, m Message) error {
	_, err := c.call(ctx, http.MethodPost, "/v1/reactions/"+url.PathEscape(c.account), struct {
		Reaction     string `json:"reaction"`
		Recipient    string `json:"recipient"`
		TargetAuthor string `json:"target_author"`
		Timestamp    int64  `json:"timestamp"`
	}{Reaction: seenReaction, Recipient: c.recipient, TargetAuthor: m.SenderUUID, Timestamp: m.ID})
	return err
}

// Typing starts or stops the typing indicator in the group. Signal's
// clients show it for about fifteen seconds after the last start, so a
// caller that is still working repeats the start; see filer.showPresence.
func (c *Client) Typing(ctx context.Context, on bool) error {
	method := http.MethodPut
	if !on {
		method = http.MethodDelete
	}
	_, err := c.call(ctx, method, "/v1/typing-indicator/"+url.PathEscape(c.account), struct {
		Recipient string `json:"recipient"`
	}{Recipient: c.recipient})
	return err
}

// call sends one JSON request, reports any status outside success, and
// hands back the response body, bounded, for the calls that read it.
func (c *Client) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post to signal: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRemoteLen))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Sanitised for the same reason websocket.go sanitises signal-cli's
		// error frames: this text is written by signal-cli, not by us, and
		// can carry a phone number.
		return nil, fmt.Errorf("signal returned %s: %s", resp.Status, sanitizeRemote(string(respBody)))
	}
	return respBody, nil
}
