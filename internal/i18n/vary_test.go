package i18n

import "testing"

// Each wording in turn, round again, and the same words never twice
// running: the point of writing a line more than one way.
func TestVarySaysEachWordingInTurn(t *testing.T) {
	t.Parallel()
	cats := Catalogues{"en": {"hi": "Hi %s", "hi.2": "Hello %s", "hi.3": "Hey %s", "bye": "Bye"}}
	tr := NewTranslator(cats, "en").Rotating()

	var got []string
	for range 4 {
		got = append(got, tr.Vary("hi", "Bo"))
	}
	want := []string{"Hi Bo", "Hello Bo", "Hey Bo", "Hi Bo"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Vary in turn = %q, want %q", got, want)
		}
	}
	// One wording is that wording every time, and keys keep their own turn.
	if a, b := tr.Vary("bye"), tr.Vary("bye"); a != "Bye" || b != "Bye" {
		t.Errorf("a line with one wording = %q, %q", a, b)
	}
	if got := tr.Vary("hi", "Bo"); got != "Hello Bo" {
		t.Errorf("after another key, Vary = %q, want the next of its own", got)
	}
}

// A translator that was never made rotating says the first wording, which
// is what lets a test pin an answer.
func TestVaryWithoutRotatingIsTheFirstWording(t *testing.T) {
	t.Parallel()
	cats := Catalogues{"en": {"hi": "Hi", "hi.2": "Hello"}}
	tr := NewTranslator(cats, "en")
	for range 3 {
		if got := tr.Vary("hi"); got != "Hi" {
			t.Fatalf("Vary = %q, want %q", got, "Hi")
		}
	}
}
