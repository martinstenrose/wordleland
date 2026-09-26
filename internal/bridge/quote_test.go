package bridge

import (
	"fmt"
	"testing"
)

// replyEnvelope is a mention of the bot posted as a reply to an earlier
// message: Signal carries the quoted message's author and text along.
func replyEnvelope(body, quotedAuthor, quotedText string) string {
	return fmt.Sprintf(`{
	  "envelope": {
	    "sourceUuid": %q,
	    "sourceName": %q,
	    "timestamp": 1787490859545,
	    "dataMessage": {
	      "message": %q,
	      "mentions": [{"number": %q, "start": 0, "length": 1}],
	      "quote": {"id": 1787400000000, "author": %q, "authorNumber": %q, "authorUuid": "0d2f1b4e-0000-4000-8000-00000000abcd", "text": %q},
	      "groupInfo": { "groupId": %q }
	    }
	  }
	}`, testUUID, testName, body, testAccount, quotedAuthor, quotedAuthor, quotedText, testGroupID)
}

func TestAReplyToTheBotsOwnPostCarriesIt(t *testing.T) {
	msg, ok := decodeEnvelope(t, replyEnvelope(MentionPlaceholder+" what does this mean?", testAccount, "Wordle 1891 — everyone's in."))
	if !ok {
		t.Fatal("a reply was dropped")
	}
	if !msg.MentionsBot {
		t.Error("MentionsBot = false on a reply that mentions the bot")
	}
	if msg.Quoted != "Wordle 1891 — everyone's in." {
		t.Errorf("Quoted = %q, want the bot's post", msg.Quoted)
	}
}

// Somebody else's words are not the bot's business, replied to or not.
func TestAReplyToAnotherMemberCarriesNothing(t *testing.T) {
	msg, ok := decodeEnvelope(t, replyEnvelope(MentionPlaceholder+" is this right?", "+46700000001", "private words"))
	if !ok {
		t.Fatal("a reply was dropped")
	}
	if msg.Quoted != "" {
		t.Errorf("Quoted = %q, want nothing from another member's message", msg.Quoted)
	}
}

func TestAPlainMessageQuotesNothing(t *testing.T) {
	msg, _ := decodeEnvelope(t, dataEnvelope("Wordle 1 891 3/6*", testGroupID))
	if msg.Quoted != "" {
		t.Errorf("Quoted = %q on a message that replies to nothing", msg.Quoted)
	}
}
