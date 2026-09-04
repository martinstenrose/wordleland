package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	const password = "correct horse battery staple"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}
	if err := VerifyPassword(encoded, password); err != nil {
		t.Errorf("VerifyPassword() rejected the correct password: %v", err)
	}
}

func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}

	for _, wrong := range []string{
		"",
		"Correct horse battery staple", // case
		"correct horse battery stapl",  // truncated
		"correct horse battery staple ",
	} {
		if err := VerifyPassword(encoded, wrong); !errors.Is(err, ErrMismatch) {
			t.Errorf("VerifyPassword(%q) = %v, want ErrMismatch", wrong, err)
		}
	}
}

// The salt is per-hash, so the same password must never produce the same
// stored value: identical hashes would reveal which accounts share a password.
func TestHashPasswordIsSalted(t *testing.T) {
	const password = "same password"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}

	if first == second {
		t.Error("hashing the same password twice produced identical output; the salt is not random")
	}
	for _, encoded := range []string{first, second} {
		if err := VerifyPassword(encoded, password); err != nil {
			t.Errorf("VerifyPassword() failed for a valid hash: %v", err)
		}
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Error("HashPassword(\"\") succeeded, want an error")
	}
}

func TestHashPasswordFormat(t *testing.T) {
	encoded, err := HashPassword("whatever")
	if err != nil {
		t.Fatalf("HashPassword() failed: %v", err)
	}

	// PHC format, with the parameters recorded alongside the hash so they can
	// be raised later without invalidating existing passwords.
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("encoded hash = %q, want the PHC prefix with current parameters", encoded)
	}
	if n := strings.Count(encoded, "$"); n != 5 {
		t.Errorf("encoded hash has %d separators, want 5: %q", n, encoded)
	}
	if strings.Contains(encoded, "whatever") {
		t.Error("the encoded hash contains the password")
	}
}

// A hash made with different parameters must still verify, which is what
// makes raising the cost a non-event rather than a migration.
func TestVerifyPasswordAcceptsOtherParameters(t *testing.T) {
	// Produced with m=16384,t=1,p=1 — deliberately weaker than current
	// settings, standing in for a hash written by an older release.
	weaker := "$argon2id$v=19$m=16384,t=1,p=1$" +
		"c29tZXNhbHR2YWx1ZQ$" // "somesaltvalue"
	// Derive the matching key so the fixture is self-consistent rather than
	// a copied literal that could rot.
	encoded := buildHashForTest(t, weaker, "hunter2")

	if err := VerifyPassword(encoded, "hunter2"); err != nil {
		t.Errorf("VerifyPassword() rejected a hash with older parameters: %v", err)
	}
	if err := VerifyPassword(encoded, "wrong"); !errors.Is(err, ErrMismatch) {
		t.Errorf("VerifyPassword() = %v for a wrong password, want ErrMismatch", err)
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"not a hash", "hunter2"},
		{"too few fields", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA"},
		{"wrong algorithm", "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{"bcrypt", "$2y$10$abcdefghijklmnopqrstuv"},
		{"unreadable version", "$argon2id$vNN$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{"wrong version", "$argon2id$v=16$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{"unreadable parameters", "$argon2id$v=19$nonsense$c2FsdA$aGFzaA"},
		{"zero parameters", "$argon2id$v=19$m=0,t=0,p=0$c2FsdA$aGFzaA"},
		// argon2 allocates whatever m= says, so a row asking for more than
		// the build allows must be refused rather than sized to. Just over
		// the ceiling on purpose: a plausible-looking number, so what is
		// being checked is the bound and not "this is obviously absurd".
		{"more memory than the build allows", "$argon2id$v=19$m=589824,t=3,p=2$c2FsdA$aGFzaA"},
		{"bad salt encoding", "$argon2id$v=19$m=65536,t=3,p=2$not!base64$aGFzaA"},
		{"bad key encoding", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$not!base64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyPassword(tt.encoded, "hunter2")
			if err == nil {
				t.Fatal("VerifyPassword() succeeded on a malformed hash")
			}
			// An unreadable stored hash is an operational fault, not a failed
			// login: reporting it as ErrMismatch would hide a corrupt row
			// behind a stream of "wrong password".
			if errors.Is(err, ErrMismatch) {
				t.Errorf("error = ErrMismatch, want a distinct malformed-hash error")
			}
		})
	}
}

// buildHashForTest completes a PHC prefix by deriving the real key for
// password, so parameter fixtures stay consistent with the code under test.
func buildHashForTest(t *testing.T, prefix, password string) string {
	t.Helper()

	params, salt, _, err := decodeHash(prefix + "AAAA")
	if err != nil {
		t.Fatalf("fixture prefix is malformed: %v", err)
	}
	key := argon2IDKey(password, salt, params)
	return prefix + encodeBase64(key)
}
