package main

import (
	"os"
	"testing"

	"github.com/martinstenrose/wordleland/internal/auth/authtest"
)

// TestMain hashes at argon2's minimum cost: `user create` and the admin
// bootstrap hash on every call, and these tests are about the CLI, not the
// key derivation. Set here, before any test starts, and nowhere else.
func TestMain(m *testing.M) {
	hashPassword = authtest.HashPassword
	os.Exit(m.Run())
}
