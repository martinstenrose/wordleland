package bridge

import (
	"fmt"
	"strings"
	"testing"
)

// Every mention in a message is kept, in body order, as the account it
// stands for — "" for the bot — so a reader can put names back where the
// placeholders sit.
func TestEveryMentionIsKeptInBodyOrder(t *testing.T) {
	const bo = "9e8d7c6b-0000-4000-8000-0000000000b0"
	raw := fmt.Sprintf(`{
	  "envelope": {
	    "sourceUuid": %q,
	    "dataMessage": {
	      "message": "%s how is %s doing?",
	      "mentions": [
	        {"number": "", "uuid": %q, "start": 9, "length": 1},
	        {"number": %q, "uuid": "0d2f1b4e-0000-4000-8000-00000000abcd", "start": 0, "length": 1}
	      ],
	      "groupInfo": { "groupId": %q }
	    }
	  }
	}`, testUUID, MentionPlaceholder, MentionPlaceholder, bo, testAccount, testGroupID)

	msg, ok := decodeEnvelope(t, raw)
	if !ok {
		t.Fatal("dropped")
	}
	if !msg.MentionsBot {
		t.Error("MentionsBot = false")
	}
	if got := strings.Join(msg.Mentions, ","); got != ","+bo {
		t.Errorf("Mentions = %q, want the bot's blank first, then Bo's uuid", got)
	}
}
