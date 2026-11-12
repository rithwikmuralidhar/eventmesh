# Reliability

## The guarantee: at-least-once

Every (event, endpoint) pair is delivered **at least once**. It can be delivered more than once. Consumers deduplicate
on the `X-EventMesh-Delivery-Id` header, which is stable across retries and replays (replay resets the same delivery
row instead of creating a new one).

### Why not exactly-once

A worker sends the request; the endpoint receives it and acts on it; the worker crashes (or the response is lost)
before the result is written to PostgreSQL. After recovery the system sees a delivery that is "processing, lease
expired" and cannot distinguish "never sent" from "sent, not recorded". Sending again risks a duplicate; not sending
risks a loss. EventMesh chooses the duplicate. Removing the ambiguity needs the endpoint's cooperation (idempotent
handling), which is exactly what the delivery ID is for.

## The dual-write problem and the reconciler

Writing to PostgreSQL and enqueueing to Redis are two operations; either can fail alone. EventMesh makes PostgreSQL
the only source of truth and treats the Redis enqueue after commit as a fast path. The **reconciler** (worker loop,
`Pool.reconcile`) re-enqueues every delivery that is `pending`/`retry_scheduled`, due, and older than a grace period.
Duplicate queue entries are harmless because of the row claim below. Proven by integration tests: enqueue failure,
complete Redis key loss, and worker crash.

## Two layers against double processing

1. **Redis** (`queue.Claim`) decides quickly who gets a job: one Lua script pops from `ready` and adds to `processing`
   with a lease expiry.
2. **PostgreSQL** decides who may work on it: `ClaimDelivery` is an `UPDATE ... WHERE status IN ('pending','retry_scheduled')
   AND next_attempt_at <= now() RETURNING ...`. Only one transaction can move the row out of those states; a worker that
   gets zero rows drops the job.

### Leases and fencing

A claimed row has `locked_by` and `lease_expires_at` (default 60 s, comfortably above the 15 s request timeout; config
validation requires lease >= 2 x timeout). If a worker stalls past its lease, the reaper resets the row and another
worker claims it. The stalled worker must not overwrite the newer state, so `FinishDelivery` includes
`AND status = 'processing' AND locked_by = $worker`. Zero rows means the lease was lost: the result is discarded
(`Pool.record` logs and returns). `delivery_attempts` has a unique `(delivery_id, attempt_number)` as a second guard.

## Retry strategy

- `EM_MAX_RETRIES=5` means one initial attempt plus up to five retries: **six attempts**. Endpoints may override.
- Backoff is exponential with **full jitter**: `random(0, min(MAX, INITIAL * 2^retry))` (`retry.Backoff`, pure, injectable random source).
- `Retry-After` (seconds or HTTP date) is honoured for 429 and 503 when longer than the backoff, capped at the max delay.
- Classification (`retry.Classify`, one function, table-tested): 2xx success; network, timeout, 408/425, 429, 5xx retry;
  3xx, other 4xx and SSRF-blocked destinations go straight to dead letter. Redirects are never followed.

### Avoiding retry storms

Jitter spreads retries of deliveries that failed together; the per-endpoint token bucket caps outgoing volume even when
many retries come due; the retry cap bounds total work. A circuit breaker that auto-disables a persistently failing
endpoint is a future improvement, not built.

## Dead letter and replay

"Dead-letter queue" is a delivery **state** in PostgreSQL (`status = 'dead_letter'`), not a separate Redis structure. It
keeps its full attempt history. Replay (`deliveries.Handler.Replay`) is allowed only from `dead_letter` and only if the
endpoint exists and is enabled (else `409`). It sets `status = 'pending'`, `cycle_attempt_count = 0`, `replay_count + 1`
with the status condition in the `UPDATE` (two simultaneous replays cause one), enqueues, and writes an audit row.
Attempt numbers keep counting up; the first attempt after a replay has trigger `replay`.

## Failure scenarios

| Scenario | What happens | Why nothing is lost |
| --- | --- | --- |
| API crashes after commit, before enqueue | Event and deliveries exist in PostgreSQL | Reconciler enqueues them after the grace period |
| Worker crashes before sending | Row stays `processing` with a lease | Reaper resets it to `pending` and enqueues; another worker sends |
| Worker crashes after sending, before recording | Same as above | The delivery is sent again (at-least-once); consumer dedupes on delivery ID |
| Redis/Memurai restarts | Queue contents may be lost | Reconciler rebuilds `ready` from PostgreSQL; in-flight leases are recovered by the PostgreSQL reaper |
| Redis data loss | All three queue keys empty | Same: integration test `TestDeliveriesSurviveLossOfAllRedisData` |
| PostgreSQL unavailable | Workers cannot claim/record; API returns errors | Rows keep their last committed state; leases expire; work resumes when it returns. Ingestion fails loudly (no silent drop) |
| Endpoint hangs | Request times out (15 s total, 10 s to first byte) -> category `timeout`, retried | The lease (60 s) outlives the timeout so no second worker starts concurrently |
| Endpoint down for an hour | Retries back off to the 5 min cap; after six attempts the delivery is dead letter | Visible with its history; replay once the endpoint is back |
| Duplicate job in the queue | Second claimer's row claim returns zero rows | Dropped as stale; the delivery is sent once |
| Two workers claim one delivery | Only one `UPDATE` succeeds | The loser acks and moves on; fencing protects the result write |
| Worker stalls past its lease | Another worker takes over | Stalled worker's result is discarded by the `locked_by` check |

## What the tests prove

`backend/tests`: retries to dead letter and replay; 4xx straight to dead letter; 4 worker "processes" x 150 deliveries
with each sent exactly once; panicking worker recovered after lease expiry; Redis key loss; enqueue failure; rate-limited
deferral records no attempt and no retry count; disabled endpoint cancels; goroutine-leak check after graceful shutdown.
`-race` has not been run on the development machine (no C compiler) but is part of CI.
