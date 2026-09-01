package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// Fixtures use a synthetic uuid, group id and profile name. The real
// envelope this was verified against carries a phone number, an account
// uuid and a group member's display name, none of which belong in the repo
// (CLAUDE.md, "Personal data").
const (
	testUUID    = "b1d4e8a2-0000-4000-8000-0123456789ab"
	testGroupID = "c2FtcGxlLWdyb3VwLWlkLXZhbHVlLWZvci10ZXN0cw=="
	testName    = "Sample Sender"
)

// dataEnvelope is the shape a message from someone else arrives in, with
// the same field set as a real one including the parts the filer
// ignores.
func dataEnvelope(body, groupID string) string {
	return fmt.Sprintf(`{
	  "envelope": {
	    "source": "+00000000000",
	    "sourceNumber": "+00000000000",
	    "sourceUuid": %q,
	    "sourceName": %q,
	    "sourceDevice": 1,
	    "timestamp": 1787490859545,
	    "serverReceivedTimestamp": 1787490858247,
	    "serverDeliveredTimestamp": 1787492351462,
	    "dataMessage": {
	      "timestamp": 1787490859545,
	      "message": %q,
	      "expiresInSeconds": 0,
	      "isExpirationUpdate": false,
	      "viewOnce": false,
	      "groupInfo": {
	        "groupId": %q,
	        "groupName": "Wordle",
	        "revision": 19,
	        "type": "DELIVER"
	      }
	    }
	  }
	}`, testUUID, testName, body, groupID)
}

// syncEnvelope is how the operator's own posts arrive, because the bot runs
// as a linked device on their account.
func syncEnvelope(body, groupID string) string {
	return fmt.Sprintf(`{
	  "envelope": {
	    "source": "+00000000000",
	    "sourceNumber": "+00000000000",
	    "sourceUuid": %q,
	    "sourceName": %q,
	    "timestamp": 1787490859545,
	    "syncMessage": {
	      "sentMessage": {
	        "timestamp": 1787490859545,
	        "message": %q,
	        "groupInfo": { "groupId": %q, "groupName": "Wordle", "type": "DELIVER" }
	      }
	    }
	  }
	}`, testUUID, testName, body, groupID)
}

func decodeEnvelope(t *testing.T, raw string) (Message, bool) {
	t.Helper()
	msg, ok, _ := decodeEnvelopeLogged(t, raw)
	return msg, ok
}

// decodeEnvelopeLogged is decodeEnvelope with the debug output a test can
// inspect, for the tests that care what got logged rather than only what
// got extracted.
func decodeEnvelopeLogged(t *testing.T, raw string) (Message, bool, string) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	msg, ok := env.message(logger)
	return msg, ok, logs.String()
}

func TestEnvelopeExtraction(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantOK  bool
		wantMsg string
	}{
		{
			name:   "message from another member",
			raw:    dataEnvelope("Wordle 1 891 3/6*", testGroupID),
			wantOK: true, wantMsg: "Wordle 1 891 3/6*",
		},
		{
			// Without this the operator's own results are never forwarded,
			// and they are one of the players.
			name:   "the operator's own message, as a sync message",
			raw:    syncEnvelope("Wordle 1 891 4/6", testGroupID),
			wantOK: true, wantMsg: "Wordle 1 891 4/6",
		},
		{
			name:   "receipt with neither message kind",
			raw:    `{"envelope":{"sourceUuid":"x","receiptMessage":{"when":1}}}`,
			wantOK: false,
		},
		{
			name:   "typing indicator",
			raw:    `{"envelope":{"sourceUuid":"x","typingMessage":{"action":"STARTED"}}}`,
			wantOK: false,
		},
		{
			// A direct message rather than a group one.
			name:   "message with no group",
			raw:    `{"envelope":{"sourceUuid":"x","dataMessage":{"message":"hello"}}}`,
			wantOK: false,
		},
		{
			// A reaction or an attachment with no caption.
			name:   "group message with an empty body",
			raw:    dataEnvelope("", testGroupID),
			wantOK: false,
		},
		{
			name:   "sync message that is not a sent message",
			raw:    `{"envelope":{"sourceUuid":"x","syncMessage":{"readMessages":[]}}}`,
			wantOK: false,
		},
		{
			// The uuid is the external id a result is filed under, so a frame
			// without one cannot be attributed. Forwarding it anyway would be
			// rejected by ingest and logged as a parser bug, which it is not.
			name: "group message with no sender uuid",
			raw: fmt.Sprintf(`{"envelope":{"sourceName":%q,"dataMessage":{"message":"Wordle 1 891 3/6",
				"groupInfo":{"groupId":%q,"type":"DELIVER"}}}}`, testName, testGroupID),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := decodeEnvelope(t, tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if msg.Body != tt.wantMsg {
				t.Errorf("Body = %q, want %q", msg.Body, tt.wantMsg)
			}
			if msg.SenderUUID != testUUID {
				t.Errorf("SenderUUID = %q, want %q", msg.SenderUUID, testUUID)
			}
			if msg.SenderName != testName {
				t.Errorf("SenderName = %q, want %q", msg.SenderName, testName)
			}
			if msg.GroupID != testGroupID {
				t.Errorf("GroupID = %q, want %q", msg.GroupID, testGroupID)
			}
		})
	}
}

// Reading, storing or logging source and sourceNumber is forbidden. The type
// is what enforces it: the fields are declared so the exclusion is visible,
// but hold nothing and print as REDACTED.
func TestRedactedFieldsCannotLeak(t *testing.T) {
	const phone = "+00000000000"
	raw := fmt.Sprintf(
		`{"envelope":{"source":%q,"sourceNumber":%q,"sourceUuid":"u","dataMessage":{"message":"hi","groupInfo":{"groupId":"g"}}}}`,
		phone, phone)

	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Every way someone might accidentally print them. The %s on a plain
	// string is the point rather than an oversight: it is one of the ways
	// this leaks in real code, so the test has to cover it.
	for _, rendered := range []string{
		fmt.Sprint(env.Envelope.Source),
		fmt.Sprintf("%v", env.Envelope.SourceNumber),
		//lint:ignore S1025 the redundant Sprintf is the case under test
		fmt.Sprintf("%s", env.Envelope.Source),
		fmt.Sprintf("%+v", env.Envelope),
		fmt.Sprintf("%#v", env.Envelope.SourceNumber),
	} {
		if strings.Contains(rendered, phone) {
			t.Errorf("a phone number survived formatting: %s", rendered)
		}
	}
	if got := fmt.Sprint(env.Envelope.Source); got != "REDACTED" {
		t.Errorf("Source printed as %q, want REDACTED", got)
	}

	// And re-encoding the envelope, which is how a naive debug dump would
	// most likely happen.
	encoded, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), phone) {
		t.Errorf("a phone number survived re-encoding: %s", encoded)
	}
}

// Frames carry fields the filer does not model; they must be ignored
// rather than rejected, since signal-cli adds to this shape over time.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	raw := `{"envelope":{"sourceUuid":"u","somethingNew":{"a":1},
	  "dataMessage":{"message":"hi","somethingElse":true,"groupInfo":{"groupId":"g","futureField":9}}}}`

	msg, ok := decodeEnvelope(t, raw)
	if !ok {
		t.Fatal("a frame with unknown fields was rejected")
	}
	if msg.Body != "hi" {
		t.Errorf("Body = %q", msg.Body)
	}
}

// Most of the traffic on a busy account is receipts, typing indicators,
// reactions and DMs, silently dropped before this change. Debug is what
// makes that traffic visible as traffic rather than nothing happening.
func TestNonGroupMessagesLogAtDebug(t *testing.T) {
	tests := map[string]string{
		"receipt":          `{"envelope":{"sourceUuid":"x","receiptMessage":{"when":1}}}`,
		"DM, no group":     `{"envelope":{"sourceUuid":"x","dataMessage":{"message":"hello"}}}`,
		"empty group body": dataEnvelope("", testGroupID),
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, ok, log := decodeEnvelopeLogged(t, raw)
			if ok {
				t.Fatal("expected the frame to be dropped")
			}
			if log == "" {
				t.Error("a dropped frame logged nothing at debug")
			}
		})
	}
}
