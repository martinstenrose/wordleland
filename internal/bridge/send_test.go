package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSenderPostsToV2Send(t *testing.T) {
	var gotPath string
	var gotBody struct {
		Message    string   `json:"message"`
		Number     string   `json:"number"`
		Recipients []string `json:"recipients"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	// Synthetic base64 for a 32-byte group identifier.
	groupID := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	send, err := NewSender(srv.URL, "+00000000000", groupID)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := send(context.Background(), "Alice took August over 28 games."); err != nil {
		t.Fatalf("send: %v", err)
	}

	if gotPath != "/v2/send" {
		t.Errorf("path = %q, want /v2/send", gotPath)
	}
	if gotBody.Message != "Alice took August over 28 games." {
		t.Errorf("message = %q", gotBody.Message)
	}
	if gotBody.Number != "+00000000000" {
		t.Errorf("number = %q", gotBody.Number)
	}
	// signal-cli-rest-api 0.100 expects the bare internal id wrapped in an
	// additional base64 layer. Prefixing the configured id directly produces
	// the Invalid identifier response this test guards against.
	want := "group.TURFeU16UTFOamM0T1dGaVkyUmxaakF4TWpNME5UWTNPRGxoWW1Oa1pXWT0="
	if len(gotBody.Recipients) != 1 || gotBody.Recipients[0] != want {
		t.Errorf("recipients = %v, want [%s]", gotBody.Recipients, want)
	}
	if len(gotBody.Recipients) == 1 && gotBody.Recipients[0] == "group."+groupID {
		t.Errorf("recipient used the unwrapped internal id: %q", gotBody.Recipients[0])
	}
}

func TestNewSenderReportsAnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid recipient"}`))
	}))
	defer srv.Close()

	send, err := NewSender(srv.URL, "+00000000000", "c2FtcGxlLWdyb3VwLWlk")
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	err = send(context.Background(), "hello")
	if err == nil {
		t.Fatal("send() succeeded on a 400 response, want an error")
	}
	if !strings.Contains(err.Error(), "invalid recipient") {
		t.Errorf("error = %v, want it to carry the server's message", err)
	}
}

// A phone number in signal-cli's own error text must not reach the error
// this bubbles up to a logger, for the same reason a raw envelope is never
// logged: see redact.go.
func TestNewSenderRedactsThePhoneNumberInAnErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to send to +00000000000"}`))
	}))
	defer srv.Close()

	send, err := NewSender(srv.URL, "+00000000000", "c2FtcGxlLWdyb3VwLWlk")
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	err = send(context.Background(), "hello")
	if err == nil {
		t.Fatal("send() succeeded on a 500 response, want an error")
	}
	if strings.Contains(err.Error(), "00000000000") {
		t.Errorf("error = %v, leaked the phone number", err)
	}
}

func TestNewSenderRejectsABadURL(t *testing.T) {
	if _, err := NewSender("not a url", "+1", "g"); err == nil {
		t.Fatal("NewSender() accepted a malformed URL")
	}
	if _, err := NewSender("ftp://example.tld", "+1", "g"); err == nil {
		t.Fatal("NewSender() accepted a non-HTTP scheme")
	}
}
