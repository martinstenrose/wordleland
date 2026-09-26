package bridge

import (
	"fmt"
	"testing"
)

// mentionEnvelope is dataEnvelope with the sender having tapped somebody
// into the message: Signal puts a placeholder character in the body where
// the name sits and lists who it stands for separately.
func mentionEnvelope(body, groupID, number string) string {
	return fmt.Sprintf(`{
	  "envelope": {
	    "source": "+00000000000",
	    "sourceNumber": "+00000000000",
	    "sourceUuid": %q,
	    "sourceName": %q,
	    "timestamp": 1787490859545,
	    "serverReceivedTimestamp": 1787490858247,
	    "dataMessage": {
	      "timestamp": 1787490859545,
	      "message": %q,
	      "mentions": [{"name": %q, "number": %q, "uuid": "0d2f1b4e-0000-4000-8000-00000000abcd", "start": 0, "length": 1}],
	      "groupInfo": { "groupId": %q, "groupName": "Wordle", "type": "DELIVER" }
	    }
	  }
	}`, testUUID, testName, body, number, number, groupID)
}

// A message is for the bot when its own number is among the mentions —
// not when its name appears in the text, which anyone can type and the
// operator can change.
func TestMentionOfTheBotIsRecognised(t *testing.T) {
	msg, ok := decodeEnvelope(t, mentionEnvelope(MentionPlaceholder+" who is leading?", testGroupID, testAccount))
	if !ok {
		t.Fatal("a mention message was dropped")
	}
	if !msg.MentionsBot {
		t.Error("MentionsBot = false for a message mentioning the bridge's account")
	}
	if msg.Body != MentionPlaceholder+" who is leading?" {
		t.Errorf("Body = %q, want the placeholder kept for whoever strips it", msg.Body)
	}
}

func TestMentionOfSomebodyElseIsNotForTheBot(t *testing.T) {
	msg, ok := decodeEnvelope(t, mentionEnvelope(MentionPlaceholder+" well played", testGroupID, "+46700000001"))
	if !ok {
		t.Fatal("a mention message was dropped")
	}
	if msg.MentionsBot {
		t.Error("MentionsBot = true for a mention of another member")
	}
}

func TestPlainMessagesDoNotMentionTheBot(t *testing.T) {
	msg, _ := decodeEnvelope(t, dataEnvelope("Wordle 1 891 3/6*", testGroupID))
	if msg.MentionsBot {
		t.Error("MentionsBot = true with no mentions at all")
	}
}

// The account's own sent messages arrive as sync messages. Whatever they
// mention, the bot never addressed itself: answering would be a loop.
func TestTheBotsOwnMessagesNeverMentionIt(t *testing.T) {
	raw := fmt.Sprintf(`{
	  "envelope": {
	    "sourceUuid": %q,
	    "syncMessage": {
	      "sentMessage": {
	        "message": "%s hello",
	        "mentions": [{"number": %q, "start": 0, "length": 1}],
	        "groupInfo": { "groupId": %q }
	      }
	    }
	  }
	}`, testUUID, MentionPlaceholder, testAccount, testGroupID)
	msg, ok := decodeEnvelope(t, raw)
	if !ok {
		t.Fatal("a sync message was dropped")
	}
	if msg.MentionsBot {
		t.Error("MentionsBot = true on the account's own sent message")
	}
}
