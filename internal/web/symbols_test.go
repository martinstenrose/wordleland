package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/martinstenrose/wordleland/internal/stats"
)

// Every glyph a template or the code table asks for has path data.
//
// symbol draws nothing for a name it does not know, rather than a broken box
// in front of a reader, which means a misspelt name is invisible to every
// test that only checks a page renders. This reads the names out of the
// templates themselves, so a glyph added to a page later is held to it too.
func TestEverySymbolExists(t *testing.T) {
	t.Parallel()

	for table, codes := range map[string]map[string]string{
		"symbolFor": symbolFor, "sectionSymbols": sectionSymbols, "calloutSymbols": calloutSymbols,
	} {
		for code, name := range codes {
			if _, ok := symbolPaths[name]; !ok {
				t.Errorf("%s[%q] = %q, which has no path data", table, code, name)
			}
		}
	}
	for _, kind := range []string{
		stats.CalloutUnbroken, stats.CalloutOneAndDone, stats.CalloutOnForm, stats.CalloutOffForm,
		stats.CalloutMissing, stats.CalloutQuickSolves, stats.CalloutCloseShaves, stats.CalloutStumped,
		stats.CalloutHardMode,
	} {
		if _, ok := calloutSymbols[kind]; !ok {
			t.Errorf("callout kind %q has no glyph", kind)
		}
	}

	named := regexp.MustCompile(`\{\{-?\s*symbol\s+"([a-z_]+)"`)
	coded := regexp.MustCompile(`\{\{-?\s*codeSymbol\s+"([a-z_]+)"`)
	seen := 0
	err := fs.WalkDir(templateFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		body, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		for _, m := range named.FindAllStringSubmatch(string(body), -1) {
			seen++
			if _, ok := symbolPaths[m[1]]; !ok {
				t.Errorf("%s draws symbol %q, which has no path data", path, m[1])
			}
		}
		for _, m := range coded.FindAllStringSubmatch(string(body), -1) {
			seen++
			if _, ok := symbolFor[m[1]]; !ok {
				t.Errorf("%s draws codeSymbol %q, which symbolFor does not map", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("no template draws a symbol by a literal name; the pattern is stale")
	}
}

// A glyph with a filled form carries both, so the stylesheet can fill the
// one that is current without the template saying which it is drawing.
func TestASymbolWithAFilledFormCarriesBoth(t *testing.T) {
	t.Parallel()

	got := string(symbol("today", 19))
	if !strings.Contains(got, `class="sym-line"`) || !strings.Contains(got, `class="sym-fill"`) {
		t.Errorf("symbol(today) = %s, want both the outline and the filled path", got)
	}
	if got := string(symbol("search", 19)); strings.Contains(got, "sym-fill") {
		t.Errorf("symbol(search) = %s, want the outline alone", got)
	}
	if got := symbol("no_such_glyph", 19); got != "" {
		t.Errorf("an unknown glyph drew %q, want nothing", got)
	}
}
