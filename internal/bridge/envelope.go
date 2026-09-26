package bridge

import (
	"log/slog"
	"strings"
	"time"
)

// redacted accepts a JSON value and discards it.
//
// forbids reading, storing or logging `source` and `sourceNumber`.
// One of them is a phone number for some senders and a UUID for others,
// depending on whether that person shares their number with the linked
// account — so a field whose contents vary by sender cannot be reasoned
// about at whatever call site ends up printing it.
//
// A field of this type has no accessible value, and formatting one yields
// "REDACTED" rather than data. Leaving the fields out of the struct
// entirely would be equally safe but would say nothing: someone would
// eventually add them back, wondering why they were missing.
type redacted struct{}

func (redacted) String() string               { return "REDACTED" }
func (redacted) GoString() string             { return "REDACTED" }
func (redacted) MarshalJSON() ([]byte, error) { return []byte(`"REDACTED"`), nil }

// UnmarshalJSON accepts anything and keeps nothing.
func (*redacted) UnmarshalJSON([]byte) error { return nil }

// envelope is one frame from signal-cli-rest-api's receive websocket.
//
// Only the fields the bridge acts on are declared; the rest of the
// frame (delivery timestamps, device ids, expiry flags) is ignored by
// encoding/json.
type envelope struct {
	// Account is the registered number the frame belongs to. It is not
	// always present, and nothing depends on it — the connection is already
	// per-account — so it is read only for logging that it disagreed.
	Account string `json:"account"`

	Envelope struct {
		SourceUUID string `json:"sourceUuid"`
		SourceName string `json:"sourceName"`

		// Two clocks, milliseconds since the epoch. Timestamp is the
		// sender's device clock, which is Signal's message id and can be
		// wrong. ServerReceivedTimestamp is when Signal's server accepted
		// the message: when it reached the group, on a clock nobody in the
		// group controls, and the one a posting time is taken from.
		Timestamp               int64 `json:"timestamp"`
		ServerReceivedTimestamp int64 `json:"serverReceivedTimestamp"`

		// Declared so the exclusion is visible here rather than inferred
		// from an absence, and typed so neither can be read or printed.
		Source       redacted `json:"source"`
		SourceNumber redacted `json:"sourceNumber"`

		// DataMessage carries a message someone else sent to the group.
		DataMessage *dataMessage `json:"dataMessage"`

		// SyncMessage carries a message this account sent from one of its
		// own devices. The bot runs as a linked device on the operator's
		// account, so the operator's own results arrive here rather than in
		// DataMessage — reading only DataMessage would silently skip one
		// player entirely.
		SyncMessage *struct {
			SentMessage *dataMessage `json:"sentMessage"`
		} `json:"syncMessage"`
	} `json:"envelope"`
}

// dataMessage is the part of a message the bridge reads.
//
// Known limitation: edited messages are ignored. A correction arrives as a
// separate edit referring back to the original, which this does not follow,
// so the first version of a message is the one forwarded. At a dozen
// messages a day that is an acceptable trade rather than an oversight — but
// it is a real behaviour, so it is recorded here rather than left to be
// rediscovered as a bug.
type dataMessage struct {
	Message   string `json:"message"`
	GroupInfo *struct {
		GroupID string `json:"groupId"`
	} `json:"groupInfo"`

	// Mentions is who the sender tapped into the message. A mention is
	// not text: the body carries a placeholder character where the name
	// sits, and this list says which account it stands for, so renaming
	// the bot's profile changes nothing about whether it was addressed.
	//
	// Only the number is read, and only to compare against the bridge's
	// own account: whether somebody else was mentioned is none of the
	// bridge's business, and the value never leaves message().
	Mentions []struct {
		Number string `json:"number"`
	} `json:"mentions"`

	// Quote is the message this one replies to, when it is a reply. Read
	// for one purpose: a question asked under one of the bot's own posts
	// carries that post along, so "what does this mean?" has a this. The
	// author is compared against the bridge's account and never kept, and
	// anybody else's quoted words are dropped unread.
	Quote *struct {
		Author       string `json:"author"`
		AuthorNumber string `json:"authorNumber"`
		Text         string `json:"text"`
	} `json:"quote"`
}

// MentionPlaceholder is the character Signal puts in a message body where a
// mention sits. It is the object replacement character, U+FFFC.
const MentionPlaceholder = "￼"

// Message is one incoming message reduced to what the bridge needs.
type Message struct {
	// SenderUUID is the account UUID: stable, and never the profile name,
	// which the sender can change at any time.
	SenderUUID string
	// SenderName is the current profile name, passed on only as a display
	// hint so a human can recognise the sender when claiming them.
	SenderName string
	// GroupID is the bare base64 form that arrives on messages.
	GroupID string
	// Body is the message text.
	Body string
	// PostedAt is when the message reached the group: Signal's server time,
	// falling back to the sender's, zero when the frame carried neither.
	PostedAt time.Time
	// ID is Signal's id for the message — the sender's timestamp, in
	// milliseconds — which is what a reaction to it has to name.
	ID int64
	// MentionsBot says the sender tapped the bridge's own account into the
	// message: a question for the bot rather than a result or conversation.
	// Never set on the account's own sent messages, so the bot cannot be
	// made to answer itself.
	MentionsBot bool
	// Quoted is the text of the bot's own post this message replies to,
	// empty when it is not a reply or replies to somebody else.
	Quoted string
}

// message extracts what the bridge acts on, reporting whether the frame
// carried a group message at all. account is the bridge's own number, for
// telling a mention of the bot from any other.
//
// Receipts, typing indicators, read markers and everything else that is not
// a message simply have neither field set, which is the common case on a
// busy account.
func (e envelope) message(account string, logger *slog.Logger) (Message, bool) {
	body := e.Envelope.DataMessage
	mentionsBot := false
	quoted := ""
	if body != nil {
		for _, m := range body.Mentions {
			if m.Number != "" && m.Number == account {
				mentionsBot = true
			}
		}
		if q := body.Quote; q != nil && account != "" && (q.AuthorNumber == account || q.Author == account) {
			quoted = q.Text
		}
	}
	if body == nil && e.Envelope.SyncMessage != nil {
		body = e.Envelope.SyncMessage.SentMessage
	}
	if body == nil || body.GroupInfo == nil {
		// The bulk of the traffic on a busy account: receipts, typing
		// indicators, reactions, attachments, group updates, and DMs. Never
		// logged before this, which made it indistinguishable from nothing
		// arriving at all.
		logger.Debug("frame carried no group message", "sender", e.Envelope.SourceUUID)
		return Message{}, false
	}
	if e.Envelope.SourceUUID == "" {
		// Nothing to file a result against: the UUID is the external id
		// every submission is keyed by. Forwarding it anyway would fail
		// ingest's validation and be logged as a parser bug, which this is
		// not — it is a frame the bridge cannot attribute.
		return Message{}, false
	}
	if strings.TrimSpace(body.Message) == "" {
		// An attachment, a reaction or a group update: nothing to parse.
		logger.Debug("group message has no text", "sender", e.Envelope.SourceUUID)
		return Message{}, false
	}

	return Message{
		SenderUUID:  e.Envelope.SourceUUID,
		SenderName:  e.Envelope.SourceName,
		GroupID:     body.GroupInfo.GroupID,
		Body:        body.Message,
		PostedAt:    e.postedAt(),
		ID:          e.Envelope.Timestamp,
		MentionsBot: mentionsBot,
		Quoted:      quoted,
	}, true
}

// postedAt prefers the server's clock to the sender's, and reports zero
// rather than the epoch when the frame has neither.
func (e envelope) postedAt() time.Time {
	switch {
	case e.Envelope.ServerReceivedTimestamp > 0:
		return time.UnixMilli(e.Envelope.ServerReceivedTimestamp)
	case e.Envelope.Timestamp > 0:
		return time.UnixMilli(e.Envelope.Timestamp)
	default:
		return time.Time{}
	}
}
