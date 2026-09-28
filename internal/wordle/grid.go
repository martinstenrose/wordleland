package wordle

import "strings"

// Grid is the coloured squares of a shared result: one row per guess, top to
// bottom, each row five letters — g for a letter in the right place, y for
// one in the word but elsewhere, n for one not in it — and the rows joined by
// "/". "nynnn/ngggy/ggggg" is a 3.
//
// The letters, not the emoji, are what is stored: the same result shares as
// ⬛ or ⬜ depending on the sharer's theme, and as 🟧 and 🟦 in high-contrast
// mode, and none of that is about the game.
type Grid string

// Rows splits a grid into its rows. An empty grid has none.
func (g Grid) Rows() []string {
	if g == "" {
		return nil
	}
	return strings.Split(string(g), "/")
}

// squares maps each square Wordle shares to its letter. High-contrast mode
// draws a letter in place orange and one elsewhere blue.
var squares = map[rune]byte{
	'🟩': 'g', '🟧': 'g',
	'🟨': 'y', '🟦': 'y',
	'⬛': 'n', '⬜': 'n',
}

// gridRow reads one line of squares, or reports that the line is not one.
// Spaces and the emoji variation selector some keyboards add between
// squares are ignored. A row is five squares: anything before them makes
// the line not a row, and so does a sixth square, but what follows the
// fifth — "🟩🟩🟩🟩🟩 puh 🥲" — is a comment typed on the row's own line,
// and the row stands. The header still decides the score, and the rows
// still have to agree with it (see readGrid), so a comment cannot make a
// grid out of something that is not one.
func gridRow(line string) (string, bool) {
	var row []byte
	for _, r := range line {
		switch r {
		case ' ', '\t', ' ', '️', '‍':
			continue
		}
		c, ok := squares[r]
		switch {
		case !ok && len(row) == 5:
			return string(row), true
		case !ok:
			return "", false
		case len(row) == 5:
			return "", false
		}
		row = append(row, c)
	}
	if len(row) != 5 {
		return "", false
	}
	return string(row), true
}

// readGrid reads the grid that follows a header: the first run of rows after
// it, blank lines before the run allowed. It returns an empty grid unless the
// rows agree with the score — one row per guess, the last one all green on a
// solve and none of them all green on a miss — because a grid that
// contradicts the header it came with is not one to draw next to that score.
func readGrid(lines []string, solved bool, guesses int) Grid {
	var rows []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(rows) > 0 {
				break
			}
			continue
		}
		row, ok := gridRow(trimmed)
		if !ok {
			break
		}
		rows = append(rows, row)
	}
	g := Grid(strings.Join(rows, "/"))
	if !g.Agrees(solved, guesses) {
		return ""
	}
	return g
}

// Agrees reports whether a grid is a well-formed record of a score: rows of
// five squares, one per guess (six on a miss), the last all green on a
// solve and no earlier row all green.
func (g Grid) Agrees(solved bool, guesses int) bool {
	rows := g.Rows()
	want := guesses
	if !solved {
		want = MaxGuesses
	}
	if want < 1 || len(rows) != want {
		return false
	}
	for i, row := range rows {
		if len(row) != 5 || strings.Trim(row, "gyn") != "" {
			return false
		}
		won := row == "ggggg"
		if won != (solved && i == len(rows)-1) {
			return false
		}
	}
	return true
}
