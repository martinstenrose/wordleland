package web

import "testing"

// The Secure attribute fails closed. APP_URL is optional, so reading an
// unset value as "not https" would quietly drop Secure from the session
// cookie on a deployment that is in fact behind TLS.
func TestSecureCookiesFailClosed(t *testing.T) {
	for _, tt := range []struct {
		appURL string
		want   bool
	}{
		{"https://wordle.example.tld", true},
		{"", true},
		{"http://localhost:8080", false},
	} {
		if got := secureCookies(tt.appURL); got != tt.want {
			t.Errorf("secureCookies(%q) = %v, want %v", tt.appURL, got, tt.want)
		}
	}
}
