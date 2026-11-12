// Package ratelimit implements the single token-bucket limiter used for the
// global API limit, the per-user ingestion limit and the per-endpoint
// outgoing delivery limit.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"eventmesh/internal/observability"
)

// tokenBucketScript is the whole limiter. Running it as one Lua script makes
// read-refill-take atomic: Redis executes scripts one at a time, so two
// workers can never both see "one token left" and both take it.
//
// The time comes from Redis (TIME), not from the caller. If each worker used
// its own clock, a worker running a second fast would refill buckets early
// and push the real rate above the limit.
//
// KEYS[1]  bucket key
// ARGV[1]  refill rate, tokens per second
// ARGV[2]  burst, the bucket capacity
// Returns  {allowed (1/0), wait in milliseconds until a token is available}
var tokenBucketScript = redis.NewScript(`
local rate  = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])

local t = redis.call('TIME')
local now_ms = t[1] * 1000 + math.floor(t[2] / 1000)

local bucket = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(bucket[1])
local ts     = tonumber(bucket[2])
if tokens == nil or ts == nil then
  -- First use: the bucket starts full.
  tokens = burst
  ts = now_ms
end

-- Refill for the time that passed, never above capacity. Capping matters when
-- the limit was lowered: an old, larger balance must not survive.
local elapsed = math.max(0, now_ms - ts)
tokens = math.min(burst, tokens + elapsed * rate / 1000)

local allowed = 0
local wait_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  wait_ms = math.ceil((1 - tokens) * 1000 / rate)
end

redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'ts', tostring(now_ms))
-- An idle bucket refills completely after burst/rate seconds, so it can be
-- dropped then; a missing key and a full bucket are equivalent.
redis.call('PEXPIRE', KEYS[1], math.ceil(burst / rate * 1000) + 1000)
return {allowed, wait_ms}
`)

// Decision is the result of one limiter check.
type Decision struct {
	Allowed bool
	// RetryAfter is how long until a token will be available. Zero when allowed.
	RetryAfter time.Duration
}

// Limiter is a Redis-backed token bucket. The rate and burst are passed on
// every call, so a changed limit takes effect on the next check.
type Limiter struct {
	rdb    redis.Scripter
	prefix string
}

// New returns a limiter using the given Redis client; every bucket key is
// namespaced by keyPrefix.
func New(rdb redis.Scripter, keyPrefix string) *Limiter {
	return &Limiter{rdb: rdb, prefix: keyPrefix}
}

// Allow takes one token from the bucket named key if one is available.
func (l *Limiter) Allow(ctx context.Context, key string, ratePerSecond float64, burst int) (Decision, error) {
	if ratePerSecond <= 0 || burst <= 0 {
		return Decision{}, fmt.Errorf("rate limit %q: rate and burst must be positive", key)
	}
	res, err := tokenBucketScript.Run(ctx, l.rdb, []string{l.prefix + ":" + key}, ratePerSecond, burst).Int64Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("run token bucket script: %w", err)
	}
	if len(res) != 2 {
		return Decision{}, fmt.Errorf("token bucket script returned %d values, want 2", len(res))
	}
	return Decision{Allowed: res[0] == 1, RetryAfter: time.Duration(res[1]) * time.Millisecond}, nil
}

// Key builders keep the Redis key layout in one place.

// APIKey is the bucket for the global per-IP API limit.
func APIKey(ip string) string { return "rl:api:" + ip }

// IngestKey is the bucket for a user's ingestion limit.
func IngestKey(userID string) string { return "rl:ingest:" + userID }

// EndpointKey is the bucket for an endpoint's outgoing delivery limit.
func EndpointKey(endpointID string) string { return "rl:endpoint:" + endpointID }

// AuthKey is the bucket for login and registration attempts.
func AuthKey(kind, subject string) string { return "rl:auth:" + kind + ":" + subject }

// AllowOrFailOpen is Allow for the API-facing limits. If Redis cannot be
// reached the request is allowed and a warning is logged: rate limiting
// protects against overload, and refusing every request because the limiter
// is down would turn a Redis outage into a full API outage. The outgoing
// delivery limit does not use this; a worker without Redis has no queue anyway.
func (l *Limiter) AllowOrFailOpen(ctx context.Context, key string, ratePerSecond float64, burst int) Decision {
	decision, err := l.Allow(ctx, key, ratePerSecond, burst)
	if err != nil {
		observability.Logger(ctx).Warn("rate limiter unavailable, failing open", "error", err.Error())
		return Decision{Allowed: true}
	}
	return decision
}
