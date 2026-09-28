package web

import (
	"strings"
	"testing"
)

// A template cannot spread a slice into a variadic call, so callers pass
// the slice. Without flattening, the page renders "[alma 37]".
func TestTranslatorFlattensPrebuiltArguments(t *testing.T) {
	t.Parallel()

	tr := translator{
		locale:   "en",
		strings:  catalogue{"greet": "%s has %d"},
		fallback: catalogue{},
	}

	if got := tr.T("greet", []any{"alma", 37}); got != "alma has 37" {
		t.Errorf("T() with a slice = %q, want the arguments spread", got)
	}
	if got := tr.T("greet", "alma", 37); got != "alma has 37" {
		t.Errorf("T() with loose args = %q", got)
	}
	// A genuine single argument that happens to be a slice of something
	// else is untouched.
	if got := tr.T("greet", []string{"a"}, 1); got == "" {
		t.Error("T() returned nothing")
	}
}

// A singular form spells the number out and has nowhere to put one.
// Formatting it anyway appended "%!(EXTRA int=1)" to the page.
func TestTranslatorLeavesVerblessStringsAlone(t *testing.T) {
	t.Parallel()

	tr := translator{
		locale: "en",
		strings: catalogue{
			"n.one":   "1 result in",
			"n.other": "%d results in",
		},
		fallback: catalogue{},
	}

	if got := tr.TN("n", 1); got != "1 result in" {
		t.Errorf("TN(1) = %q, want the singular untouched", got)
	}
	if got := tr.TN("n", 4); got != "4 results in" {
		t.Errorf("TN(4) = %q", got)
	}
}

// Every plural form in the shipped catalogues has to survive both paths.
func TestNoCatalogueStringFormatsBadly(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	for locale, cat := range srv.catalogues {
		tr := translator{locale: locale, strings: cat, fallback: srv.catalogues["en"]}
		for key := range cat {
			base, form, found := strings.Cut(key, ".one")
			if !found || form != "" {
				continue
			}
			for _, n := range []int{1, 2, 7} {
				got := tr.TN(base, n)
				if args, ok := pluralArgs[base]; ok {
					got = tr.TP(base, n, args(n)...)
				}
				if strings.Contains(got, "%!") {
					t.Errorf("%s: plural %q at %d = %q", locale, base, n, got)
				}
			}
		}
	}
}

// pluralArgs are the plural keys whose form is picked by one number and that
// are formatted with more than it — through TP, as their callers do. Each
// gives the arguments its caller passes, for a count of n.
var pluralArgs = map[string]func(n int) []any{
	"months.seasonLeader":     func(n int) []any { return []any{"Alma", n} },
	"months.seasonShared":     func(n int) []any { return []any{"Alma and Bo", n} },
	"today.standing.daysLeft": func(n int) []any { return []any{"September", n} },
	"months.award.climbed":    func(n int) []any { return []any{n, "Aug"} },
	"board.h2h.sub":           func(n int) []any { return []any{n, 2, "Alma", "0.12"} },
	"board.h2h.subRecent":     func(n int) []any { return []any{n, 2, "Alma", "0.12"} },
}
