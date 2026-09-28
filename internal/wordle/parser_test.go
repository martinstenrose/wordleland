package wordle

import "testing"

// The real chat export is never committed: it is a log of private group
// messages, and nothing here needs it. These fixtures are hand-written,
// with two exceptions — the examples printed itself, and individual
// lines the owner captured and passed on, which explicitly allows.
func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantOK   bool
		puzzle   int
		solved   bool
		guesses  int
		hardMode bool
	}{
		// Both separator forms occur in real traffic: 664 NBSP against 681
		// commas across the corpus.
		{
			name:   "NBSP separator, captured message",
			input:  "Wordle 1 891 3/6*\n\n⬛🟨⬛⬛⬛\n⬛🟩🟩⬛🟨\n🟩🟩🟩🟩🟩",
			wantOK: true, puzzle: 1891, solved: true, guesses: 3, hardMode: true,
		},
		{
			name:   "comma separator, captured message",
			input:  "Wordle 1,891 3/6\n\n🟨⬛⬛⬛⬛\n🟨🟨🟨⬛⬛\n🟩🟩🟩🟩🟩",
			wantOK: true, puzzle: 1891, solved: true, guesses: 3, hardMode: false,
		},
		{
			name:   "plain space separator",
			input:  "Wordle 1 890 4/6",
			wantOK: true, puzzle: 1890, solved: true, guesses: 4,
		},
		{
			name:   "no separator",
			input:  "Wordle 1890 4/6",
			wantOK: true, puzzle: 1890, solved: true, guesses: 4,
		},
		{
			name:   "narrow no-break space",
			input:  "Wordle 1 890 4/6",
			wantOK: true, puzzle: 1890, solved: true, guesses: 4,
		},
		{
			name:   "thin space",
			input:  "Wordle 1 890 4/6",
			wantOK: true, puzzle: 1890, solved: true, guesses: 4,
		},
		{
			name:   "low puzzle number needs no separator",
			input:  "Wordle 42 3/6",
			wantOK: true, puzzle: 42, solved: true, guesses: 3,
		},

		// Every guess count.
		{name: "one guess", input: "Wordle 1890 1/6", wantOK: true, puzzle: 1890, solved: true, guesses: 1},
		{name: "two guesses", input: "Wordle 1890 2/6", wantOK: true, puzzle: 1890, solved: true, guesses: 2},
		{name: "three guesses", input: "Wordle 1890 3/6", wantOK: true, puzzle: 1890, solved: true, guesses: 3},
		{name: "four guesses", input: "Wordle 1890 4/6", wantOK: true, puzzle: 1890, solved: true, guesses: 4},
		{name: "five guesses", input: "Wordle 1890 5/6", wantOK: true, puzzle: 1890, solved: true, guesses: 5},
		{name: "six guesses", input: "Wordle 1890 6/6", wantOK: true, puzzle: 1890, solved: true, guesses: 6},

		// A failure carries no guess count: the "7" convention stays out
		// of storage entirely.
		{name: "failed", input: "Wordle 1890 X/6", wantOK: true, puzzle: 1890, solved: false, guesses: 0},
		{name: "failed lowercase", input: "Wordle 1890 x/6", wantOK: true, puzzle: 1890, solved: false},

		// Hard mode is roughly half of real results, on both outcomes.
		{
			name:   "hard mode solved",
			input:  "Wordle 1890 4/6*",
			wantOK: true, puzzle: 1890, solved: true, guesses: 4, hardMode: true,
		},
		{
			name:   "hard mode failed",
			input:  "Wordle 1890 X/6*",
			wantOK: true, puzzle: 1890, solved: false, hardMode: true,
		},

		// Quoting someone else's score must not attribute it to the quoter.
		{
			name:   "quoted line is skipped",
			input:  "> Wordle 1890 1/6\nWordle 1891 5/6",
			wantOK: true, puzzle: 1891, solved: true, guesses: 5,
		},
		{
			name:   "quoted line with no result of its own",
			input:  "> Wordle 1890 1/6\nnice one",
			wantOK: false,
		},
		{
			name:   "quote marker after whitespace",
			input:  "   > Wordle 1890 1/6\nWordle 1891 5/6",
			wantOK: true, puzzle: 1891, solved: true, guesses: 5,
		},

		{
			name:   "first result wins",
			input:  "Wordle 1890 3/6\nWordle 1891 5/6",
			wantOK: true, puzzle: 1890, solved: true, guesses: 3,
		},

		// The grid is never parsed, which is why theme and contrast mode do
		// not matter.
		{
			name:   "dark theme grid",
			input:  "Wordle 1890 3/6\n\n⬛🟨⬛⬛⬛\n⬛🟩🟩⬛🟨\n🟩🟩🟩🟩🟩",
			wantOK: true, puzzle: 1890, solved: true, guesses: 3,
		},
		{
			name:   "light theme grid",
			input:  "Wordle 1890 3/6\n\n⬜⬜🟨⬜⬜\n⬜🟩⬜🟩🟩\n🟩🟩🟩🟩🟩",
			wantOK: true, puzzle: 1890, solved: true, guesses: 3,
		},
		{
			name:   "high contrast grid",
			input:  "Wordle 1890 3/6\n\n⬛🟦⬛⬛⬛\n⬛🟧🟧⬛🟦\n🟧🟧🟧🟧🟧",
			wantOK: true, puzzle: 1890, solved: true, guesses: 3,
		},
		{
			name:   "grid alone is not a result",
			input:  "🟩🟩🟩🟩🟩",
			wantOK: false,
		},

		// Ordinary conversation, which is most of the group's traffic.
		{name: "empty", input: "", wantOK: false},
		{name: "chat", input: "anyone else find that one brutal", wantOK: false},
		{name: "mentions wordle without a score", input: "wordle was hard today", wantOK: false},
		{name: "wrong denominator", input: "Wordle 1890 3/5", wantOK: false},
		{name: "guess count out of range", input: "Wordle 1890 7/6", wantOK: false},
		{name: "zero guesses", input: "Wordle 1890 0/6", wantOK: false},

		// Surrounding text and leading blank lines.
		{
			name:   "leading blank lines",
			input:  "\n\n\nWordle 1890 4/6",
			wantOK: true, puzzle: 1890, solved: true, guesses: 4,
		},
		{
			name:   "text before the result on its own line",
			input:  "phew\nWordle 1890 6/6",
			wantOK: true, puzzle: 1890, solved: true, guesses: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("Parse() ok = %v, want %v (result %+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.PuzzleNo != tt.puzzle {
				t.Errorf("PuzzleNo = %d, want %d", got.PuzzleNo, tt.puzzle)
			}
			if got.Solved != tt.solved {
				t.Errorf("Solved = %v, want %v", got.Solved, tt.solved)
			}
			if got.Guesses != tt.guesses {
				t.Errorf("Guesses = %d, want %d", got.Guesses, tt.guesses)
			}
			if got.HardMode != tt.hardMode {
				t.Errorf("HardMode = %v, want %v", got.HardMode, tt.hardMode)
			}
		})
	}
}

// The four forms printed, which all denote the same puzzle. They differ
// by locale and by whether the sender used the app or the browser.
func TestParseSpecExamples(t *testing.T) {
	tests := map[string]Result{
		"Wordle 1 890 4/6\n⬛🟨⬛⬛⬛\n⬛⬛⬛⬛⬛\n⬛🟩🟩⬛🟩\n🟩🟩🟩🟩🟩": {PuzzleNo: 1890, Solved: true, Guesses: 4, Grid: "nynnn/nnnnn/nggng/ggggg"},
		"Wordle 1 890 3/6*\n⬛🟨⬛⬛⬛\n⬛🟩🟩⬛🟨\n🟩🟩🟩🟩🟩":       {PuzzleNo: 1890, Solved: true, Guesses: 3, HardMode: true, Grid: "nynnn/nggny/ggggg"},
		"Wordle 1,890 3/6\n🟨⬛⬛⬛⬛\n🟨🟨🟨⬛⬛\n🟩🟩🟩🟩🟩":        {PuzzleNo: 1890, Solved: true, Guesses: 3, Grid: "ynnnn/yyynn/ggggg"},
		"Wordle 1,890 3/6*\n⬜⬜🟨⬜⬜\n⬜🟩⬜🟩🟩\n🟩🟩🟩🟩🟩":       {PuzzleNo: 1890, Solved: true, Guesses: 3, HardMode: true, Grid: "nnynn/ngngg/ggggg"},
	}

	for input, want := range tests {
		got, ok := Parse(input)
		if !ok {
			t.Errorf("Parse(%q) found no result", input)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %+v, want %+v", input, got, want)
		}
	}
}

// The grid under the header is read into letters, whatever squares the
// sharer's theme drew it in, and kept only when it agrees with the score.
// The header alone decides the score either way: a grid that is missing or
// wrong costs the grid, never the result.
func TestParseGrid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  Grid
	}{
		{"captured message, blank line before the grid",
			"Wordle 1 891 3/6*\n\n⬛🟨⬛⬛⬛\n⬛🟩🟩⬛🟨\n🟩🟩🟩🟩🟩", "nynnn/nggny/ggggg"},
		{"light theme", "Wordle 1,891 2/6\n\n⬜🟨⬜⬜⬜\n🟩🟩🟩🟩🟩", "nynnn/ggggg"},
		{"high contrast", "Wordle 1,891 2/6\n\n⬛🟦⬛⬛🟧\n🟧🟧🟧🟧🟧", "nynng/ggggg"},
		{"variation selectors between squares", "Wordle 1,891 2/6\n\n⬜️🟨⬜️⬜️⬜️\n🟩🟩🟩🟩🟩", "nynnn/ggggg"},
		{"windows line endings", "Wordle 1,891 2/6\r\n\r\n⬛🟨⬛⬛⬛\r\n🟩🟩🟩🟩🟩\r\n", "nynnn/ggggg"},
		{"a one", "Wordle 1,891 1/6\n\n🟩🟩🟩🟩🟩", "ggggg"},
		{"a miss has six rows and no win",
			"Wordle 1,891 X/6\n\n⬛⬛⬛⬛⬛\n⬛🟨⬛⬛⬛\n⬛🟩🟩⬛⬛\n🟩🟩🟩⬛⬛\n🟩🟩🟩⬛🟩\n🟩🟩🟩🟨🟩",
			"nnnnn/nynnn/nggnn/gggnn/gggng/gggyg"},
		{"chat after the grid is not part of it",
			"Wordle 1,891 2/6\n\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩\nphew, lucky", "nynnn/ggggg"},
		{"chat before the header does not matter",
			"morning all\nWordle 1,891 2/6\n\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩", "nynnn/ggggg"},

		// Refused: the result files, the grid does not.
		{"no grid at all", "Wordle 1,891 3/6", ""},
		{"too few rows for the score", "Wordle 1,891 3/6\n\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩", ""},
		{"too many rows for the score", "Wordle 1,891 2/6\n\n⬛⬛⬛⬛⬛\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩", ""},
		{"a solve that does not end green", "Wordle 1,891 2/6\n\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟨🟩", ""},
		{"a green row before the end", "Wordle 1,891 3/6\n\n🟩🟩🟩🟩🟩\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩", ""},
		{"a miss that ends green",
			"Wordle 1,891 X/6\n\n⬛⬛⬛⬛⬛\n⬛⬛⬛⬛⬛\n⬛⬛⬛⬛⬛\n⬛⬛⬛⬛⬛\n⬛⬛⬛⬛⬛\n🟩🟩🟩🟩🟩", ""},
		{"a row of four", "Wordle 1,891 2/6\n\n⬛🟨⬛⬛\n🟩🟩🟩🟩🟩", ""},
		{"text on the grid's first line", "Wordle 1,891 2/6\nnice\n⬛🟨⬛⬛⬛\n🟩🟩🟩🟩🟩", ""},
		{"a grid split by a blank line", "Wordle 1,891 2/6\n\n⬛🟨⬛⬛⬛\n\n🟩🟩🟩🟩🟩", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.input)
			if !ok {
				t.Fatal("the result itself was not parsed")
			}
			if got.Grid != tt.want {
				t.Errorf("Grid = %q, want %q", got.Grid, tt.want)
			}
		})
	}
}
