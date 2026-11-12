// Package token generates the random strings used for API keys, session
// tokens and endpoint secrets. Everything comes from crypto/rand.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Base62Len32 is the fixed length of 32 random bytes encoded in base62:
// ceil(256 / log2(62)) = 43 characters.
const Base62Len32 = 43

// RandomBytes returns n bytes from the operating system's CSPRNG.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return b, nil
}

// Base62From32Bytes returns 32 random bytes encoded as exactly 43 base62
// characters. The value is left-padded with '0' so the length never varies,
// which keeps parsing and validation simple.
func Base62From32Bytes() (string, error) {
	b, err := RandomBytes(32)
	if err != nil {
		return "", err
	}
	return EncodeBase62(b, Base62Len32), nil
}

// EncodeBase62 encodes b as a big-endian base62 number, left-padded to width.
func EncodeBase62(b []byte, width int) string {
	n := new(big.Int).SetBytes(b)
	s := n.Text(62) // big.Int uses 0-9a-zA-Z; any fixed alphabet is fine here.
	if len(s) < width {
		s = strings.Repeat("0", width-len(s)) + s
	}
	return s
}

// RandomString returns n characters drawn uniformly from the base62 alphabet.
// rand.Int is used instead of "byte % 62" because the modulo would bias the
// first characters of the alphabet.
func RandomString(n int) (string, error) {
	limit := big.NewInt(int64(len(base62Alphabet)))
	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("read random index: %w", err)
		}
		out[i] = base62Alphabet[idx.Int64()]
	}
	return string(out), nil
}

// IsBase62 reports whether s is non-empty and contains only base62 characters.
func IsBase62(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(base62Alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// SHA256 returns the SHA-256 digest of s. High-entropy tokens (API keys,
// session tokens) are stored as this digest: a slow password hash would add
// cost without adding security, because there is nothing to brute-force.
func SHA256(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
