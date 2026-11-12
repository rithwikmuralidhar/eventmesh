// Package events handles event ingestion and the event read API.
package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Limits on ingestion input.
const (
	MaxEventTypeLen      = 100
	MaxIdempotencyKeyLen = 255
	MaxPayloadBytes      = 256 * 1024
)

var eventTypeRE = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)

// ValidateEventType checks an event type such as "payment.completed".
func ValidateEventType(eventType string) error {
	if eventType == "" {
		return errors.New("event_type is required")
	}
	if len(eventType) > MaxEventTypeLen {
		return fmt.Errorf("event_type must be at most %d characters", MaxEventTypeLen)
	}
	if !eventTypeRE.MatchString(eventType) {
		return errors.New("event_type must be lowercase letters, digits and underscores in dot-separated segments")
	}
	return nil
}

// ValidatePattern checks a subscription pattern. A pattern is one of:
// "*" (everything), an exact event type, or a prefix wildcard "prefix.*".
func ValidatePattern(pattern string) error {
	if pattern == "*" {
		return nil
	}
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		if err := ValidateEventType(prefix); err != nil {
			return fmt.Errorf("pattern %q: prefix before \".*\" must be a valid event type", pattern)
		}
		return nil
	}
	if err := ValidateEventType(pattern); err != nil {
		return fmt.Errorf("pattern %q is not \"*\", an event type, or a prefix wildcard like \"payment.*\"", pattern)
	}
	return nil
}

// Matches reports whether a subscription pattern selects an event type.
// "payment.*" matches every type below "payment." at any depth, but not
// "payment" itself and not "payments.created".
func Matches(pattern, eventType string) bool {
	if pattern == "*" {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		return strings.HasPrefix(eventType, prefix+".")
	}
	return pattern == eventType
}

// MatchesAny reports whether any of an endpoint's patterns selects the type.
func MatchesAny(patterns []string, eventType string) bool {
	for _, p := range patterns {
		if Matches(p, eventType) {
			return true
		}
	}
	return false
}

// CanonicalPayload validates that raw is a JSON object and returns it in
// canonical form: object keys sorted, insignificant whitespace removed,
// numbers kept exactly as written. Two bodies that differ only in key order
// or spacing canonicalize to the same bytes.
func CanonicalPayload(raw json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep 2500 and 2500.0 distinct, and avoid float rounding of large integers
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errors.New("payload must be valid JSON")
	}
	if dec.More() {
		return nil, errors.New("payload must be a single JSON value")
	}
	if _, isObject := v.(map[string]any); !isObject {
		return nil, errors.New("payload must be a JSON object")
	}
	// encoding/json writes map keys in sorted order, which is what makes this canonical.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("canonicalize payload: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// RequestHash is the fingerprint stored with an event and compared when an
// idempotency key is reused: same key and same hash is a replay, same key and
// a different hash is a conflict.
func RequestHash(eventType string, canonicalPayload []byte) []byte {
	h := sha256.New()
	h.Write([]byte(eventType))
	h.Write([]byte{0}) // separator that cannot appear in an event type
	h.Write(canonicalPayload)
	return h.Sum(nil)
}
