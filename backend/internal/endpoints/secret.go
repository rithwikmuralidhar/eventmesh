// Package endpoints manages webhook endpoints: validation, CRUD, signing
// secrets and per-endpoint delivery settings.
package endpoints

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"

	"eventmesh/internal/token"
)

// SecretPrefix marks a webhook signing secret.
const SecretPrefix = "whsec_"

// ErrDecrypt is returned when a stored secret cannot be decrypted: the
// ciphertext was modified, truncated, or encrypted with a different key.
var ErrDecrypt = errors.New("endpoint secret could not be decrypted")

// GenerateSecret returns a new signing secret: "whsec_" plus 32 random bytes
// in base62.
func GenerateSecret() (string, error) {
	random, err := token.Base62From32Bytes()
	if err != nil {
		return "", fmt.Errorf("generate endpoint secret: %w", err)
	}
	return SecretPrefix + random, nil
}

// SecretCipher encrypts endpoint secrets at rest with AES-256-GCM.
//
// Unlike an API key, the signing secret cannot be stored as a hash: the
// worker needs the plaintext to compute the HMAC of every delivery. So it is
// encrypted with a key that lives only in the environment, which means a
// leaked database dump alone does not reveal the secrets.
type SecretCipher struct {
	aead cipher.AEAD
}

// NewSecretCipher builds a cipher from a 32-byte key.
func NewSecretCipher(key []byte) (*SecretCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &SecretCipher{aead: aead}, nil
}

// Encrypt returns nonce || ciphertext || tag. A fresh random nonce per
// encryption is required by GCM: reusing a nonce under the same key breaks
// both confidentiality and authenticity. With 96-bit random nonces the
// collision risk is negligible at this system's scale.
func (c *SecretCipher) Encrypt(plaintext string) ([]byte, error) {
	nonce, err := token.RandomBytes(c.aead.NonceSize())
	if err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal appends to its first argument, so passing nonce puts it in front.
	return c.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt reverses Encrypt. GCM authenticates the ciphertext, so any
// modification is detected and reported as ErrDecrypt rather than returning
// garbage.
func (c *SecretCipher) Decrypt(ciphertext []byte) (string, error) {
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize+c.aead.Overhead() {
		return "", ErrDecrypt
	}
	plaintext, err := c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plaintext), nil
}
