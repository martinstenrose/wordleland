// Package authtest hashes passwords for tests at argon2's minimum cost.
//
// A test that signs in is testing sessions, limits and forms, not the key
// derivation, and 64 MiB per hash under the race detector was most of what
// those tests cost. HashPassword has auth.HashPassword's signature so it drops
// into the same seams; the hash it writes records its parameters, so
// auth.VerifyPassword checks it at the same low cost.
//
// Only test files import this package, so it never reaches the binary.
// internal/auth's own tests do not use it: they pin the real parameters.
package authtest

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// HashPassword is auth.HashPassword at m=8 KiB, t=1, p=1.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password is empty")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	const memory, passes, threads = 8, 1, 1
	key := argon2.IDKey([]byte(password), salt, passes, memory, threads, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, passes, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}
