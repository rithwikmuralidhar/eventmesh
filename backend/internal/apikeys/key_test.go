package apikeys

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateProducesParseableUniqueKeys(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k, err := Generate()
		require.NoError(t, err)

		prefix, err := Parse(k.Plaintext)
		require.NoError(t, err, "generated key must parse: %q", k.Plaintext)
		assert.Equal(t, k.Prefix, prefix)
		assert.True(t, strings.HasPrefix(k.Plaintext, "em_"+k.Prefix+"_"))
		assert.Len(t, k.Plaintext, len("em_")+8+1+43)
		assert.Equal(t, Hash(k.Plaintext), k.Hash)
		assert.Len(t, k.Hash, 32)

		require.False(t, seen[k.Plaintext], "duplicate key generated")
		seen[k.Plaintext] = true
	}
}

func TestHashIsDeterministicAndKeySpecific(t *testing.T) {
	a, err := Generate()
	require.NoError(t, err)
	b, err := Generate()
	require.NoError(t, err)

	assert.Equal(t, Hash(a.Plaintext), Hash(a.Plaintext))
	assert.NotEqual(t, Hash(a.Plaintext), Hash(b.Plaintext))
}

func TestParseRejectsMalformedKeys(t *testing.T) {
	valid, err := Generate()
	require.NoError(t, err)
	secret := strings.Repeat("a", 43)

	cases := map[string]string{
		"empty":             "",
		"wrong scheme":      "sk_abcdefgh_" + secret,
		"missing secret":    "em_abcdefgh",
		"short prefix":      "em_abc_" + secret,
		"long prefix":       "em_abcdefghi_" + secret,
		"short secret":      "em_abcdefgh_" + strings.Repeat("a", 42),
		"oversized secret":  "em_abcdefgh_" + strings.Repeat("a", 65),
		"bad chars prefix":  "em_abcd-fgh_" + secret,
		"bad chars secret":  "em_abcdefgh_" + strings.Repeat("a", 42) + "!",
		"extra segment":     valid.Plaintext + "_x",
		"bearer left on":    "Bearer " + valid.Plaintext,
		"whitespace suffix": valid.Plaintext + " ",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(in)
			assert.ErrorIs(t, err, ErrMalformedKey)
		})
	}
}
