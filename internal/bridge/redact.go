package bridge

import (
	"strings"
	"unicode"
)

// maxRemoteLen bounds a relayed string. signal-cli decides how long its own
// error is, and a log line is not the place to find out.
const maxRemoteLen = 200

// redactedNumber replaces anything phone-number-shaped.
const redactedNumber = "[number]"

// minRedactDigits is how many digits make a span look like a phone number.
// Seven is the shortest national subscriber number worth worrying about; a
// puzzle number is four and survives, a millisecond timestamp is thirteen
// and does not. Redacting a timestamp costs nothing worth keeping.
const minRedactDigits = 7

// sanitizeRemote makes a string that came from signal-cli safe to log.
//
// Two separate problems, one function, because both arrive on the same
// values and fixing one alone leaves the other:
//
//   - Control characters, newlines above all: a value that reaches the log
//     verbatim can close the line and open a convincing fake one. Whoever
//     reads the log next cannot tell the forged entry from a real one.
//   - Phone numbers. signal-cli names accounts and recipients in its error
//     text, and a number written to disk is exactly what this codebase
//     refuses to do everywhere else — it is why a raw envelope is never
//     dumped either.
//
// It is deliberately blunt. This is diagnostic text, not data: losing a
// digit span that happened to be harmless costs nothing, and guessing
// correctly which ones are numbers is not a thing that can be got right.
func sanitizeRemote(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	// Spans of digits and the punctuation people write numbers with, held
	// until it is known whether the span has enough digits to be one.
	var span strings.Builder
	digits := 0

	flush := func() {
		raw := span.String()
		if digits >= minRedactDigits {
			// Separators the span swallowed after the last digit belong to
			// the sentence around the number, not to the number, and
			// eating them runs the next word into the placeholder.
			upToLastDigit := strings.TrimRightFunc(raw, func(r rune) bool {
				return !unicode.IsDigit(r)
			})
			b.WriteString(redactedNumber)
			b.WriteString(raw[len(upToLastDigit):])
		} else {
			b.WriteString(raw)
		}
		span.Reset()
		digits = 0
	}

	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			span.WriteRune(r)
			digits++
		case r == '+':
			// May start a span: the country prefix comes before any digit.
			// A lone plus with no digits after it is written back as it was.
			span.WriteRune(r)
		case span.Len() > 0 && strings.ContainsRune("-()., ", r):
			// Kept inside the span: "+46 70 123 45 67" is one number, and
			// splitting on the spaces would hide it behind the threshold.
			span.WriteRune(r)
		default:
			flush()
			if unicode.IsControl(r) {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	flush()

	out := strings.TrimSpace(b.String())
	if len(out) > maxRemoteLen {
		// ToValidUTF8 drops the partial rune a byte cut can leave behind,
		// so the line stays valid however the string was encoded.
		out = strings.ToValidUTF8(out[:maxRemoteLen], "") + "…"
	}
	return out
}
