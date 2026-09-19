package i18n

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// numberFormat is how one locale writes a number: what separates the
// fractional part, and what — if anything — groups the thousands.
//
// English is the odd one here with no grouping at all. That is deliberate
// and predates the other locales: the application wrote bare digits before
// any of this existed, and the puzzle numbers this mostly formats read
// better as "1918" than as "1,918".
type numberFormat struct {
	decimal string
	group   string
}

// Locales that write a number the way English does need no entry. Swedish
// groups with a space; German, Spanish and Italian group with a full stop.
// All four put a comma before the fraction.
var numberFormats = map[string]numberFormat{
	"sv": {decimal: ",", group: " "},
	"de": {decimal: ",", group: "."},
	"es": {decimal: ",", group: "."},
	"it": {decimal: ",", group: "."},
}

// Integer formats a whole number for display.
func Integer(locale string, value int) string {
	return localizeNumber(locale, strconv.Itoa(value))
}

// Decimal formats a fixed-precision decimal for display.
func Decimal(locale string, value float64, places int) string {
	raw := strconv.FormatFloat(value, 'f', places, 64)
	return localizeNumber(locale, raw)
}

// Sprintf applies locale-aware formatting to numeric arguments while
// preserving the catalogue's existing fmt verbs. That covers counts inside
// translated sentences as well as numbers preformatted by page builders.
func Sprintf(locale, format string, args ...any) string {
	if _, ok := numberFormats[locale]; !ok {
		return fmt.Sprintf(format, args...)
	}
	localized := make([]any, len(args))
	for i, arg := range args {
		switch arg.(type) {
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			localized[i] = localNumber{locale: locale, value: arg}
		default:
			localized[i] = arg
		}
	}
	return fmt.Sprintf(format, localized...)
}

// localNumber keeps the original fmt verb, width and precision, then
// localizes the resulting digits. Implementing fmt.Formatter avoids changing
// every %d and %.2f catalogue entry to accept a preformatted string.
type localNumber struct {
	locale string
	value  any
}

func (n localNumber) Format(state fmt.State, verb rune) {
	var spec strings.Builder
	spec.WriteByte('%')
	for _, flag := range "#0+- " {
		if state.Flag(int(flag)) {
			spec.WriteRune(flag)
		}
	}
	if width, ok := state.Width(); ok {
		spec.WriteString(strconv.Itoa(width))
	}
	if precision, ok := state.Precision(); ok {
		spec.WriteByte('.')
		spec.WriteString(strconv.Itoa(precision))
	}
	spec.WriteRune(verb)
	_, _ = io.WriteString(state, localizeNumber(n.locale, fmt.Sprintf(spec.String(), n.value)))
}

func localizeNumber(locale, raw string) string {
	f, ok := numberFormats[locale]
	if !ok {
		return raw
	}

	sign := ""
	if strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, "+") {
		sign, raw = raw[:1], raw[1:]
	}

	integer, fraction, found := strings.Cut(raw, ".")
	if len(integer) > 3 {
		first := len(integer) % 3
		if first == 0 {
			first = 3
		}
		var grouped strings.Builder
		grouped.WriteString(integer[:first])
		for i := first; i < len(integer); i += 3 {
			grouped.WriteString(f.group)
			grouped.WriteString(integer[i : i+3])
		}
		integer = grouped.String()
	}
	if found {
		return sign + integer + f.decimal + fraction
	}
	return sign + integer
}
