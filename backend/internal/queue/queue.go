// Package queue is the delivery job queue, built directly on Redis lists and
// sorted sets. A job is a delivery ID and nothing else.
//
// Three keys hold every job that Redis knows about:
//
//	<prefix>:queue:ready       list         IDs ready to be claimed, oldest first
//	<prefix>:queue:delayed     sorted set   IDs waiting for a due time (score = due, ms)
//	<prefix>:queue:processing  sorted set   IDs claimed by a worker (score = lease expiry, ms)
//
// Every operation that touches more than one key is a Lua script. Redis runs
// a script as a single uninterruptible step, so a job can never be observed
// halfway between two structures, and a crash cannot leave it in neither.
//
// All times come from the Redis server clock (TIME) inside the scripts, so
// workers with skewed clocks still agree on when a job is due.
//
// Redis is not the source of truth. Everything here can be rebuilt from
// PostgreSQL by the reconciler, and a duplicate entry is harmless because the
// worker's row claim in PostgreSQL decides who may actually process a job.
package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Key name suffixes; every key is "<prefix>:queue:<name>" so that several
// environments (and parallel integration-test runs) can share one Redis.
const (
	keyReady      = "queue:ready"
	keyDelayed    = "queue:delayed"
	keyProcessing = "queue:processing"
)

const nowMS = `
local t = redis.call('TIME')
local now_ms = t[1] * 1000 + math.floor(t[2] / 1000)
`

// enqueueScript makes jobs ready now.
// It also removes them from the delayed set: a replayed or reconciled job may
// still have an old delayed entry, and leaving it would produce a second,
// pointless wake-up later.
// KEYS: ready, delayed. ARGV: delivery IDs.
var enqueueScript = redis.NewScript(`
for i = 1, #ARGV do
  redis.call('ZREM', KEYS[2], ARGV[i])
  redis.call('RPUSH', KEYS[1], ARGV[i])
end
return #ARGV
`)

// scheduleScript parks a job until now + delay, and releases the worker's
// claim on it in the same step. Doing both atomically means a retry is never
// lost between "no longer processing" and "scheduled".
// KEYS: delayed, processing. ARGV: delivery ID, delay in ms.
var scheduleScript = redis.NewScript(nowMS + `
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('ZADD', KEYS[1], now_ms + tonumber(ARGV[2]), ARGV[1])
return 1
`)

// claimScript pops the oldest ready job and records it as processing with a
// lease expiry, in one step. If the worker dies right after this script
// returns, the job is still in the processing set and the reaper will return
// it to ready when the lease expires. With two separate commands (LPOP, then
// ZADD) a crash in between would lose the job from Redis.
// KEYS: ready, processing. ARGV: lease in ms. Returns the ID, or false if empty.
var claimScript = redis.NewScript(nowMS + `
local id = redis.call('LPOP', KEYS[1])
if not id then
  return false
end
redis.call('ZADD', KEYS[2], now_ms + tonumber(ARGV[1]), id)
return id
`)

// ackScript removes a finished job from the processing set.
// KEYS: processing. ARGV: delivery ID.
var ackScript = redis.NewScript(`
return redis.call('ZREM', KEYS[1], ARGV[1])
`)

// promoteScript moves jobs whose due time has passed from delayed to ready.
// It is safe to run from several workers at once: each job is removed from
// the sorted set by exactly one script execution.
// KEYS: delayed, ready. ARGV: batch size. Returns the number moved.
var promoteScript = redis.NewScript(nowMS + `
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms, 'LIMIT', 0, tonumber(ARGV[1]))
for i = 1, #ids do
  redis.call('ZREM', KEYS[1], ids[i])
  redis.call('RPUSH', KEYS[2], ids[i])
end
return #ids
`)

// reapScript returns jobs whose lease expired to the ready list: the worker
// that claimed them crashed, stalled, or was killed before it could ack.
// KEYS: processing, ready. ARGV: batch size. Returns the number moved.
var reapScript = redis.NewScript(nowMS + `
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms, 'LIMIT', 0, tonumber(ARGV[1]))
for i = 1, #ids do
  redis.call('ZREM', KEYS[1], ids[i])
  redis.call('RPUSH', KEYS[2], ids[i])
end
return #ids
`)

// Queue is the Redis-backed delivery queue.
type Queue struct {
	rdb                        redis.UniversalClient
	ready, delayed, processing string
}

// New returns a queue on the given Redis client, namespaced by keyPrefix.
func New(rdb redis.UniversalClient, keyPrefix string) *Queue {
	return &Queue{
		rdb:        rdb,
		ready:      keyPrefix + ":" + keyReady,
		delayed:    keyPrefix + ":" + keyDelayed,
		processing: keyPrefix + ":" + keyProcessing,
	}
}

// Keys returns the three Redis keys, for tests that simulate Redis data loss.
func (q *Queue) Keys() []string { return []string{q.ready, q.delayed, q.processing} }

// Enqueue makes the given deliveries ready for a worker.
func (q *Queue) Enqueue(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if err := enqueueScript.Run(ctx, q.rdb, []string{q.ready, q.delayed}, args...).Err(); err != nil {
		return fmt.Errorf("enqueue %d deliveries: %w", len(ids), err)
	}
	return nil
}

// Schedule parks a delivery until delay has passed and releases this worker's
// claim on it. Used for retries and rate-limit deferrals.
func (q *Queue) Schedule(ctx context.Context, id string, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	err := scheduleScript.Run(ctx, q.rdb, []string{q.delayed, q.processing}, id, delay.Milliseconds()).Err()
	if err != nil {
		return fmt.Errorf("schedule delivery %s: %w", id, err)
	}
	return nil
}

// Claim takes the oldest ready delivery and leases it for the given duration.
// It does not block: ok is false when the ready list is empty.
func (q *Queue) Claim(ctx context.Context, lease time.Duration) (id string, ok bool, err error) {
	res, err := claimScript.Run(ctx, q.rdb, []string{q.ready, q.processing}, lease.Milliseconds()).Result()
	if errors.Is(err, redis.Nil) {
		// A Lua "false" reaches the client as a nil reply: nothing to claim.
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("claim delivery: %w", err)
	}
	id, isString := res.(string)
	if !isString {
		return "", false, fmt.Errorf("claim delivery: unexpected reply type %T", res)
	}
	return id, true, nil
}

// Ack removes a delivery from the processing set once its outcome is recorded.
func (q *Queue) Ack(ctx context.Context, id string) error {
	if err := ackScript.Run(ctx, q.rdb, []string{q.processing}, id).Err(); err != nil {
		return fmt.Errorf("ack delivery %s: %w", id, err)
	}
	return nil
}

// PromoteDue moves up to batch due deliveries from delayed to ready.
func (q *Queue) PromoteDue(ctx context.Context, batch int) (int, error) {
	n, err := promoteScript.Run(ctx, q.rdb, []string{q.delayed, q.ready}, batch).Int()
	if err != nil {
		return 0, fmt.Errorf("promote due deliveries: %w", err)
	}
	return n, nil
}

// ReapExpired returns up to batch deliveries with expired leases to ready.
func (q *Queue) ReapExpired(ctx context.Context, batch int) (int, error) {
	n, err := reapScript.Run(ctx, q.rdb, []string{q.processing, q.ready}, batch).Int()
	if err != nil {
		return 0, fmt.Errorf("reap expired leases: %w", err)
	}
	return n, nil
}

// Depths is the size of each queue structure, for the queue-depth gauge.
type Depths struct {
	Ready      int64
	Delayed    int64
	Processing int64
}

// Depths reads the three sizes in one round trip. The values are a
// monitoring snapshot and need not be mutually consistent.
func (q *Queue) Depths(ctx context.Context) (Depths, error) {
	pipe := q.rdb.Pipeline()
	ready := pipe.LLen(ctx, q.ready)
	delayed := pipe.ZCard(ctx, q.delayed)
	processing := pipe.ZCard(ctx, q.processing)
	if _, err := pipe.Exec(ctx); err != nil {
		return Depths{}, fmt.Errorf("read queue depths: %w", err)
	}
	return Depths{Ready: ready.Val(), Delayed: delayed.Val(), Processing: processing.Val()}, nil
}
