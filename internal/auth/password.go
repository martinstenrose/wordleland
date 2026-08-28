// Package auth holds password hashing, sessions, TOTP and authorization.
// It is hand-rolled deliberately: the surface is small, and the
// defences here are the whole of the protection rather than a second layer
// behind a secret URL.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. These are a starting point rather than a ceiling: the
// encoding below records the parameters alongside each hash, so raising them
// later re-hashes on next login instead of invalidating every password.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB, so 64 MiB per hash
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// argonVersion is the argon2 version the encoding records. Verifying a hash
// produced by a different version is refused rather than attempted, since the
// derived key would differ.
const argonVersion = argon2.Version

// ErrMismatch is returned when a password does not match a hash. It is
// deliberately indistinguishable from every other wrong-password outcome:
// callers must not tell a user whether the account or the password was wrong.
var ErrMismatch = errors.New("password does not match")

// HashPassword derives an argon2id hash in PHC string format:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
//
// The parameters travel with the hash so they can be raised without a
// migration: an old hash still verifies under the parameters it was made with.
//
// Memory is the expensive part at 64 MiB per call, which is why the login path
// bounds concurrent hashing with a semaphore. Nothing here enforces
// that — this function is deliberately just the primitive.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password is empty")
	}

	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argonVersion, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches encoded.
//
// It returns ErrMismatch for a wrong password and a different error only when
// the stored hash is unreadable, which is an operational fault rather than a
// failed login.
func VerifyPassword(encoded, password string) error {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt,
		params.time, params.memory, params.threads, uint32(len(want)))

	// Constant time: a byte-wise comparison leaks how much of the hash
	// matched, which is enough to narrow a search.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func decodeHash(encoded string) (params argonParams, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	// A leading "$" makes the first field empty: "", "argon2id", "v=19",
	// "m=...,t=...,p=...", salt, key.
	if len(parts) != 6 || parts[0] != "" {
		return params, nil, nil, errors.New("password hash is malformed")
	}
	if parts[1] != "argon2id" {
		return params, nil, nil, fmt.Errorf("password hash uses %q, want argon2id", parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return params, nil, nil, errors.New("password hash has no readable version")
	}
	if version != argonVersion {
		return params, nil, nil, fmt.Errorf("password hash uses argon2 version %d, want %d", version, argonVersion)
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&params.memory, &params.time, &params.threads); err != nil {
		return params, nil, nil, errors.New("password hash has no readable parameters")
	}
	if params.memory == 0 || params.time == 0 || params.threads == 0 {
		return params, nil, nil, errors.New("password hash has zero parameters")
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return params, nil, nil, errors.New("password hash has an unreadable salt")
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return params, nil, nil, errors.New("password hash is unreadable")
	}
	if len(salt) == 0 || len(key) == 0 {
		return params, nil, nil, errors.New("password hash is empty")
	}
	return params, salt, key, nil
}

// argon2IDKey and encodeBase64 exist so tests can build hashes with arbitrary
// parameters without duplicating the derivation.
func argon2IDKey(password string, salt []byte, p argonParams) []byte {
	return argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, argonKeyLen)
}

func encodeBase64(b []byte) string {
	return base64.RawStdEncoding.EncodeToString(b)
}

// MinPasswordLength is a floor, not a policy. Composition rules push people
// toward predictable substitutions; length is what actually helps, and the
// real defences are the login rate limit and mandatory admin 2FA.
const MinPasswordLength = 12
