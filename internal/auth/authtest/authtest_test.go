package authtest

import (
	"errors"
	"testing"

	"github.com/martinstenrose/wordleland/internal/auth"
)

// The encoding here is written out a second time, so this is what keeps it
// in step with the one auth.VerifyPassword reads.
func TestHashVerifiesWithAuth(t *testing.T) {
	t.Parallel()

	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}
	if err := auth.VerifyPassword(encoded, "correct horse battery staple"); err != nil {
		t.Errorf("VerifyPassword() rejected the correct password: %v", err)
	}
	if err := auth.VerifyPassword(encoded, "wrong"); !errors.Is(err, auth.ErrMismatch) {
		t.Errorf("VerifyPassword() = %v for a wrong password, want ErrMismatch", err)
	}
}

func TestHashRejectsEmpty(t *testing.T) {
	t.Parallel()

	if _, err := HashPassword(""); err == nil {
		t.Error("HashPassword(\"\") succeeded, want an error")
	}
}
