// Package i18n loads the string catalogues shared by the web frontend and
// the Signal bridge.
//
// It exists so that what both surfaces render the same way — a month
// name, a tied name list's conjunction, a formatted score — cannot drift
// between them by using one shared set of keys. Splitting the catalogue
// out of internal/web is what lets the bridge read it without importing
// the web package, which it cannot — web already imports bridge, to hold
// the running Supervisor for the diagnostics page.
//
// Not every key is shared on purpose: the board's "months.line.*" and the
// Signal bridge's "announce.line.*" both describe a closed month's winner,
// but say it differently on purpose — see internal/announce's winnerLine
// for why — so they are deliberately two key families, not one reused
// across a "for the page" and "for the chat message" branch.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// localeFS holds one JSON file per locale.
//
//go:embed locales
var localeFS embed.FS

// Default is what an unrecognised or missing locale falls back to.
const Default = "en"

// Catalogue is one locale's strings.
type Catalogue map[string]string

// Catalogues holds every locale, loaded at startup so a malformed file
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
	// turns is where Vary keeps its place, shared by every copy of a
	// Translator made with Rotating; nil for one that never rotates.
	turns *turns
}

// turns is the next variant to say for each key.
type turns struct {
	mu   sync.Mutex
	next map[string]int
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
	return Sprintf(t.Locale, format, args...)
}

// TN is T for a count said with its noun: key+".one" for one, else
// key+".other", formatted with n — "1 dag", "2 dagar".
func (t Translator) TN(key string, n int) string {
	if n == 1 {
		return t.T(key+".one", n)
	}
	return t.T(key+".other", n)
}

// Rotating is the translator with a memory for Vary, so a line said with
// it comes out in its next wording each time. Copies share the memory.
func (t Translator) Rotating() Translator {
	t.turns = &turns{next: map[string]int{}}
	return t
}

// Vary is T for a line written more than one way: key, then "key.2",
// "key.3" and on for as long as the locale has them, each said in turn so
// the same words do not come twice running. Without Rotating it is always
// key itself, which is what a test pins.
func (t Translator) Vary(key string, args ...any) string {
	if t.turns == nil {
		return t.T(key, args...)
	}
	count := 1
	for {
		if _, ok := t.strings[key+"."+strconv.Itoa(count+1)]; !ok {
			break
		}
		count++
	}
	t.turns.mu.Lock()
	n := t.turns.next[key] % count
	t.turns.next[key] = n + 1
	t.turns.mu.Unlock()
	if n == 0 {
		return t.T(key, args...)
	}
	return t.T(key+"."+strconv.Itoa(n+1), args...)
}

func (t Translator) Integer(value int) string {
	return Integer(t.Locale, value)
}

func (t Translator) Decimal(value float64, places int) string {
	return Decimal(t.Locale, value, places)
}
