package i18n

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Integer formats a whole number for display. Swedish groups thousands with
// spaces; English preserves the ungrouped form the application used before
// locale-aware number formatting.
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
	if locale != "sv" {
		return fmt.Sprintf(format, args...)
	}
	localized := make([]any, len(args))
	for i, arg := range args {
		switch arg.(type) {
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			localized[i] = swedishNumber{value: arg}
		default:
			localized[i] = arg
		}
	}
	return fmt.Sprintf(format, localized...)
}

// swedishNumber keeps the original fmt verb, width and precision, then
// localizes the resulting digits. Implementing fmt.Formatter avoids changing
// every %d and %.2f catalogue entry to accept a preformatted string.
type swedishNumber struct{ value any }

func (n swedishNumber) Format(state fmt.State, verb rune) {
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
	_, _ = io.WriteString(state, localizeNumber("sv", fmt.Sprintf(spec.String(), n.value)))
}

func localizeNumber(locale, raw string) string {
	if locale != "sv" {
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
			grouped.WriteByte(' ')
			grouped.WriteString(integer[i : i+3])
		}
		integer = grouped.String()
	}
	if found {
		return sign + integer + "," + fraction
	}
	return sign + integer
}
