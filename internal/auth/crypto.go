package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// ErrDecrypt covers any failure to recover a secret: a wrong key, a truncated
// value, or a tampered one. They are not distinguished, because the response
// to all three is the same and telling them apart helps only an attacker.
var ErrDecrypt = errors.New("cannot decrypt")

// Cipher encrypts TOTP secrets at rest with AES-GCM.
//
// The operational consequence is stated in docs/decisions.md and is worth repeating
// where the code is: losing the key makes every enrolled secret
// unrecoverable. The key belongs in the backup routine, and the CLI's
// reset-2fa is what makes losing it survivable rather than fatal.
type Cipher struct {
	aead cipher.AEAD
}

// KeyLen is the key length required, matching what config validates.
const KeyLen = 32

// NewCipher builds a Cipher from a 32-byte key.
//
// Callers validate the key at boot rather than at first use: a
// misconfigured key that only surfaces when someone first enrols would be
// discovered at the worst possible moment.
//
// The length is re-checked here rather than trusted from config. aes.NewCipher
// would happily accept 16 or 24 bytes and silently give AES-128, so the
// package that depends on AES-256 is the one that should insist on it.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext, returning nonce||ciphertext.
//
// GCM is authenticated, so a stored value that has been altered fails to
// decrypt rather than yielding a plausible-looking wrong secret — which for a
// TOTP secret would mean silently locking someone out with no clue why.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// The nonce is prepended rather than stored separately: it is not secret,
	// and keeping it with the ciphertext removes any chance of the two being
	// separated by a schema change later.
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt opens a value produced by Encrypt.
func (c *Cipher) Decrypt(sealed []byte) ([]byte, error) {
	nonceSize := c.aead.NonceSize()
	if len(sealed) < nonceSize {
		return nil, ErrDecrypt
	}
	plaintext, err := c.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// randomBytes is a small helper for callers needing entropy.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return b, nil
}
