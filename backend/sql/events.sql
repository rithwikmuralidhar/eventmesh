-- name: InsertEvent :one
-- ON CONFLICT DO NOTHING turns the idempotency race into "zero rows returned"
-- instead of an error that would abort the transaction. A concurrent insert
-- of the same key blocks here until the first transaction finishes, so the
-- loser can then read the winner's committed row.
INSERT INTO events (id, user_id, event_type, payload, idempotency_key, request_hash)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING id, created_at;

-- name: GetEventByIdempotencyKey :one
SELECT e.id, e.request_hash, e.created_at,
       (SELECT count(*) FROM deliveries d WHERE d.event_id = e.id)::int AS delivery_count
FROM events e
WHERE e.user_id = $1 AND e.idempotency_key = $2;

-- name: InsertDeliveries :exec
-- One statement for the whole fan-out: the two arrays are unnested in step.
INSERT INTO deliveries (id, event_id, endpoint_id, user_id)
SELECT unnest(sqlc.arg('ids')::uuid[]), sqlc.arg('event_id'), unnest(sqlc.arg('endpoint_ids')::uuid[]), sqlc.arg('user_id');

-- name: GetEvent :one
-- Event status is derived from the deliveries, never stored.
SELECT e.id, e.event_type, e.payload, e.idempotency_key, e.created_at,
       COALESCE(d.total, 0)::int AS delivery_count,
       (CASE
            WHEN COALESCE(d.total, 0) = 0 THEN 'no_subscribers'
            WHEN d.open_count > 0 THEN 'in_progress'
            WHEN d.delivered = d.total THEN 'delivered'
            ELSE 'failed'
        END)::text AS status
FROM events e
LEFT JOIN LATERAL (
    SELECT count(*) AS total,
           count(*) FILTER (WHERE dl.status IN ('pending', 'processing', 'retry_scheduled')) AS open_count,
           count(*) FILTER (WHERE dl.status = 'delivered') AS delivered
    FROM deliveries dl
    WHERE dl.event_id = e.id
) d ON true
WHERE e.id = $1 AND e.user_id = $2;

-- name: ListEvents :many
-- Keyset pagination on (created_at, id). The status filter is applied to the
-- derived status, so it is evaluated after the per-event aggregate.
SELECT t.id, t.event_type, t.idempotency_key, t.created_at, t.delivery_count, t.status
FROM (
    SELECT e.id, e.event_type, e.idempotency_key, e.created_at,
           COALESCE(d.total, 0)::int AS delivery_count,
           (CASE
                WHEN COALESCE(d.total, 0) = 0 THEN 'no_subscribers'
                WHEN d.open_count > 0 THEN 'in_progress'
                WHEN d.delivered = d.total THEN 'delivered'
                ELSE 'failed'
            END)::text AS status
    FROM events e
    LEFT JOIN LATERAL (
        SELECT count(*) AS total,
               count(*) FILTER (WHERE dl.status IN ('pending', 'processing', 'retry_scheduled')) AS open_count,
               count(*) FILTER (WHERE dl.status = 'delivered') AS delivered
        FROM deliveries dl
        WHERE dl.event_id = e.id
    ) d ON true
    WHERE e.user_id = sqlc.arg('user_id')
      AND (sqlc.narg('event_type')::text IS NULL OR e.event_type = sqlc.narg('event_type')::text)
      AND (sqlc.narg('created_from')::timestamptz IS NULL OR e.created_at >= sqlc.narg('created_from')::timestamptz)
      AND (sqlc.narg('created_to')::timestamptz IS NULL OR e.created_at < sqlc.narg('created_to')::timestamptz)
      AND (sqlc.narg('id_prefix')::text IS NULL OR e.id::text LIKE sqlc.narg('id_prefix')::text || '%')
      AND (
        sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (e.created_at, e.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
      )
) t
WHERE (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status')::text)
  AND (sqlc.narg('endpoint_id')::uuid IS NULL OR EXISTS (
    SELECT 1 FROM deliveries x WHERE x.event_id = t.id AND x.endpoint_id = sqlc.narg('endpoint_id')::uuid))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg('page_limit');
