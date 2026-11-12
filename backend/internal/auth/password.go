// Package auth implements dashboard authentication: password hashing,
// server-side sessions and the login, registration and logout flows.
package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"eventmesh/internal/token"
)

// MinPasswordLen is the minimum accepted password length.
const MinPasswordLen = 10

// MaxPasswordLen bounds the work an attacker can force by submitting a huge password.
const MaxPasswordLen = 256

const (
	saltLen = 16
	keyLen  = 32
)

// ErrInvalidHash is returned when a stored password hash cannot be parsed.
var ErrInvalidHash = errors.New("password hash is malformed")

// PasswordHasher hashes passwords with Argon2id. The parameters are fields so
// tests can use cheap settings; production uses DefaultPasswordHasher.
type PasswordHasher struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultPasswordHasher returns the production parameters: 64 MB of memory,
// 3 iterations, parallelism 2. Memory-hardness is what makes offline guessing
// expensive on GPUs, which is why Argon2id is used for passwords while API
// keys (already 256 bits of randomness) get a plain SHA-256.
func DefaultPasswordHasher() PasswordHasher {
	return PasswordHasher{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2}
}

// Hash returns a self-describing PHC string:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>. The parameters travel with
// the hash, so they can be raised later without breaking existing passwords.
func (h PasswordHasher) Hash(password string) (string, error) {
	salt, err := token.RandomBytes(saltLen)
	if err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.Iterations, h.MemoryKiB, h.Parallelism, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.MemoryKiB, h.Iterations, h.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored PHC hash, using
// the parameters recorded in the hash itself.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 128 {
		return false, ErrInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ValidatePassword checks the length rules for a new password.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(password) > MaxPasswordLen {
		return fmt.Errorf("password must be at most %d characters", MaxPasswordLen)
	}
	return nil
}
