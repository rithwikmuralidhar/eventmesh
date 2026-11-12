-- name: CreateEndpoint :one
INSERT INTO webhook_endpoints (
    id, user_id, name, url, event_types, secret_ciphertext, enabled,
    rate_limit_per_second, max_retries, initial_retry_delay_ms, max_retry_delay_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetEndpoint :one
SELECT * FROM webhook_endpoints
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: ListEndpoints :many
SELECT * FROM webhook_endpoints
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: ListEndpointStats :many
-- Per-endpoint delivery counts for the endpoint list, in one grouped scan.
SELECT
    endpoint_id,
    count(*)::bigint                                                                      AS total,
    count(*) FILTER (WHERE status = 'delivered')::bigint                                  AS delivered,
    count(*) FILTER (WHERE status = 'dead_letter')::bigint                                AS dead_letter,
    count(*) FILTER (WHERE status IN ('pending', 'processing', 'retry_scheduled'))::bigint AS in_progress,
    count(*) FILTER (WHERE status = 'cancelled')::bigint                                  AS cancelled
FROM deliveries
WHERE user_id = $1
GROUP BY endpoint_id;

-- name: UpdateEndpoint :one
-- The service loads the row, applies the PATCH and writes every column back.
UPDATE webhook_endpoints
SET name = $3,
    url = $4,
    event_types = $5,
    enabled = $6,
    rate_limit_per_second = $7,
    max_retries = $8,
    initial_retry_delay_ms = $9,
    max_retry_delay_ms = $10,
    updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateEndpointSecret :one
UPDATE webhook_endpoints
SET secret_ciphertext = $3, updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING id;

-- name: SoftDeleteEndpoint :one
UPDATE webhook_endpoints
SET deleted_at = now(), enabled = false, updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING id;

-- name: CancelOpenDeliveriesForEndpoint :many
-- State machine: pending | retry_scheduled -> cancelled. Rows that are being
-- processed right now are left alone; the worker cancels or finishes them.
UPDATE deliveries
SET status = 'cancelled', updated_at = now()
WHERE endpoint_id = $1 AND user_id = $2 AND status IN ('pending', 'retry_scheduled')
RETURNING id;

-- name: ListActiveEndpointsForUser :many
-- Read inside the ingestion transaction; pattern matching happens in Go so
-- the matching rule exists in exactly one place.
SELECT id, event_types
FROM webhook_endpoints
WHERE user_id = $1 AND enabled AND deleted_at IS NULL;
