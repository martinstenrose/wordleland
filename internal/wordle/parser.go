package wordle

import (
	"regexp"
	"strconv"
	"strings"
)

// Result is one parsed share-text result.
type Result struct {
	PuzzleNo int
	Solved   bool
	// Guesses is 1-6 when solved, and 0 when not. Callers storing this map a
	// failure to NULL: The "7" convention stays out of storage entirely.
	Guesses  int
	HardMode bool
	// Grid is the share's coloured squares, when the message carried a grid
	// that agrees with the score, and empty otherwise. See Grid.
	Grid Grid
}

// headerPattern matches the one line that carries the score.
//
// The header alone decides the score. The grid under it is read afterwards,
// and only kept when it agrees with that score (see readGrid), so a message
// whose squares were cropped, edited or mangled on the way still files its
// result — just without a grid.
//
//   - The space after "Wordle" is always a plain U+0020, confirmed by byte
//     inspection of the real export.
//   - The puzzle number may carry a comma or an NBSP as a thousands separator.
//     Those are the only two forms seen in the corpus, though normalisation
//     handles the narrow and thin spaces too as cheap insurance.
//   - A trailing asterisk, with no space before it, means hard mode.
//
// MaxGuesses is the denominator in a Wordle scoreline. Named so the places
// that render "4/6" agree with the pattern that parses it.
const MaxGuesses = 6

var headerPattern = regexp.MustCompile(`(?i)\bWordle (\d[\d,\x{00a0} ]*\d|\d) ([1-6X])/6(\*?)`)

// separatorReplacer normalises the space characters that can appear inside a
// puzzle number. NBSP is the one that must work — the corpus has 664 of them
// against 681 commas — and the narrow and thin spaces are covered because the
// spec asks for them, not because either has been seen.
var separatorReplacer = strings.NewReplacer(
	" ", " ", // no-break space
	" ", " ", // narrow no-break space
	" ", " ", // thin space
)

// Parse extracts the first result from a message.
//
// It returns ok=false for a message with no result, which is the common case:
// most traffic in the group is ordinary conversation.
func Parse(text string) (Result, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Quoted and forwarded lines are skipped entirely, so quoting
		// someone else's result does not attribute it to the quoter.
		if strings.HasPrefix(trimmed, ">") {
			continue
		}

		match := headerPattern.FindStringSubmatch(separatorReplacer.Replace(trimmed))
		if match == nil {
			continue
		}

		puzzle, err := parsePuzzleNumber(match[1])
		if err != nil {
			continue
		}

		result := Result{PuzzleNo: puzzle, HardMode: match[3] == "*"}
		if token := strings.ToUpper(match[2]); token == "X" {
			result.Solved = false
		} else {
			guesses, err := strconv.Atoi(token)
			if err != nil {
				continue
			}
			result.Solved = true
			result.Guesses = guesses
		}
		result.Grid = readGrid(lines[i+1:], result.Solved, result.Guesses)

		// The first result wins. A message quoting one score above a new one
		// has already had the quoted line skipped; two unquoted results in one
		// message are rare enough that taking the first is the predictable
		// choice.
		return result, true
	}
	return Result{}, false
}

// parsePuzzleNumber strips separators from a captured number.
func parsePuzzleNumber(s string) (int, error) {
	return strconv.Atoi(strings.NewReplacer(",", "", " ", "").Replace(s))
}
