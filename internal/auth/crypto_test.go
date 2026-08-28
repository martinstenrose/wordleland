package auth

import (
	"bytes"
	"errors"
	"testing"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("NewCipher() failed: %v", err)
	}
	return c
}

func TestCipherRoundTrip(t *testing.T) {
	c := testCipher(t)
	secret := []byte("JBSWY3DPEHPK3PXP")

	sealed, err := c.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt() failed: %v", err)
	}
	if bytes.Contains(sealed, secret) {
		t.Error("the ciphertext contains the plaintext")
	}

	opened, err := c.Decrypt(sealed)
	if err != nil {
		t.Fatalf("Decrypt() failed: %v", err)
	}
	if !bytes.Equal(opened, secret) {
		t.Errorf("Decrypt() = %q, want %q", opened, secret)
	}
}

// A fresh nonce per call, so two enrolments of the same secret do not produce
// identical stored values.
func TestCipherIsNonDeterministic(t *testing.T) {
	c := testCipher(t)
	secret := []byte("JBSWY3DPEHPK3PXP")

	first, err := c.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt() failed: %v", err)
	}
	second, err := c.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt() failed: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Error("encrypting the same secret twice produced identical output")
	}
}

// GCM is authenticated: a tampered value must fail rather than decrypt to a
// plausible wrong secret, which for TOTP would lock someone out silently.
func TestCipherRejectsTampering(t *testing.T) {
	c := testCipher(t)

	sealed, err := c.Encrypt([]byte("JBSWY3DPEHPK3PXP"))
	if err != nil {
		t.Fatalf("Encrypt() failed: %v", err)
	}

	tests := map[string]func([]byte) []byte{
		"flipped ciphertext bit": func(b []byte) []byte {
			out := bytes.Clone(b)
			out[len(out)-1] ^= 0x01
			return out
		},
		"flipped nonce bit": func(b []byte) []byte {
			out := bytes.Clone(b)
			out[0] ^= 0x01
			return out
		},
		"truncated":            func(b []byte) []byte { return b[:len(b)-1] },
		"empty":                func([]byte) []byte { return nil },
		"shorter than a nonce": func(b []byte) []byte { return b[:4] },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Decrypt(mutate(sealed)); !errors.Is(err, ErrDecrypt) {
				t.Errorf("Decrypt() error = %v, want ErrDecrypt", err)
			}
		})
	}
}

// Losing the key makes every enrolled secret unrecoverable. This pins
// that so it is a documented property rather than a surprise.
func TestCipherWithWrongKeyFails(t *testing.T) {
	c := testCipher(t)
	sealed, err := c.Encrypt([]byte("JBSWY3DPEHPK3PXP"))
	if err != nil {
		t.Fatalf("Encrypt() failed: %v", err)
	}

	other, err := NewCipher(bytes.Repeat([]byte{0x99}, 32))
	if err != nil {
		t.Fatalf("NewCipher() failed: %v", err)
	}
	if _, err := other.Decrypt(sealed); !errors.Is(err, ErrDecrypt) {
		t.Errorf("Decrypt() with a different key error = %v, want ErrDecrypt", err)
	}
}

func TestNewCipherRejectsBadKeyLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := NewCipher(bytes.Repeat([]byte{1}, n)); err == nil {
			t.Errorf("NewCipher() accepted a %d-byte key", n)
		}
	}
}
