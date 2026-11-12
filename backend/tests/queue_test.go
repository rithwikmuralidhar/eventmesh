package tests

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/queue"
	"eventmesh/internal/ratelimit"
)

func TestQueueClaimMovesJobToProcessingAtomically(t *testing.T) {
	rdb, prefix := newRedis(t)
	q := queue.New(rdb, prefix)
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, "a", "b"))
	id, ok, err := q.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "a", id, "oldest job first")

	d, err := q.Depths(ctx)
	require.NoError(t, err)
	assert.Equal(t, queue.Depths{Ready: 1, Delayed: 0, Processing: 1}, d, "the claimed job is in processing, never in neither place")

	require.NoError(t, q.Ack(ctx, id))
	d, _ = q.Depths(ctx)
	assert.EqualValues(t, 0, d.Processing)
}

func TestQueueClaimOnEmptyQueueIsNotAnError(t *testing.T) {
	rdb, prefix := newRedis(t)
	_, ok, err := queue.New(rdb, prefix).Claim(context.Background(), time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestQueueManyClaimersGetEachJobExactlyOnce(t *testing.T) {
	rdb, prefix := newRedis(t)
	q := queue.New(rdb, prefix)
	ctx := context.Background()

	const jobs = 300
	ids := make([]string, jobs)
	for i := range ids {
		ids[i] = fmt.Sprintf("job-%d", i)
	}
	require.NoError(t, q.Enqueue(ctx, ids...))

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				id, ok, err := q.Claim(ctx, time.Minute)
				if err != nil || !ok {
					return
				}
				mu.Lock()
				seen[id]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.Len(t, seen, jobs)
	for id, n := range seen {
		assert.Equal(t, 1, n, "job %s claimed more than once", id)
	}
}

func TestQueueScheduledJobBecomesReadyOnlyWhenDue(t *testing.T) {
	rdb, prefix := newRedis(t)
	q := queue.New(rdb, prefix)
	ctx := context.Background()

	require.NoError(t, q.Schedule(ctx, "later", 300*time.Millisecond))
	n, err := q.PromoteDue(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "not due yet")

	require.Eventually(t, func() bool {
		n, err := q.PromoteDue(ctx, 100)
		return err == nil && n == 1
	}, 3*time.Second, 50*time.Millisecond)

	id, ok, err := q.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "later", id)
}

func TestQueueExpiredLeaseIsReturnedToReady(t *testing.T) {
	rdb, prefix := newRedis(t)
	q := queue.New(rdb, prefix)
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, "crashy"))
	_, ok, err := q.Claim(ctx, 100*time.Millisecond) // a worker claims, then "dies": never acks
	require.NoError(t, err)
	require.True(t, ok)

	n, _ := q.ReapExpired(ctx, 100)
	assert.Equal(t, 0, n, "lease still valid")

	require.Eventually(t, func() bool {
		n, err := q.ReapExpired(ctx, 100)
		return err == nil && n == 1
	}, 3*time.Second, 50*time.Millisecond)

	id, ok, err := q.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "crashy", id, "another worker can now take the abandoned job")
}

func TestQueueEnqueueClearsStaleDelayedEntry(t *testing.T) {
	rdb, prefix := newRedis(t)
	q := queue.New(rdb, prefix)
	ctx := context.Background()

	require.NoError(t, q.Schedule(ctx, "x", time.Hour))
	require.NoError(t, q.Enqueue(ctx, "x"))
	d, err := q.Depths(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 0, d.Delayed)
	assert.EqualValues(t, 1, d.Ready)
}

func TestRateLimiterNeverGrantsMoreThanBurstPlusRefill(t *testing.T) {
	rdb, prefix := newRedis(t)
	limiter := ratelimit.New(rdb, prefix)
	ctx := context.Background()

	const rate, burst = 10, 10
	start := time.Now()
	var granted int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				d, err := limiter.Allow(ctx, "bucket", rate, burst)
				if err == nil && d.Allowed {
					mu.Lock()
					granted++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	// Upper bound: the full bucket plus what could have refilled meanwhile.
	max := int64(burst) + int64(time.Since(start).Seconds()*rate) + 1
	assert.LessOrEqual(t, granted, max, "1000 concurrent attempts must not overshoot the limit")
	assert.GreaterOrEqual(t, granted, int64(burst), "the burst itself must be granted")
}

func TestRateLimiterReportsWaitWhenEmpty(t *testing.T) {
	rdb, prefix := newRedis(t)
	limiter := ratelimit.New(rdb, prefix)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		d, err := limiter.Allow(ctx, "k", 1, 2)
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}
	d, err := limiter.Allow(ctx, "k", 1, 2)
	require.NoError(t, err)
	assert.False(t, d.Allowed)
	assert.Greater(t, d.RetryAfter, time.Duration(0))
}
