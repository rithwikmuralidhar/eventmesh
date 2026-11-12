package webhook

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSignKnownVector(t *testing.T) {
	// Computed independently with:
	//   printf '1700000000.{"a":1}' | openssl dgst -sha256 -hmac 'whsec_test'
	got := Sign("whsec_test", 1700000000, []byte(`{"a":1}`))
	assert.Equal(t, "v1=38877139021993b830af32feea6e18a8da83eb2f6e49ee50bd9e4cf4ca4d3789", got)
}

func TestVerify(t *testing.T) {
	const secret = "whsec_correct"
	body := []byte(`{"event":"payment.completed","amount":2500}`)
	now := time.Unix(1_800_000_000, 0)
	ts := now.Unix()
	tsHeader := strconv.FormatInt(ts, 10)
	sig := Sign(secret, ts, body)

	cases := []struct {
		name      string
		secret    string
		signature string
		timestamp string
		body      []byte
		now       time.Time
		want      error
	}{
		{"valid", secret, sig, tsHeader, body, now, nil},
		{"valid at edge of tolerance", secret, sig, tsHeader, body, now.Add(5 * time.Minute), nil},
		{"tampered body", secret, sig, tsHeader, []byte(`{"event":"payment.completed","amount":9999}`), now, ErrSignatureMismatch},
		{"body with extra whitespace", secret, sig, tsHeader, append([]byte(" "), body...), now, ErrSignatureMismatch},
		{"wrong secret", "whsec_other", sig, tsHeader, body, now, ErrSignatureMismatch},
		{"timestamp changed to look fresh", secret, sig, strconv.FormatInt(ts+1, 10), body, now, ErrSignatureMismatch},
		{"stale timestamp", secret, sig, tsHeader, body, now.Add(5*time.Minute + time.Second), ErrTimestampStale},
		{"timestamp from the future", secret, sig, tsHeader, body, now.Add(-6 * time.Minute), ErrTimestampStale},
		{"missing version", secret, sig[3:], tsHeader, body, now, ErrSignatureFormat},
		{"unknown version", secret, "v2=" + sig[3:], tsHeader, body, now, ErrSignatureFormat},
		{"not hex", secret, "v1=zzzz", tsHeader, body, now, ErrSignatureFormat},
		{"truncated mac", secret, sig[:len(sig)-2], tsHeader, body, now, ErrSignatureFormat},
		{"empty signature", secret, "", tsHeader, body, now, ErrSignatureFormat},
		{"non-numeric timestamp", secret, sig, "yesterday", body, now, ErrSignatureFormat},
		{"empty timestamp", secret, sig, "", body, now, ErrSignatureFormat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(tc.secret, tc.signature, tc.timestamp, tc.body, tc.now, DefaultTolerance)
			if tc.want == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tc.want)
			}
		})
	}
}
