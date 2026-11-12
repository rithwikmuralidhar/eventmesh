-- name: ListDeliveries :many
SELECT d.id, d.event_id, d.endpoint_id, d.status, d.attempt_count, d.replay_count,
       d.next_attempt_at, d.last_attempt_at, d.last_response_status, d.last_error_category,
       d.created_at, d.updated_at,
       e.event_type, ep.name AS endpoint_name
FROM deliveries d
JOIN events e ON e.id = d.event_id
JOIN webhook_endpoints ep ON ep.id = d.endpoint_id
WHERE d.user_id = sqlc.arg('user_id')
  AND (sqlc.narg('status')::text IS NULL OR d.status = sqlc.narg('status')::text)
  AND (sqlc.narg('endpoint_id')::uuid IS NULL OR d.endpoint_id = sqlc.narg('endpoint_id')::uuid)
  AND (sqlc.narg('event_id')::uuid IS NULL OR d.event_id = sqlc.narg('event_id')::uuid)
  AND (sqlc.narg('event_type')::text IS NULL OR e.event_type = sqlc.narg('event_type')::text)
  AND (sqlc.narg('created_from')::timestamptz IS NULL OR d.created_at >= sqlc.narg('created_from')::timestamptz)
  AND (sqlc.narg('created_to')::timestamptz IS NULL OR d.created_at < sqlc.narg('created_to')::timestamptz)
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (d.created_at, d.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY d.created_at DESC, d.id DESC
LIMIT sqlc.arg('page_limit');

-- name: GetDelivery :one
SELECT d.id, d.event_id, d.endpoint_id, d.status, d.attempt_count, d.cycle_attempt_count,
       d.replay_count, d.next_attempt_at, d.last_attempt_at, d.last_response_status,
       d.last_error_category, d.last_error, d.created_at, d.updated_at,
       e.event_type,
       ep.name AS endpoint_name, ep.url AS endpoint_url, ep.enabled AS endpoint_enabled,
       ep.deleted_at AS endpoint_deleted_at,
       ep.max_retries AS endpoint_max_retries
FROM deliveries d
JOIN events e ON e.id = d.event_id
JOIN webhook_endpoints ep ON ep.id = d.endpoint_id
WHERE d.id = $1 AND d.user_id = $2;

-- name: ListAttempts :many
-- Scoped through the delivery's owner, so attempts cannot be read across tenants.
SELECT a.id, a.attempt_number, a."trigger", a.worker_id, a.started_at, a.response_status,
       a.response_body_excerpt, a.duration_ms, a.error_category, a.error_message
FROM delivery_attempts a
JOIN deliveries d ON d.id = a.delivery_id
WHERE a.delivery_id = $1 AND d.user_id = $2
ORDER BY a.attempt_number;

-- name: ReplayDelivery :one
-- State machine: dead_letter -> pending. The status condition makes two
-- simultaneous replays result in one: the second UPDATE matches zero rows.
-- The EXISTS clause refuses a replay to a disabled or deleted endpoint.
UPDATE deliveries d
SET status = 'pending',
    cycle_attempt_count = 0,
    replay_count = d.replay_count + 1,
    next_attempt_at = now(),
    updated_at = now()
WHERE d.id = $1
  AND d.user_id = $2
  AND d.status = 'dead_letter'
  AND EXISTS (
    SELECT 1 FROM webhook_endpoints ep
    WHERE ep.id = d.endpoint_id AND ep.enabled AND ep.deleted_at IS NULL
  )
RETURNING d.id, d.event_id, d.endpoint_id, d.replay_count;

-- name: StatsTotals :one
SELECT
    (SELECT count(*) FROM events e
      WHERE e.user_id = sqlc.arg('user_id') AND e.created_at >= sqlc.arg('since'))::bigint AS total_events,
    count(*)::bigint                                                                        AS total_deliveries,
    count(*) FILTER (WHERE d.status = 'delivered')::bigint                                  AS delivered,
    count(*) FILTER (WHERE d.status = 'dead_letter')::bigint                                AS dead_letter,
    count(*) FILTER (WHERE d.status IN ('pending', 'processing', 'retry_scheduled'))::bigint AS pending,
    count(*) FILTER (WHERE d.status = 'cancelled')::bigint                                  AS cancelled
FROM deliveries d
WHERE d.user_id = sqlc.arg('user_id') AND d.created_at >= sqlc.arg('since');

-- name: StatsTimeSeries :many
-- bucket_unit is 'hour' or 'day'. Buckets are cut in UTC regardless of the
-- session time zone. Empty buckets are filled in by the service.
SELECT (date_trunc(sqlc.arg('bucket_unit')::text, d.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')::timestamptz AS bucket,
       count(*)::bigint                                                    AS total,
       count(*) FILTER (WHERE d.status = 'delivered')::bigint              AS delivered,
       count(*) FILTER (WHERE d.status = 'dead_letter')::bigint            AS dead_letter
FROM deliveries d
WHERE d.user_id = sqlc.arg('user_id') AND d.created_at >= sqlc.arg('since')
GROUP BY 1
ORDER BY 1;

-- name: RecentFailures :many
SELECT d.id, d.event_id, d.endpoint_id, d.last_error_category, d.last_response_status,
       d.attempt_count, d.last_attempt_at, d.created_at,
       e.event_type, ep.name AS endpoint_name
FROM deliveries d
JOIN events e ON e.id = d.event_id
JOIN webhook_endpoints ep ON ep.id = d.endpoint_id
WHERE d.user_id = sqlc.arg('user_id') AND d.status = 'dead_letter' AND d.created_at >= sqlc.arg('since')
ORDER BY d.created_at DESC
LIMIT sqlc.arg('row_limit');
