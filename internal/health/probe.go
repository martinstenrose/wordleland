// Package health provides the self-probe the container healthcheck runs.
package health

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// Probe requests the given URL and reports whether it answered healthy.
//
// It exists because the runtime image is distroless: there is no shell, no
// curl and no wget for a HEALTHCHECK to run. The binary already knows how
// to speak HTTP, so it probes itself.
func Probe(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// Run performs the probe and exits, which is what a healthcheck wants: a
// process that says yes or no and stops.
func Run(url string) {
	if err := Probe(url); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
