package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/store"
)

// defaultLocale is what an unrecognised or missing preference falls back to.
//
// English ships now; Swedish is a translation of these keys rather than a
// rewrite of every template, which is the whole reason the catalogue exists
// before there is a second language to put in it.
const defaultLocale = i18n.Default

// localeCookie remembers a choice made with ?lang=.
const localeCookie = "wordleland_lang"

// catalogue and catalogues are aliases onto internal/i18n's types, which is
// what also loads them: the Signal bridge announces a month's winner in the
// same words the board renders, and a second copy of the JSON is a second
// thing that could drift from it. See internal/i18n.
type catalogue = i18n.Catalogue
type catalogues = i18n.Catalogues

func loadCatalogues() (catalogues, error) {
	return i18n.Load()
}

// translator resolves keys for one request's locale.
type translator struct {
	locale   string
	strings  catalogue
	fallback catalogue
}

// T looks up a key, formatting it with args when there are any.
//
// A missing key renders as the key itself rather than as nothing: a visible
// "board.rank" in the page says exactly what is wrong, where an empty cell
// looks like a data problem and gets debugged as one.
// TN picks a singular or plural form, by the convention key+".one" and
// key+".other", passing n as the sole argument. English and Swedish both
// split at exactly one, so this stays a suffix lookup rather than a plural
// rule engine; a language that needs more would need a real one.
func (t translator) TN(key string, n int) string {
	form := ".other"
	if n == 1 {
		form = ".one"
	}
	return t.T(key+form, n)
}

func (t translator) T(key string, args ...any) string {
	format, ok := t.strings[key]
	if !ok {
		if format, ok = t.fallback[key]; !ok {
			return key
		}
	}
	if len(args) == 0 {
		return format
	}
	// A singular form usually spells the number out — "1 result in" — so it
	// has nowhere to put one. Sprintf would append "%!(EXTRA int=1)" to it,
	// which is what shipped. No verb, no formatting.
	if !strings.Contains(format, "%") {
		return format
	}
	// A template cannot spread a slice into a variadic call, so a caller
	// holding pre-built arguments passes the slice itself. Flatten it here
	// rather than making every call site unpack by hand — otherwise the
	// slice is formatted as one value and the page shows "[alma 37]".
	if len(args) == 1 {
		if list, ok := args[0].([]any); ok {
			args = list
		}
	}
	return i18n.Sprintf(t.locale, format, args...)
}

func (t translator) Integer(value int) string {
	return i18n.Integer(t.locale, value)
}

func (t translator) Decimal(value float64, places int) string {
	return i18n.Decimal(t.locale, value, places)
}

// translatorFor picks a locale: an explicit ?lang= wins, then the cookie it
// sets, then the browser's Accept-Language, then English.
func (s *Server) translatorFor(w http.ResponseWriter, r *http.Request) translator {
	locale := ""

	if requested := r.URL.Query().Get("lang"); requested != "" {
		if _, ok := s.catalogues[requested]; ok {
			locale = requested
			// w is nil when the caller is composing an email rather than a
			// response: the reader's choice still picks the language, but
			// there is nowhere to remember it.
			if w != nil {
				http.SetCookie(w, &http.Cookie{
					Name:     localeCookie,
					Value:    requested,
					Path:     "/",
					HttpOnly: true,
					Secure:   s.secureCookies,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   365 * 24 * 60 * 60,
				})
			}
		}
	}

	// A signed-in reader's choice belongs to the account, not the browser:
	// it is what their mail is written in, and it should follow them to a
	// second device. Written only on an explicit change, which is rare.
	if user, ok := authenticated(r); ok {
		if locale != "" && locale != user.Locale {
			if err := store.SetUserLocale(r.Context(), s.db, user.ID, locale); err != nil {
				// Not fatal: the page still renders in the language they
				// asked for, and the cookie carries it meanwhile.
				s.logger.Error("store language preference", "error", err)
			}
		}
		if locale == "" && s.KnownLocale(user.Locale) {
			locale = user.Locale
		}
	}

	if locale == "" {
		if c, err := r.Cookie(localeCookie); err == nil {
			if _, ok := s.catalogues[c.Value]; ok {
				locale = c.Value
			}
		}
	}

	if locale == "" {
		locale = s.acceptedLocale(r.Header.Get("Accept-Language"))
	}

	return translator{
		locale:   locale,
		strings:  s.catalogues[locale],
		fallback: s.catalogues[defaultLocale],
	}
}

// acceptedLocale takes the first Accept-Language entry we have strings for.
// Quality values are ignored: browsers list preferences in order anyway, and
// parsing them properly buys nothing for two languages.
func (s *Server) acceptedLocale(header string) string {
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(part)
		if i := strings.Index(tag, ";"); i >= 0 {
			tag = tag[:i]
		}
		if i := strings.Index(tag, "-"); i >= 0 {
			tag = tag[:i]
		}
		tag = strings.ToLower(strings.TrimSpace(tag))
		if _, ok := s.catalogues[tag]; ok && tag != "" {
			return tag
		}
	}
	return defaultLocale
}

// localeOrder lists the loaded locales with the default first.
func localeOrder(c catalogues) []string {
	rest := make([]string, 0, len(c))
	for code := range c {
		if code != defaultLocale {
			rest = append(rest, code)
		}
	}
	sort.Strings(rest)
	return append([]string{defaultLocale}, rest...)
}

// translatorIn builds a translator for a named locale, with no request in
// front of it. Mail is composed for a recipient rather than for whoever
// triggered it: a reset requested from a Swedish browser for an English
// reader must arrive in English.
//
// An unknown locale falls back rather than failing. The value comes from
// the database, and a message that does not send is worse than one in the
// wrong language.
func (s *Server) translatorIn(locale string) translator {
	catalogue, ok := s.catalogues[locale]
	if !ok {
		locale, catalogue = defaultLocale, s.catalogues[defaultLocale]
	}
	return translator{
		locale:   locale,
		strings:  catalogue,
		fallback: s.catalogues[defaultLocale],
	}
}

// KnownLocale reports whether there are strings for a locale, so a value
// off a form is checked before it is stored.
func (s *Server) KnownLocale(locale string) bool {
	_, ok := s.catalogues[locale]
	return ok
}
