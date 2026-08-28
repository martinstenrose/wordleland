// Package i18n loads the string catalogues shared by the web frontend and
// the Signal bridge.
//
// It exists so a sentence translated once cannot drift between the two
// surfaces that say it: the board renders "months.line.*" in a template,
// and the Signal bridge posts the same keys back into the group when a
// month closes. Splitting the catalogue out of internal/web is what lets
// the bridge read it without importing the web package, which it cannot —
// web already imports bridge, to hold the running Supervisor for the
// diagnostics page.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

// localeFS holds one JSON file per locale.
//
//go:embed locales
var localeFS embed.FS

// Default is what an unrecognised or missing locale falls back to.
const Default = "en"

// Catalogue is one locale's strings.
type Catalogue map[string]string

// Catalogues holds every locale, loaded once at startup so a malformed file
// is a boot failure rather than a blank page, or a silent English fallback,
// later.
type Catalogues map[string]Catalogue

// Load reads every locale file embedded in the binary.
func Load() (Catalogues, error) {
	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		return nil, fmt.Errorf("read locales: %w", err)
	}

	out := make(Catalogues, len(entries))
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if name == e.Name() {
			continue
		}
		data, err := localeFS.ReadFile("locales/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var c Catalogue
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		out[name] = c
	}

	if _, ok := out[Default]; !ok {
		return nil, fmt.Errorf("no %s.json in locales", Default)
	}
	return out, nil
}

// Translator resolves keys for one locale, formatting them with args when
// there are any.
//
// It is the same lookup-then-fallback-then-key rule internal/web's
// translator uses, kept as a second small implementation rather than a
// shared one: web's version also carries plural forms and a cookie-backed
// locale choice, which a once-a-month chat message has no use for, and
// mirroring five lines here is cheaper than a type both packages have to
// agree on.
type Translator struct {
	Locale   string
	strings  Catalogue
	fallback Catalogue
}

// NewTranslator builds a Translator for locale, falling back to Default
// when it is not one of the loaded catalogues.
func NewTranslator(cats Catalogues, locale string) Translator {
	cat, ok := cats[locale]
	if !ok {
		locale, cat = Default, cats[Default]
	}
	return Translator{Locale: locale, strings: cat, fallback: cats[Default]}
}

// T looks up a key and formats it with args. A missing key renders as the
// key itself, so a typo is visible rather than a blank message.
func (t Translator) T(key string, args ...any) string {
	format, ok := t.strings[key]
	if !ok {
		if format, ok = t.fallback[key]; !ok {
			return key
		}
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
