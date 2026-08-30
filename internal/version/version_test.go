package version

import "testing"

// The commit is the half that identifies a rolling build: "testing" names a
// moving target, and "testing (2352757)" names one image.
func TestStringIdentifiesTheBuild(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version string
		commit  string
		want    string
	}{
		{
			name:    "a released build",
			version: "0.3.0", commit: "2352757abcdef0123456789",
			want: "0.3.0 (2352757)",
		},
		{
			// The case the whole package exists for. Without the hash this
			// says only that somebody merged something, at some point.
			name:    "the rolling tag",
			version: "testing", commit: "84eff81fedcba9876543210",
			want: "testing (84eff81)",
		},
		{
			name:    "an unstamped local build",
			version: "dev", commit: "",
			want: "dev",
		},
		{
			// Nothing enforces a full hash on the way in, so a short one
			// must not be truncated into nonsense or panic.
			name:    "a commit shorter than the abbreviation",
			version: "dev", commit: "abc",
			want: "dev (abc)",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stamp(t, tt.version, tt.commit)

			if got := String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Set separates "built locally" from "published image whose stamp went
// missing", which are worth showing differently.
func TestSetReportsWhetherTheBuildWasStamped(t *testing.T) {
	for _, tt := range []struct {
		commit string
		want   bool
	}{
		{"2352757", true},
		{"", false},
		// Build args arrive as strings and an empty one can come through as
		// whitespace rather than nothing.
		{"   ", false},
	} {
		stamp(t, "dev", tt.commit)

		if got := Set(); got != tt.want {
			t.Errorf("Set() with Commit=%q = %v, want %v", tt.commit, got, tt.want)
		}
	}
}

// stamp sets the package vars for one test and puts them back afterwards,
// so a table entry cannot leak into the next one. These are package-level
// by necessity — ldflags can only write to package variables — which makes
// restoring them the test's job.
func stamp(t *testing.T, v, c string) {
	t.Helper()
	prevV, prevC := Version, Commit
	t.Cleanup(func() { Version, Commit = prevV, prevC })
	Version, Commit = v, c
}
