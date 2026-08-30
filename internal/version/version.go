// Package version reports which build of Wordleland is running.
//
// It exists because the answer was unavailable from inside the app, and
// that cost real time: an image was deployed, the page was read, and the
// container turned out to predate the change being looked for. Nothing on
// the running instance could have said so.
//
// Go stamps VCS information into a binary automatically, but not here:
// .dockerignore excludes .git so the build context has no repository to
// read, which is deliberate — the build context should not carry it. So
// the values arrive as ldflags instead, set from the image build.
package version

import "strings"

// Version and Commit are set at build time with -ldflags -X. The defaults
// are what a plain `go build` or a test binary reports, and "dev" is
// meant to be recognisable as "not from CI" rather than mistaken for a
// release.
var (
	Version = "dev"
	Commit  = ""
)

// shortCommit is how much of a hash a person actually reads. Seven is
// what git itself abbreviates to and what the GitHub UI shows, so a value
// here can be pasted into either.
const shortCommit = 7

// String identifies the build in one line.
//
// The commit is the load-bearing half. A released build says "0.3.0",
// which is meaningful on its own, but the rolling tag says "testing" —
// which names a moving target and identifies nothing without the hash
// beside it.
func String() string {
	if Commit == "" {
		return Version
	}
	commit := Commit
	if len(commit) > shortCommit {
		commit = commit[:shortCommit]
	}
	return Version + " (" + commit + ")"
}

// Set reports whether this build was stamped at all. An unstamped binary
// is not a fault — it is what a local build is — but it is worth showing
// differently from one that can name its commit.
func Set() bool { return strings.TrimSpace(Commit) != "" }
