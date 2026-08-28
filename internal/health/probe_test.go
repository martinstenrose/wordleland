package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The probe is what the container HEALTHCHECK runs, so "healthy" has to mean
// 200 and nothing else. A probe that accepted any answer would report a
// half-dead container as fine, which is worse than having no healthcheck.
func TestProbeAcceptsOnlyOK(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"healthy", http.StatusOK, false},
		{"unhealthy", http.StatusServiceUnavailable, true},
		{"not found", http.StatusNotFound, true},
		// A 500 body can still be served; the status is what decides.
		{"server error", http.StatusInternalServerError, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			err := Probe(srv.URL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Probe() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), srv.URL) {
				t.Errorf("error %q does not name the URL probed", err)
			}
		})
	}
}

// A container that is not listening yet is not healthy, and the error has to
// say so rather than panicking on a nil response.
func TestProbeReportsAConnectionFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	err := Probe(url)
	if err == nil {
		t.Fatal("probing a closed listener reported healthy")
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error %q does not name the URL probed", err)
	}
}
