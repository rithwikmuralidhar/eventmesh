-- Queries used only by the worker process. Each one names the status it
-- expects in its WHERE clause: that is how the state machine is enforced
-- under concurrency.

-- name: ClaimDelivery :one
-- The row claim. Redis decided who got the job quickly; this statement
-- decides who is actually allowed to work on it. Only one transaction can
-- move a row out of pending/retry_scheduled, so if two workers hold the same
-- delivery ID (a duplicate queue entry, a reaped lease, a reconciler
-- re-enqueue) exactly one of them gets a row back. The other sees zero rows
-- and drops the job. The joined event and endpoint come back in the same
-- round trip.
WITH claimed AS (
    UPDATE deliveries
    SET status = 'processing',
        locked_by = sqlc.arg('worker_id'),
        lease_expires_at = now() + make_interval(secs => sqlc.arg('lease_seconds')::double precision),
        updated_at = now()
    WHERE deliveries.id = sqlc.arg('id')
      AND deliveries.status IN ('pending', 'retry_scheduled')
      AND deliveries.next_attempt_at <= now()
    RETURNING id, event_id, endpoint_id, user_id, attempt_count, cycle_attempt_count, replay_count
)
SELECT c.id, c.event_id, c.endpoint_id, c.user_id, c.attempt_count, c.cycle_attempt_count, c.replay_count,
       e.event_type, e.payload, e.created_at AS event_created_at,
       ep.url, ep.secret_ciphertext, ep.enabled, ep.deleted_at, ep.rate_limit_per_second,
       ep.max_retries, ep.initial_retry_delay_ms, ep.max_retry_delay_ms
FROM claimed c
JOIN events e ON e.id = c.event_id
JOIN webhook_endpoints ep ON ep.id = c.endpoint_id;

-- name: GetDeliverySchedule :one
-- Looked up when a claim returns no row, to tell "already handled" from
-- "claimed slightly before it was due".
SELECT status, next_attempt_at FROM deliveries WHERE id = $1;

-- name: FinishDelivery :one
-- Records the outcome of an HTTP attempt: processing -> delivered |
-- retry_scheduled | dead_letter. "AND locked_by = worker" is the fencing
-- check: if this worker's lease expired and another worker took the row,
-- zero rows match and this worker must not overwrite the newer state.
UPDATE deliveries
SET status = sqlc.arg('status'),
    attempt_count = attempt_count + 1,
    cycle_attempt_count = cycle_attempt_count + 1,
    next_attempt_at = sqlc.arg('next_attempt_at'),
    lease_expires_at = NULL,
    locked_by = NULL,
    last_attempt_at = sqlc.arg('last_attempt_at'),
    last_response_status = sqlc.narg('last_response_status'),
    last_error_category = sqlc.narg('last_error_category'),
    last_error = sqlc.narg('last_error'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'processing'
  AND locked_by = sqlc.arg('worker_id')
  AND sqlc.arg('status')::text IN ('delivered', 'retry_scheduled', 'dead_letter')
RETURNING id;

-- name: InsertAttempt :exec
INSERT INTO delivery_attempts (
    id, delivery_id, attempt_number, "trigger", worker_id, started_at,
    response_status, response_body_excerpt, duration_ms, error_category, error_message
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: DeferDelivery :one
-- processing -> pending, used when the endpoint's rate limit has no token.
-- No attempt is recorded and no counter changes: being rate-limited is not a failure.
UPDATE deliveries
SET status = 'pending',
    next_attempt_at = sqlc.arg('next_attempt_at'),
    lease_expires_at = NULL,
    locked_by = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'processing' AND locked_by = sqlc.arg('worker_id')
RETURNING id;

-- name: CancelClaimedDelivery :one
-- processing -> cancelled, used when the worker finds the endpoint disabled or deleted.
UPDATE deliveries
SET status = 'cancelled',
    lease_expires_at = NULL,
    locked_by = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'processing' AND locked_by = sqlc.arg('worker_id')
RETURNING id;

-- name: ReapExpiredDeliveries :many
-- processing -> pending for rows whose lease ran out: the worker that held
-- them crashed or stalled. Safe to run from several workers at once, because
-- each row can only be updated by one of them.
UPDATE deliveries
SET status = 'pending',
    next_attempt_at = now(),
    lease_expires_at = NULL,
    locked_by = NULL,
    updated_at = now()
WHERE id IN (
    SELECT d.id FROM deliveries d
    WHERE d.status = 'processing' AND d.lease_expires_at < now()
    ORDER BY d.lease_expires_at
    LIMIT sqlc.arg('batch_size')
    FOR UPDATE SKIP LOCKED
)
RETURNING id;

-- name: ListDueDeliveries :many
-- The reconciler's query: everything PostgreSQL says is due but that has been
-- waiting longer than the grace period, which means the fast-path enqueue was
-- lost (API crash after commit, Redis flush, Redis outage).
SELECT id FROM deliveries
WHERE status IN ('pending', 'retry_scheduled')
  AND next_attempt_at <= now() - make_interval(secs => sqlc.arg('grace_seconds')::double precision)
ORDER BY next_attempt_at
LIMIT sqlc.arg('batch_size');
