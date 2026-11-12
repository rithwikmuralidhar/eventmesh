// Package apikeys manages the API keys external clients use to ingest events.
package apikeys

import (
	"errors"
	"fmt"
	"strings"

	"eventmesh/internal/token"
)

const (
	keyScheme = "em"
	prefixLen = 8
)

// ErrMalformedKey is returned when a string does not have the API key format.
var ErrMalformedKey = errors.New("malformed api key")

// GeneratedKey is a new key. Plaintext is shown to the user exactly once and
// is never stored; only Prefix (for display) and Hash (for lookup) are kept.
type GeneratedKey struct {
	Plaintext string
	Prefix    string
	Hash      []byte
}

// Generate creates a key of the form em_<8 char prefix>_<43 base62 chars>.
// The secret part is 32 random bytes, so the key cannot be guessed.
func Generate() (GeneratedKey, error) {
	prefix, err := token.RandomString(prefixLen)
	if err != nil {
		return GeneratedKey{}, fmt.Errorf("generate key prefix: %w", err)
	}
	secret, err := token.Base62From32Bytes()
	if err != nil {
		return GeneratedKey{}, fmt.Errorf("generate key secret: %w", err)
	}
	plaintext := keyScheme + "_" + prefix + "_" + secret
	return GeneratedKey{Plaintext: plaintext, Prefix: prefix, Hash: Hash(plaintext)}, nil
}

// Hash returns the lookup hash of a key. A single SHA-256 is correct here:
// the key has 256 bits of entropy, so there is no dictionary to attack and a
// slow hash such as bcrypt would only slow down every ingestion request.
func Hash(plaintext string) []byte {
	return token.SHA256(plaintext)
}

// Parse checks the shape of a presented key and returns its display prefix.
// It rejects obviously invalid input before any database lookup.
func Parse(plaintext string) (prefix string, err error) {
	parts := strings.Split(plaintext, "_")
	if len(parts) != 3 || parts[0] != keyScheme {
		return "", ErrMalformedKey
	}
	if len(parts[1]) != prefixLen || !token.IsBase62(parts[1]) {
		return "", ErrMalformedKey
	}
	if len(parts[2]) < token.Base62Len32 || len(parts[2]) > 64 || !token.IsBase62(parts[2]) {
		return "", ErrMalformedKey
	}
	return parts[1], nil
}
