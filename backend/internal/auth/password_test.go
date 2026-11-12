package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cheap parameters keep the unit tests fast; the format and logic are identical.
var testHasher = PasswordHasher{MemoryKiB: 64, Iterations: 1, Parallelism: 1}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := testHasher.Hash("correct horse battery")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$"))

	ok, err := VerifyPassword("correct horse battery", hash)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = VerifyPassword("correct horse batterz", hash)
	require.NoError(t, err)
	assert.False(t, ok, "wrong password must not verify")
}

func TestPasswordHashUsesRandomSalt(t *testing.T) {
	a, err := testHasher.Hash("same password 1")
	require.NoError(t, err)
	b, err := testHasher.Hash("same password 1")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "two hashes of one password must differ")
}

func TestDefaultHasherParametersAreRecordedInHash(t *testing.T) {
	hash, err := DefaultPasswordHasher().Hash("eventmesh-demo-1")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$"), hash)

	ok, err := VerifyPassword("eventmesh-demo-1", hash)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	good, err := testHasher.Hash("some password")
	require.NoError(t, err)
	parts := strings.Split(good, "$")

	bad := []string{
		"",
		"plaintext",
		"$2a$10$abcdefghijklmnopqrstuv", // bcrypt
		strings.Replace(good, "argon2id", "argon2i", 1),
		strings.Replace(good, "v=19", "v=16", 1),
		strings.Join(append(append([]string{}, parts[:4]...), "!!!", parts[5]), "$"),
		strings.Join(append(append([]string{}, parts[:5]...), ""), "$"),
		good + "$extra",
	}
	for _, h := range bad {
		ok, err := VerifyPassword("some password", h)
		assert.False(t, ok)
		assert.ErrorIs(t, err, ErrInvalidHash, "%q", h)
	}
}

func TestValidatePassword(t *testing.T) {
	assert.Error(t, ValidatePassword("short"))
	assert.Error(t, ValidatePassword("123456789"))
	assert.NoError(t, ValidatePassword("1234567890"))
	assert.NoError(t, ValidatePassword(strings.Repeat("x", 256)))
	assert.Error(t, ValidatePassword(strings.Repeat("x", 257)))
}
