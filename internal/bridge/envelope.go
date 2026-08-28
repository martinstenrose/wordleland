package bridge

import "strings"

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
		Timestamp  int64  `json:"timestamp"`

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
}

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
}

// message extracts what the bridge acts on, reporting whether the frame
// carried a group message at all.
//
// Receipts, typing indicators, read markers and everything else that is not
// a message simply have neither field set, which is the common case on a
// busy account.
func (e envelope) message() (Message, bool) {
	body := e.Envelope.DataMessage
	if body == nil && e.Envelope.SyncMessage != nil {
		body = e.Envelope.SyncMessage.SentMessage
	}
	if body == nil || body.GroupInfo == nil {
		return Message{}, false
	}
	if strings.TrimSpace(body.Message) == "" {
		// An attachment, a reaction or a group update: nothing to parse.
		return Message{}, false
	}

	return Message{
		SenderUUID: e.Envelope.SourceUUID,
		SenderName: e.Envelope.SourceName,
		GroupID:    body.GroupInfo.GroupID,
		Body:       body.Message,
	}, true
}
