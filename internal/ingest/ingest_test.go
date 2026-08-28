package ingest

import (
	"errors"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// A submission names its player exactly once. Two identifiers is a bug in
// the caller, and picking one would hide it.
func TestMethodRefusesAmbiguity(t *testing.T) {
	tests := []struct {
		name string
		sub  Submission
		want string
	}{
		{"sender pair", Submission{Source: "signal", ExternalID: "u1"}, "sender"},
		{"player id", Submission{PlayerID: ptr(int64(7))}, "player_id"},
		{"slug", Submission{Slug: "martin"}, "slug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.sub.Method()
			if err != nil {
				t.Fatalf("Method() failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("Method() = %q, want %q", got, tt.want)
			}
		})
	}

	bad := []struct {
		name string
		sub  Submission
		want string
	}{
		{"nothing", Submission{}, "name the player"},
		{"source without id", Submission{Source: "signal"}, "together"},
		{"id without source", Submission{ExternalID: "u1"}, "together"},
		{"sender and slug", Submission{Source: "signal", ExternalID: "u1", Slug: "martin"}, "only once"},
		{"id and slug", Submission{PlayerID: ptr(int64(7)), Slug: "martin"}, "only once"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.sub.Method()
			if err == nil {
				t.Fatal("Method() accepted an ambiguous submission")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
			// The caller has to be able to tell its own mistake from ours.
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Errorf("error is %T, want a *ValidationError", err)
			}
		})
	}
}

// The score itself, independently of whose it is. A failure carries no
// guess count: the "7" convention lives in computation, never in storage.
func TestValidate(t *testing.T) {
	ok := []struct {
		name string
		sub  Submission
	}{
		{"solved in one", Submission{PuzzleNo: 1, Solved: true, Guesses: ptr(1)}},
		{"solved in six", Submission{PuzzleNo: 1, Solved: true, Guesses: ptr(6)}},
		{"failed", Submission{PuzzleNo: 1, Solved: false}},
	}
	for _, tt := range ok {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.sub.Validate(); err != nil {
				t.Errorf("Validate() rejected a valid submission: %v", err)
			}
		})
	}

	bad := []struct {
		name string
		sub  Submission
		want string
	}{
		{"no puzzle", Submission{Solved: false}, "puzzle_no"},
		{"negative puzzle", Submission{PuzzleNo: -1, Solved: false}, "puzzle_no"},
		{"solved without a count", Submission{PuzzleNo: 1, Solved: true}, "guesses is required"},
		{"solved in zero", Submission{PuzzleNo: 1, Solved: true, Guesses: ptr(0)}, "between 1 and 6"},
		{"solved in seven", Submission{PuzzleNo: 1, Solved: true, Guesses: ptr(7)}, "between 1 and 6"},
		{"failed with a count", Submission{PuzzleNo: 1, Solved: false, Guesses: ptr(7)}, "omitted"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sub.Validate()
			if err == nil {
				t.Fatal("Validate() accepted an impossible score")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Errorf("error is %T, want a *ValidationError", err)
			}
		})
	}
}

// A seven is what the group writes for a failure, and it must never be
// storable as a guess count — that convention belongs in computation only.
func TestSevenIsNotAGuessCount(t *testing.T) {
	sub := Submission{PuzzleNo: 1891, Solved: true, Guesses: ptr(7)}
	if err := sub.Validate(); err == nil {
		t.Fatal("a seven was accepted as a solved score")
	}
}
