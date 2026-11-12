-- name: CreateUser :one
INSERT INTO users (id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetSessionByTokenHash :one
-- Expired sessions are treated as absent.
SELECT s.id, s.user_id, s.expires_at, s.last_seen_at, u.email, u.created_at AS user_created_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > now();

-- name: TouchSession :exec
-- Sliding expiry: each use pushes the expiry out again.
UPDATE sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1 AND user_id = $2;

-- name: DeleteOtherSessions :exec
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE user_id = $1 AND expires_at <= now();

-- name: CreateAPIKey :one
INSERT INTO api_keys (id, user_id, name, key_prefix, key_hash)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, name, key_prefix, last_used_at, created_at, revoked_at;

-- name: ListAPIKeys :many
SELECT id, user_id, name, key_prefix, last_used_at, created_at, revoked_at
FROM api_keys
WHERE user_id = $1
ORDER BY created_at DESC, id DESC;

-- name: GetAPIKeyByHash :one
-- The only lookup that is not scoped by user: the key itself is the credential.
SELECT id, user_id, last_used_at, revoked_at FROM api_keys WHERE key_hash = $1;

-- name: RevokeAPIKey :one
-- COALESCE keeps the original revocation time, which makes revoking twice harmless.
UPDATE api_keys
SET revoked_at = COALESCE(revoked_at, now())
WHERE id = $1 AND user_id = $2
RETURNING id, user_id, name, key_prefix, last_used_at, created_at, revoked_at;

-- name: TouchAPIKeyLastUsed :exec
-- The time condition keeps ingestion from writing this row on every request.
UPDATE api_keys
SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: InsertAuditLog :exec
INSERT INTO audit_logs (id, user_id, action, resource_type, resource_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListAuditLogs :many
SELECT id, user_id, action, resource_type, resource_id, metadata, created_at
FROM audit_logs
WHERE user_id = sqlc.arg('user_id')
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit');
