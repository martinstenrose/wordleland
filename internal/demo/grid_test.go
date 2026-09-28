package demo

import "testing"

// Every invented grid is one ingest would accept for its score.
func TestGridForAgreesWithItsOutcome(t *testing.T) {
	t.Parallel()

	outcomes := []Outcome{{Solved: false}}
	for g := 1; g <= 6; g++ {
		outcomes = append(outcomes, Outcome{Solved: true, Guesses: g})
	}
	for puzzle := 1800; puzzle < 1900; puzzle++ {
		for _, o := range outcomes {
			if g := GridFor("someone", puzzle, 0, o); !g.Agrees(o.Solved, o.Guesses) {
				t.Fatalf("puzzle %d, %+v: grid %q does not agree", puzzle, o, g)
			}
		}
	}
}
