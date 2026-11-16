-- Query plans for docs/database.md. Each "without" case drops the index inside a transaction
-- and rolls back, so the database is unchanged afterwards.
\pset pager off
\set u '(SELECT id FROM users ORDER BY email LIMIT 1)'

\echo ==== 1. Delivery explorer, status filter: WITH deliveries_user_status_created_idx
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id, status, created_at FROM deliveries
WHERE user_id = :u AND status = 'dead_letter' ORDER BY created_at DESC LIMIT 26;

\echo ==== 1. Same query WITHOUT any (user_id, ...) index on deliveries
BEGIN;
DROP INDEX deliveries_user_status_created_idx;
DROP INDEX deliveries_user_created_idx;
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id, status, created_at FROM deliveries
WHERE user_id = :u AND status = 'dead_letter' ORDER BY created_at DESC LIMIT 26;
ROLLBACK;

\echo ==== 2. Event history, page 1 by keyset
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id, event_type, created_at FROM events
WHERE user_id = :u ORDER BY created_at DESC, id DESC LIMIT 26;

-- The cursor is what the client sends back from the previous page; fetch it first so the plan
-- below shows only the page query.
SELECT created_at AS cur_at, id AS cur_id FROM events WHERE user_id = :u
ORDER BY created_at DESC, id DESC OFFSET 15000 LIMIT 1 \gset
\echo ==== 2. Event history, a deep page by KEYSET (cursor = row 15,000)
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id, event_type, created_at FROM events
WHERE user_id = :u
  AND (created_at, id) < (:'cur_at'::timestamptz, :'cur_id'::uuid)
ORDER BY created_at DESC, id DESC LIMIT 26;

\echo ==== 2. The same deep page by OFFSET 15000
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id, event_type, created_at FROM events
WHERE user_id = :u ORDER BY created_at DESC, id DESC OFFSET 15000 LIMIT 26;

\echo ==== 3. Reconciler (ListDueDeliveries): WITH the partial index deliveries_due_idx
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id FROM deliveries
WHERE status IN ('pending', 'retry_scheduled') AND next_attempt_at <= now() - interval '30 seconds'
ORDER BY next_attempt_at LIMIT 500;

\echo ==== 3. Same query WITHOUT deliveries_due_idx
BEGIN;
DROP INDEX deliveries_due_idx;
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id FROM deliveries
WHERE status IN ('pending', 'retry_scheduled') AND next_attempt_at <= now() - interval '30 seconds'
ORDER BY next_attempt_at LIMIT 500;
ROLLBACK;

\echo ==== sizes
SELECT relname, pg_size_pretty(pg_relation_size(oid)) AS size, reltuples::bigint AS rows
FROM pg_class WHERE relname IN ('deliveries','events','deliveries_due_idx','deliveries_user_status_created_idx','events_user_created_idx')
ORDER BY relname;
