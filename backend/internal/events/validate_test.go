package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEventType(t *testing.T) {
	valid := []string{"payment", "payment.completed", "order.item_added", "a.b.c.d", "v2.user_1.created", strings.Repeat("a", 100)}
	for _, v := range valid {
		assert.NoError(t, ValidateEventType(v), v)
	}
	invalid := []string{
		"", "Payment.completed", "payment..completed", ".payment", "payment.", "payment completed",
		"payment-completed", "payment.*", "*", "payment/completed", "päyment", strings.Repeat("a", 101),
	}
	for _, v := range invalid {
		assert.Error(t, ValidateEventType(v), "%q should be rejected", v)
	}
}

func TestValidatePattern(t *testing.T) {
	valid := []string{"*", "payment.completed", "payment.*", "order.item.*", "payment"}
	for _, v := range valid {
		assert.NoError(t, ValidatePattern(v), v)
	}
	invalid := []string{"", "**", "*.completed", "payment.*.completed", "pay*", "payment.**", ".*", "Payment.*", "payment. *"}
	for _, v := range invalid {
		assert.Error(t, ValidatePattern(v), "%q should be rejected", v)
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		pattern, eventType string
		want               bool
	}{
		{"*", "payment.completed", true},
		{"*", "anything", true},
		{"payment.completed", "payment.completed", true},
		{"payment.completed", "payment.failed", false},
		{"payment.completed", "payment.completed.v2", false},
		{"payment.*", "payment.completed", true},
		{"payment.*", "payment.refund.created", true},
		{"payment.*", "payment", false},
		{"payment.*", "payments.created", false}, // prefix must end at a segment boundary
		{"payment.*", "order.payment.completed", false},
		{"order.item.*", "order.item.added", true},
		{"order.item.*", "order.created", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, Matches(tc.pattern, tc.eventType), "%s vs %s", tc.pattern, tc.eventType)
	}

	assert.True(t, MatchesAny([]string{"order.*", "payment.completed"}, "payment.completed"))
	assert.False(t, MatchesAny([]string{"order.*", "payment.completed"}, "payment.failed"))
	assert.False(t, MatchesAny(nil, "payment.failed"))
}

func TestCanonicalPayload(t *testing.T) {
	a, err := CanonicalPayload(json.RawMessage(`{"b": 1, "a": {"y": [1, 2], "x": "<tag>"}}`))
	require.NoError(t, err)
	b, err := CanonicalPayload(json.RawMessage("{\n\"a\":{\"x\":\"<tag>\",\"y\":[1,2]},\"b\":1}"))
	require.NoError(t, err)
	assert.Equal(t, string(a), string(b), "key order and whitespace must not matter")
	assert.Equal(t, `{"a":{"x":"<tag>","y":[1,2]},"b":1}`, string(a))

	// Large integers must survive exactly; float64 decoding would corrupt this one.
	big, err := CanonicalPayload(json.RawMessage(`{"id": 9007199254740993}`))
	require.NoError(t, err)
	assert.Equal(t, `{"id":9007199254740993}`, string(big))

	for _, bad := range []string{`[1,2]`, `"text"`, `42`, `null`, `true`, `{"a":`, ``, `{} {}`, `{"a":1} trailing`} {
		_, err := CanonicalPayload(json.RawMessage(bad))
		assert.Error(t, err, "%q should be rejected", bad)
	}
}

func TestRequestHash(t *testing.T) {
	p1, err := CanonicalPayload(json.RawMessage(`{"a":1,"b":2}`))
	require.NoError(t, err)
	p2, err := CanonicalPayload(json.RawMessage(`{ "b": 2, "a": 1 }`))
	require.NoError(t, err)
	p3, err := CanonicalPayload(json.RawMessage(`{"a":1,"b":3}`))
	require.NoError(t, err)

	assert.Equal(t, RequestHash("payment.completed", p1), RequestHash("payment.completed", p2))
	assert.NotEqual(t, RequestHash("payment.completed", p1), RequestHash("payment.completed", p3), "different payload")
	assert.NotEqual(t, RequestHash("payment.completed", p1), RequestHash("payment.failed", p1), "different type")
	assert.Len(t, RequestHash("payment.completed", p1), 32)
}
