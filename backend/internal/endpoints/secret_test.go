package endpoints

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey(fill byte) []byte { return bytes.Repeat([]byte{fill}, 32) }

func TestGenerateSecret(t *testing.T) {
	a, err := GenerateSecret()
	require.NoError(t, err)
	b, err := GenerateSecret()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(a, "whsec_"))
	assert.Len(t, a, len("whsec_")+43)
	assert.NotEqual(t, a, b)
}

func TestSecretCipherRoundTrip(t *testing.T) {
	c, err := NewSecretCipher(testKey(1))
	require.NoError(t, err)
	secret, err := GenerateSecret()
	require.NoError(t, err)

	ct, err := c.Encrypt(secret)
	require.NoError(t, err)
	assert.NotContains(t, string(ct), secret, "ciphertext must not contain the plaintext")
	assert.Len(t, ct, 12+len(secret)+16, "nonce + ciphertext + tag")

	got, err := c.Decrypt(ct)
	require.NoError(t, err)
	assert.Equal(t, secret, got)
}

func TestSecretCipherUsesFreshNonce(t *testing.T) {
	c, err := NewSecretCipher(testKey(1))
	require.NoError(t, err)
	a, err := c.Encrypt("whsec_same")
	require.NoError(t, err)
	b, err := c.Encrypt("whsec_same")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "encrypting twice must not produce the same bytes")
	assert.NotEqual(t, a[:12], b[:12], "nonces must differ")
}

func TestSecretCipherDetectsTampering(t *testing.T) {
	c, err := NewSecretCipher(testKey(1))
	require.NoError(t, err)
	ct, err := c.Encrypt("whsec_tamper_me")
	require.NoError(t, err)

	// Flip one bit in every position in turn: nonce, ciphertext and tag are all authenticated.
	for i := range ct {
		tampered := bytes.Clone(ct)
		tampered[i] ^= 0x01
		_, err := c.Decrypt(tampered)
		require.ErrorIs(t, err, ErrDecrypt, "bit flip at byte %d went undetected", i)
	}

	for _, short := range [][]byte{nil, ct[:5], ct[:12], ct[:27]} {
		_, err := c.Decrypt(short)
		assert.ErrorIs(t, err, ErrDecrypt)
	}
	_, err = c.Decrypt(ct[:len(ct)-1])
	assert.ErrorIs(t, err, ErrDecrypt, "truncated ciphertext")
}

func TestSecretCipherWrongKey(t *testing.T) {
	c1, err := NewSecretCipher(testKey(1))
	require.NoError(t, err)
	c2, err := NewSecretCipher(testKey(2))
	require.NoError(t, err)

	ct, err := c1.Encrypt("whsec_key_one")
	require.NoError(t, err)
	_, err = c2.Decrypt(ct)
	assert.ErrorIs(t, err, ErrDecrypt)
}

func TestNewSecretCipherRejectsBadKeyLength(t *testing.T) {
	for _, n := range []int{0, 16, 24, 31, 33} {
		_, err := NewSecretCipher(make([]byte, n))
		assert.Error(t, err, "key of %d bytes", n)
	}
}
