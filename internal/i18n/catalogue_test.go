package i18n

import (
	"regexp"
	"sort"
	"testing"
)

// A key that exists in English and nowhere else falls back silently, so the
// page renders — in English, in the middle of a Swedish sentence. Nothing
// fails, nothing is logged, and it is only ever noticed by someone reading
// that page in that language. This is where it is noticed instead.
func TestEveryLocaleCarriesEveryKey(t *testing.T) {
	cats, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	english, ok := cats[Default]
	if !ok {
		t.Fatalf("no %q catalogue", Default)
	}
	if len(cats) < 2 {
		t.Fatalf("only %d catalogue(s) loaded; this test needs the others to compare against", len(cats))
	}

	for locale, cat := range cats {
		if locale == Default {
			continue
		}
		var missing, extra []string
		for key := range english {
			if _, ok := cat[key]; !ok {
				missing = append(missing, key)
			}
		}
		for key := range cat {
			if _, ok := english[key]; !ok {
				extra = append(extra, key)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s is missing %d key(s): %v", locale, len(missing), missing)
		}
		// The other direction matters too: a key only a translation has is
		// one nothing renders, usually a rename that was applied on one side.
		if len(extra) > 0 {
			t.Errorf("%s has %d key(s) English does not: %v", locale, len(extra), extra)
		}
	}
}

// fmt verbs are positional, so a translation with a different set of them
// than English either drops an argument or prints %!d(MISSING) into the page.
// The counts have to match, verb for verb.
func TestEveryTranslationTakesTheSameArguments(t *testing.T) {
	cats, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	verbs := regexp.MustCompile(`%[-#+ 0-9.]*[a-zA-Z]`)

	english := cats[Default]
	for locale, cat := range cats {
		if locale == Default {
			continue
		}
		for key, want := range english {
			got, ok := cat[key]
			if !ok {
				continue // reported by the test above
			}
			a, b := verbs.FindAllString(want, -1), verbs.FindAllString(got, -1)
			sort.Strings(a)
			sort.Strings(b)
			if len(a) != len(b) {
				t.Errorf("%s[%q] takes %v, English takes %v", locale, key, b, a)
				continue
			}
			for i := range a {
				if a[i] != b[i] {
					t.Errorf("%s[%q] takes %v, English takes %v", locale, key, b, a)
					break
				}
			}
		}
	}
}

// Each locale names itself in its own language, because that is how a reader
// who cannot read the current one finds theirs in the menu.
func TestEveryLocaleNamesItself(t *testing.T) {
	cats, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for locale, cat := range cats {
		if cat["locale.name"] == "" {
			t.Errorf("%s does not name itself", locale)
		}
	}
	if got := cats["de"]["locale.name"]; got != "Deutsch" {
		t.Errorf("de names itself %q", got)
	}
}

// A puzzle number names a puzzle; it does not count anything. Grouping its
// digits invites the eye to read a magnitude out of a name — "#1.918" in
// German, "#1 918" in Swedish — and nobody writes a house number or a flight
// number that way either. English arrived here by having no grouping to
// apply; the rest get it on purpose.
func TestAnIdentifierIsNeverGrouped(t *testing.T) {
	for _, locale := range []string{"en", "sv", "de", "es", "it"} {
		if got := Identifier(1918); got != "1918" {
			t.Errorf("Identifier(1918) = %q under %s, want the bare digits", got, locale)
		}
	}
	// A quantity still groups, so this is a distinction rather than a
	// blanket retreat from locale-aware numbers.
	if got := Integer("de", 1918); got != "1.918" {
		t.Errorf("Integer(\"de\", 1918) = %q, want a grouped quantity", got)
	}
}
