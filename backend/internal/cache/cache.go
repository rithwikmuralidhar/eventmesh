// Package cache is a small read-through cache on Redis for values that are
// expensive to compute and tolerate being a few seconds old.
//
// It is deliberately a TTL cache with no explicit invalidation. The one user
// today is the dashboard statistics, whose inputs change on every delivery
// attempt: invalidating on write would delete the entry thousands of times a
// second under load and cache nothing. A short TTL bounds staleness instead.
//
// Redis is an optimisation here, never a dependency: every Redis error is
// treated as a miss, so an outage makes the endpoint slower, not broken.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// Outcome says how a value was obtained; handlers expose it as X-Cache.
type Outcome string

// Outcomes.
const (
	Hit    Outcome = "HIT"
	Miss   Outcome = "MISS"
	Bypass Outcome = "BYPASS" // caching is turned off (TTL 0)
)

// Cache is a namespaced TTL cache.
type Cache struct {
	rdb    redis.Cmdable
	prefix string
	ttl    time.Duration
	group  singleflight.Group
}

// New returns a cache whose keys live under "<keyPrefix>:cache:". A TTL of
// zero (or a nil client) disables caching: Fetch then always computes.
func New(rdb redis.Cmdable, keyPrefix string, ttl time.Duration) *Cache {
	return &Cache{rdb: rdb, prefix: keyPrefix + ":cache:", ttl: ttl}
}

// Fetch returns the cached bytes for key, or runs compute, stores its result
// for the TTL and returns it.
//
// Concurrent misses for the same key in this process share one compute call
// (singleflight). Without that, the moment a popular entry expires every
// waiting request recomputes it at once: a cache stampede. Other API
// processes may still each compute once; that is bounded by the process
// count and not worth a distributed lock here.
func (c *Cache) Fetch(ctx context.Context, key string, compute func(context.Context) ([]byte, error)) ([]byte, Outcome, error) {
	if c == nil || c.rdb == nil || c.ttl <= 0 {
		b, err := compute(ctx)
		return b, Bypass, err
	}
	full := c.prefix + key
	if b, err := c.rdb.Get(ctx, full).Bytes(); err == nil {
		return b, Hit, nil
	} else if !errors.Is(err, redis.Nil) && ctx.Err() != nil {
		return nil, Miss, ctx.Err()
	}

	v, err, _ := c.group.Do(full, func() (any, error) {
		b, err := compute(ctx)
		if err != nil {
			return nil, err
		}
		// Best effort, and not tied to the request: a client that hangs up
		// must not stop the value being stored for the others.
		setCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = c.rdb.Set(setCtx, full, b, c.ttl).Err()
		return b, nil
	})
	if err != nil {
		return nil, Miss, err
	}
	return v.([]byte), Miss, nil
}
