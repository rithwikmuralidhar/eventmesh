package tests

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/cache"
)

func totals(r resp) map[string]any {
	t, _ := r.Body["totals"].(map[string]any)
	return t
}

func TestStatsAreCachedForTheTTLThenRefreshed(t *testing.T) {
	s := newStack(t, overrides{"EM_STATS_CACHE_TTL": "1s"})
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")

	first := c.do("GET", "/dashboard/stats", nil)
	require.Equal(t, http.StatusOK, first.Status, string(first.Raw))
	assert.Equal(t, "MISS", first.Header.Get("X-Cache"))
	assert.EqualValues(t, 0, totals(first)["events"])

	// New data arrives; within the TTL the cached (stale) answer is served.
	require.Equal(t, http.StatusAccepted, s.ingest(key, "x.y", map[string]any{}).Status)
	second := c.do("GET", "/dashboard/stats", nil)
	assert.Equal(t, "HIT", second.Header.Get("X-Cache"))
	assert.Equal(t, string(first.Raw), string(second.Raw), "a hit returns the stored bytes")
	assert.EqualValues(t, 0, totals(second)["events"], "stale by design, for at most the TTL")

	// A different range is a different key.
	assert.Equal(t, "MISS", c.do("GET", "/dashboard/stats?range=7d", nil).Header.Get("X-Cache"))

	// After the TTL the entry is gone and the new event shows up.
	require.Eventually(t, func() bool {
		r := c.do("GET", "/dashboard/stats", nil)
		return r.Header.Get("X-Cache") == "MISS" && totals(r)["events"] == float64(1)
	}, 5*time.Second, 100*time.Millisecond)

	// The entry really has a TTL in Redis (it would otherwise live forever).
	keys, err := s.rdb.Keys(context.Background(), s.prefix+":cache:stats:*").Result()
	require.NoError(t, err)
	require.NotEmpty(t, keys)
	for _, k := range keys {
		ttl := s.rdb.PTTL(context.Background(), k).Val()
		assert.Greater(t, ttl, time.Duration(0), "key %s has no expiry", k)
		assert.LessOrEqual(t, ttl, time.Second)
	}
}

func TestStatsCacheIsPerUser(t *testing.T) {
	s := newStack(t, overrides{"EM_STATS_CACHE_TTL": "30s"})
	tg := newTarget(t, 200)
	a, b := s.newClient(), s.newClient()
	a.register("a@example.com")
	b.register("b@example.com")
	a.createEndpoint("hook", tg.URL, "*")
	key := a.createKey("k")
	require.Equal(t, http.StatusAccepted, s.ingest(key, "x.y", map[string]any{}).Status)

	ra := a.do("GET", "/dashboard/stats", nil)
	assert.EqualValues(t, 1, totals(ra)["events"])
	rb := b.do("GET", "/dashboard/stats", nil)
	assert.Equal(t, "MISS", rb.Header.Get("X-Cache"), "B must not be served A's cached entry")
	assert.EqualValues(t, 0, totals(rb)["events"])
}

func TestStatsCacheDisabledWithZeroTTL(t *testing.T) {
	s := newStack(t, nil) // the harness sets EM_STATS_CACHE_TTL=0
	c := s.newClient()
	c.register("a@example.com")
	assert.Equal(t, "BYPASS", c.do("GET", "/dashboard/stats", nil).Header.Get("X-Cache"))
	assert.Equal(t, "BYPASS", c.do("GET", "/dashboard/stats", nil).Header.Get("X-Cache"))
}

func TestCacheConcurrentMissesComputeOnce(t *testing.T) {
	rdb, prefix := newRedis(t)
	c := cache.New(rdb, prefix, 5*time.Second)
	var computes atomic.Int32
	release := make(chan struct{})
	compute := func(context.Context) ([]byte, error) {
		computes.Add(1)
		<-release // hold the first caller so the others pile up behind it
		return []byte("value"), nil
	}

	const callers = 20
	var wg sync.WaitGroup
	results := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, _, err := c.Fetch(context.Background(), "k", compute)
			if err == nil {
				results[i] = string(b)
			}
		}(i)
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, computes.Load(), "a stampede of misses must run the expensive work once")
	for _, r := range results {
		assert.Equal(t, "value", r)
	}
	_, outcome, err := c.Fetch(context.Background(), "k", compute)
	require.NoError(t, err)
	assert.Equal(t, cache.Hit, outcome)
}

func TestCacheFailsOpenWhenRedisIsDown(t *testing.T) {
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = dead.Close() })
	c := cache.New(dead, "x", 5*time.Second)
	b, outcome, err := c.Fetch(context.Background(), "k", func(context.Context) ([]byte, error) { return []byte("fresh"), nil })
	require.NoError(t, err, "a Redis outage must not fail the request")
	assert.Equal(t, "fresh", string(b))
	assert.Equal(t, cache.Miss, outcome)

	// A compute error is returned, and nothing is cached.
	boom := errors.New("boom")
	_, _, err = c.Fetch(context.Background(), "k2", func(context.Context) ([]byte, error) { return nil, boom })
	assert.ErrorIs(t, err, boom)
}
