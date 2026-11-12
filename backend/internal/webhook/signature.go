// Package webhook builds, signs and sends the outgoing webhook requests.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Header names of an outgoing delivery.
const (
	HeaderEventID    = "X-EventMesh-Event-Id"
	HeaderEventType  = "X-EventMesh-Event-Type"
	HeaderDeliveryID = "X-EventMesh-Delivery-Id"
	HeaderAttempt    = "X-EventMesh-Attempt"
	HeaderTimestamp  = "X-EventMesh-Timestamp"
	HeaderSignature  = "X-EventMesh-Signature"

	UserAgent = "EventMesh/1.0"

	signatureVersion = "v1"

	// DefaultTolerance is how old a signed timestamp may be before a consumer
	// should reject the request as a possible replay.
	DefaultTolerance = 5 * time.Minute
)

// Verification errors. They are distinct so the receiver can report why a
// signature failed.
var (
	ErrSignatureFormat   = errors.New("signature header is malformed")
	ErrSignatureMismatch = errors.New("signature does not match")
	ErrTimestampStale    = errors.New("timestamp is outside the tolerance window")
)

// Sign returns the X-EventMesh-Signature value for a request:
// "v1=" + hex(HMAC-SHA256(secret, "<timestamp>.<body>")).
//
// The timestamp is part of the signed message so a captured request cannot be
// replayed later with a fresh timestamp. The body is signed as the exact
// bytes sent; consumers must verify against the raw body, not re-encoded JSON.
func Sign(secret string, timestamp int64, body []byte) string {
	return signatureVersion + "=" + hex.EncodeToString(computeMAC(secret, timestamp, body))
}

func computeMAC(secret string, timestamp int64, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return mac.Sum(nil)
}

// Verify is the consumer-side check, used by the local receiver and shipped
// as the reference implementation in docs/security.md. It recomputes the MAC
// over the raw body, compares in constant time, and rejects timestamps
// outside the tolerance window.
func Verify(secret, signatureHeader, timestampHeader string, body []byte, now time.Time, tolerance time.Duration) error {
	version, hexMAC, found := strings.Cut(signatureHeader, "=")
	if !found || version != signatureVersion {
		return ErrSignatureFormat
	}
	got, err := hex.DecodeString(hexMAC)
	if err != nil || len(got) != sha256.Size {
		return ErrSignatureFormat
	}
	timestamp, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return ErrSignatureFormat
	}

	// hmac.Equal is constant-time: comparing with == would leak, through
	// timing, how many leading bytes of a forged signature were correct.
	if !hmac.Equal(got, computeMAC(secret, timestamp, body)) {
		return ErrSignatureMismatch
	}

	// Check the age only after the MAC: an unauthenticated timestamp tells us nothing.
	age := now.Sub(time.Unix(timestamp, 0))
	if age > tolerance || age < -tolerance {
		return ErrTimestampStale
	}
	return nil
}
