package ai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactExcerpt(t *testing.T) {
	cases := []struct{ name, in, mustNotContain string }{
		{"jwt", "got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcDEF123 back", "eyJhbGci"},
		{"bearer", "Authorization: Bearer abcdef1234567890", "abcdef1234567890"},
		{"stripe-style key", "key sk_live_abcdefgh12345678 rejected", "sk_live_abcdefgh12345678"},
		{"whsec", "secret whsec_ABCDEFGHIJKLMNOP", "whsec_ABCDEFGHIJKLMNOP"},
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123", "ghp_abcdef"},
		{"aws key", "AKIAABCDEFGHIJKLMNOP is invalid", "AKIAABCDEFGHIJKLMNOP"},
		{"kv password", "password=hunter2hunter2 failed", "hunter2hunter2"},
		{"json token", `{"token":"abc123xyz"}`, "abc123xyz"},
		{"email", "user bob.smith+x@example.com not found", "bob.smith"},
		{"card", "card 4242 4242 4242 4242 declined", "4242 4242"},
		{"card dashes", "4111-1111-1111-1111", "4111-1111"},
		{"opaque", "id 0123456789abcdef0123456789abcdef0123 bad", "0123456789abcdef0123456789abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactExcerpt(tc.in)
			require.NotContains(t, out, tc.mustNotContain)
			require.Contains(t, out, Redacted)
		})
	}
}

func TestRedactKeepsOrdinaryText(t *testing.T) {
	in := "HTTP 503 Service Unavailable: upstream timeout after 15s, order 12345"
	require.Equal(t, in, RedactExcerpt(in))
	// A digit run that is not Luhn-valid is not a card.
	require.Equal(t, "ref 1234 5678 9012 3456", RedactExcerpt("ref 1234 5678 9012 3456"))
}

func TestTruncate(t *testing.T) {
	require.Equal(t, "abc", Truncate("abcdef", 3))
	require.Equal(t, "abc", Truncate("abc", 10))
	require.Equal(t, "", Truncate("abc", 0))
}
