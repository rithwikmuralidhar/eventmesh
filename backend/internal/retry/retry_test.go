package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/ssrf"
)

// fixedRand returns a chosen point of the jitter window: 0 = start, 1 = end.
type fixedRand struct{ fraction float64 }

func (f fixedRand) Int63n(n int64) int64 {
	v := int64(float64(n-1) * f.fraction)
	if v < 0 {
		return 0
	}
	return v
}

var defaultPolicy = Policy{MaxRetries: 5, InitialDelay: 2 * time.Second, MaxDelay: 5 * time.Minute}

func TestBackoffCeilingDoublesThenCaps(t *testing.T) {
	cases := []struct {
		retry int
		want  time.Duration
	}{
		{0, 2 * time.Second},
		{1, 4 * time.Second},
		{2, 8 * time.Second},
		{3, 16 * time.Second},
		{7, 256 * time.Second},
		{8, 5 * time.Minute}, // 512 s would exceed the cap
		{9, 5 * time.Minute},
		{62, 5 * time.Minute},
		{10_000, 5 * time.Minute}, // must not overflow
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("retry %d", tc.retry), func(t *testing.T) {
			// With the random source pinned to the top of the window, Backoff
			// returns exactly the ceiling.
			assert.Equal(t, tc.want, Backoff(defaultPolicy, tc.retry, fixedRand{1}))
			// Pinned to the bottom it returns zero: full jitter starts at 0.
			assert.Equal(t, time.Duration(0), Backoff(defaultPolicy, tc.retry, fixedRand{0}))
		})
	}
}

func TestBackoffStaysInsideWindowForRealRandomness(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	for retry := 0; retry < 12; retry++ {
		ceiling := backoffCeiling(defaultPolicy, retry)
		var maxSeen time.Duration
		for i := 0; i < 2000; i++ {
			d := Backoff(defaultPolicy, retry, rnd)
			require.GreaterOrEqual(t, d, time.Duration(0))
			require.LessOrEqual(t, d, ceiling)
			if d > maxSeen {
				maxSeen = d
			}
		}
		// Full jitter uses the whole window, not a narrow band near the top or bottom.
		assert.Greater(t, maxSeen, ceiling/2, "retry %d never produced a delay in the upper half", retry)
	}
}

func TestBackoffDegenerateConfig(t *testing.T) {
	assert.Equal(t, time.Duration(0), Backoff(Policy{InitialDelay: 0, MaxDelay: time.Minute}, 3, fixedRand{1}))
	// An initial delay above the cap is clamped to the cap.
	assert.Equal(t, time.Second, Backoff(Policy{InitialDelay: time.Minute, MaxDelay: time.Second}, 0, fixedRand{1}))
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		err             error
		success         bool
		category        string
		retryable       bool
		honorRetryAfter bool
	}{
		{name: "200", status: 200, success: true},
		{name: "201", status: 201, success: true},
		{name: "204", status: 204, success: true},
		{name: "299", status: 299, success: true},

		{name: "dns failure", err: &net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}, category: CategoryNetwork, retryable: true},
		{name: "connection refused", err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, category: CategoryNetwork, retryable: true},
		{name: "connection reset", err: &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, category: CategoryNetwork, retryable: true},
		{name: "tls error", err: errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), category: CategoryNetwork, retryable: true},
		{name: "unexpected eof", err: &url.Error{Op: "Post", URL: "http://x", Err: errors.New("EOF")}, category: CategoryNetwork, retryable: true},

		{name: "context deadline", err: context.DeadlineExceeded, category: CategoryTimeout, retryable: true},
		{name: "wrapped deadline", err: &url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}, category: CategoryTimeout, retryable: true},
		{name: "net timeout", err: &net.OpError{Op: "dial", Err: timeoutErr{}}, category: CategoryTimeout, retryable: true},
		{name: "dns timeout", err: &net.DNSError{Err: "timeout", IsTimeout: true}, category: CategoryTimeout, retryable: true},

		{name: "408", status: 408, category: CategoryHTTPRetryable, retryable: true},
		{name: "425", status: 425, category: CategoryHTTPRetryable, retryable: true},
		{name: "429", status: 429, category: CategoryHTTPRateLimited, retryable: true, honorRetryAfter: true},

		{name: "500", status: 500, category: CategoryHTTPServerError, retryable: true},
		{name: "502", status: 502, category: CategoryHTTPServerError, retryable: true},
		{name: "503", status: 503, category: CategoryHTTPServerError, retryable: true, honorRetryAfter: true},
		{name: "504", status: 504, category: CategoryHTTPServerError, retryable: true},
		{name: "599", status: 599, category: CategoryHTTPServerError, retryable: true},

		{name: "301", status: 301, category: CategoryHTTPRedirect},
		{name: "302", status: 302, category: CategoryHTTPRedirect},
		{name: "307", status: 307, category: CategoryHTTPRedirect},
		{name: "304", status: 304, category: CategoryHTTPRedirect},

		{name: "400", status: 400, category: CategoryHTTPClientError},
		{name: "401", status: 401, category: CategoryHTTPClientError},
		{name: "404", status: 404, category: CategoryHTTPClientError},
		{name: "410", status: 410, category: CategoryHTTPClientError},
		{name: "422", status: 422, category: CategoryHTTPClientError},

		{name: "blocked", err: fmt.Errorf("dial tcp: %w", ssrf.ErrBlocked), category: CategoryBlocked},
		{name: "blocked wrapped in url error", err: &url.Error{Op: "Post", URL: "http://x", Err: &net.OpError{Op: "dial", Err: fmt.Errorf("%w: 10.0.0.1", ssrf.ErrBlocked)}}, category: CategoryBlocked},

		{name: "invalid status 100", status: 100, category: CategoryHTTPClientError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.status, tc.err)
			assert.Equal(t, Classification{
				Success:         tc.success,
				Category:        tc.category,
				Retryable:       tc.retryable,
				HonorRetryAfter: tc.honorRetryAfter,
			}, got)
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		header string
		want   time.Duration
		ok     bool
	}{
		{"seconds", "120", 2 * time.Minute, true},
		{"zero seconds", "0", 0, true},
		{"padded", "  7 ", 7 * time.Second, true},
		{"http date in future", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{"http date in past", now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"rfc850 date", now.Add(30 * time.Second).Format(time.RFC850), 30 * time.Second, true},
		{"empty", "", 0, false},
		{"negative", "-5", 0, false},
		{"fraction", "1.5", 0, false},
		{"garbage", "soon", 0, false},
		{"huge does not overflow", "99999999999999", 365 * 24 * time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseRetryAfter(tc.header, now)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNextDelay(t *testing.T) {
	top := fixedRand{1} // backoff for retry 0 is exactly 2 s

	t.Run("no hint uses backoff", func(t *testing.T) {
		assert.Equal(t, 2*time.Second, NextDelay(defaultPolicy, 0, 0, false, top))
	})
	t.Run("longer hint wins", func(t *testing.T) {
		assert.Equal(t, 30*time.Second, NextDelay(defaultPolicy, 0, 30*time.Second, true, top))
	})
	t.Run("shorter hint is ignored", func(t *testing.T) {
		assert.Equal(t, 2*time.Second, NextDelay(defaultPolicy, 0, time.Second, true, top))
	})
	t.Run("hint is capped at max delay", func(t *testing.T) {
		assert.Equal(t, 5*time.Minute, NextDelay(defaultPolicy, 0, 24*time.Hour, true, top))
	})
	t.Run("hint ignored when the response type does not honor it", func(t *testing.T) {
		assert.Equal(t, 2*time.Second, NextDelay(defaultPolicy, 0, 30*time.Second, false, top))
	})
}

func TestShouldRetryOffByOne(t *testing.T) {
	retryable := Classification{Category: CategoryHTTPServerError, Retryable: true}

	// MaxRetries = 5 means six attempts in total: attempts 1..5 are followed by
	// a retry, attempt 6 is the last.
	for attempts := 1; attempts <= 5; attempts++ {
		assert.True(t, ShouldRetry(retryable, attempts, defaultPolicy), "attempt %d should be retried", attempts)
	}
	assert.False(t, ShouldRetry(retryable, 6, defaultPolicy), "attempt 6 is the last")
	assert.False(t, ShouldRetry(retryable, 7, defaultPolicy))

	assert.False(t, ShouldRetry(retryable, 1, Policy{MaxRetries: 0}), "MaxRetries 0 means a single attempt")
	assert.False(t, ShouldRetry(Classification{Category: CategoryHTTPClientError}, 1, defaultPolicy), "non-retryable")
	assert.False(t, ShouldRetry(Classification{Success: true}, 1, defaultPolicy), "success is never retried")
}
