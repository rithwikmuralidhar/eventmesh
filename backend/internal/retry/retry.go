// Package retry holds the two retry rules of the system, each exactly once:
// how a delivery attempt is classified, and how long to wait before the next.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"eventmesh/internal/ssrf"
)

// Error categories stored on attempts and deliveries. The dashboard turns
// them into plain-language explanations.
const (
	CategoryNetwork         = "network"
	CategoryTimeout         = "timeout"
	CategoryHTTPRetryable   = "http_retryable"
	CategoryHTTPRateLimited = "http_rate_limited"
	CategoryHTTPServerError = "http_server_error"
	CategoryHTTPRedirect    = "http_redirect"
	CategoryHTTPClientError = "http_client_error"
	CategoryBlocked         = "blocked_destination"
)

// Policy is the effective retry configuration for one endpoint: the system
// defaults with any per-endpoint overrides applied.
type Policy struct {
	// MaxRetries is the number of retries after the initial attempt, so a
	// delivery gets MaxRetries+1 attempts in total per cycle.
	MaxRetries   int
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

// Rand is the random source Backoff needs. *math/rand.Rand satisfies it;
// tests pass a deterministic implementation.
type Rand interface {
	// Int63n returns a uniform value in [0, n). n is always > 0.
	Int63n(n int64) int64
}

// Classification is the verdict on one attempt.
type Classification struct {
	Success bool
	// Category is empty on success.
	Category  string
	Retryable bool
	// HonorRetryAfter is true for the responses whose Retry-After header the
	// spec says to respect (429 and 503).
	HonorRetryAfter bool
}

// Classify maps the outcome of one HTTP attempt to a category and a retry
// decision. err is the transport error, if any; statusCode is used only when
// err is nil.
func Classify(statusCode int, err error) Classification {
	if err != nil {
		return classifyError(err)
	}
	switch {
	case statusCode >= 200 && statusCode <= 299:
		return Classification{Success: true}
	case statusCode == http.StatusRequestTimeout, statusCode == http.StatusTooEarly:
		return Classification{Category: CategoryHTTPRetryable, Retryable: true}
	case statusCode == http.StatusTooManyRequests:
		return Classification{Category: CategoryHTTPRateLimited, Retryable: true, HonorRetryAfter: true}
	case statusCode >= 500 && statusCode <= 599:
		return Classification{
			Category:        CategoryHTTPServerError,
			Retryable:       true,
			HonorRetryAfter: statusCode == http.StatusServiceUnavailable,
		}
	case statusCode >= 300 && statusCode <= 399:
		// Redirects are never followed: following one would let an endpoint
		// bounce the request to an address the SSRF policy never saw.
		return Classification{Category: CategoryHTTPRedirect}
	case statusCode >= 400 && statusCode <= 499:
		return Classification{Category: CategoryHTTPClientError}
	default:
		// 1xx or out-of-range codes: the endpoint is misbehaving. Treat it like
		// a client error, because retrying an invalid response will not fix it.
		return Classification{Category: CategoryHTTPClientError}
	}
}

func classifyError(err error) Classification {
	// Order matters: a blocked destination surfaces as a dial error, so it
	// must be recognised before the generic network case.
	if errors.Is(err, ssrf.ErrBlocked) {
		return Classification{Category: CategoryBlocked}
	}
	if isTimeout(err) {
		return Classification{Category: CategoryTimeout, Retryable: true}
	}
	return Classification{Category: CategoryNetwork, Retryable: true}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// Backoff returns the wait before retry number retryNumber (0 for the first
// retry) using exponential backoff with full jitter:
//
//	delay = random(0, min(MaxDelay, InitialDelay * 2^retryNumber))
//
// Full jitter spreads retries of many deliveries that failed together across
// the whole window, instead of having them all return at the same instant.
func Backoff(p Policy, retryNumber int, rnd Rand) time.Duration {
	ceiling := backoffCeiling(p, retryNumber)
	if ceiling <= 0 {
		return 0
	}
	// Int63n is exclusive of its argument; +1 makes the ceiling reachable.
	return time.Duration(rnd.Int63n(int64(ceiling) + 1))
}

// backoffCeiling is the upper bound of the jitter window. It doubles in a
// loop rather than shifting so that it cannot overflow for large retry numbers.
func backoffCeiling(p Policy, retryNumber int) time.Duration {
	ceiling := p.InitialDelay
	for i := 0; i < retryNumber && ceiling < p.MaxDelay; i++ {
		ceiling *= 2
	}
	if ceiling > p.MaxDelay {
		ceiling = p.MaxDelay
	}
	return ceiling
}

// ParseRetryAfter reads a Retry-After header, which is either a number of
// seconds or an HTTP date. It returns false for a missing or invalid value.
// A date in the past yields a zero delay.
func ParseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(header, 10, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		const maxSecs = int64(365 * 24 * 60 * 60) // keeps the multiplication from overflowing
		if secs > maxSecs {
			secs = maxSecs
		}
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(header); err == nil {
		if d := at.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// NextDelay combines backoff with an endpoint's Retry-After hint. The hint is
// used only when it is longer than the computed backoff, and it is capped at
// MaxDelay so an endpoint cannot park a delivery for a day.
func NextDelay(p Policy, retryNumber int, retryAfter time.Duration, honorRetryAfter bool, rnd Rand) time.Duration {
	delay := Backoff(p, retryNumber, rnd)
	if honorRetryAfter && retryAfter > delay {
		delay = retryAfter
		if delay > p.MaxDelay {
			delay = p.MaxDelay
		}
	}
	return delay
}

// ShouldRetry reports whether a failed attempt may be retried. cycleAttempts
// is the number of attempts made in the current cycle including the one that
// just failed. With MaxRetries = 5 the sixth attempt is the last: attempts
// 1..5 may be followed by a retry, attempt 6 may not.
func ShouldRetry(c Classification, cycleAttempts int, p Policy) bool {
	return !c.Success && c.Retryable && cycleAttempts <= p.MaxRetries
}

// LockedRand is a Rand safe for use by many goroutines: all workers share one.
type LockedRand struct {
	mu  sync.Mutex
	rnd *rand.Rand
}

// NewLockedRand returns a concurrency-safe random source. Jitter does not
// need cryptographic randomness, only an even spread.
func NewLockedRand(seed int64) *LockedRand {
	return &LockedRand{rnd: rand.New(rand.NewSource(seed))} //nolint:gosec // jitter, not security
}

// Int63n returns a uniform value in [0, n).
func (l *LockedRand) Int63n(n int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rnd.Int63n(n)
}
