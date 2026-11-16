-- Synthetic volume for reading query plans (docs/database.md). Run by scripts\explain-plans.ps1
-- against a scratch database, never the development one.
-- 20 users x 3 endpoints, 400,000 events, 1,200,000 deliveries spread over 30 days.
INSERT INTO users (id, email, password_hash)
SELECT uuidv7(), 'bench' || g || '@example.com', 'x' FROM generate_series(1, 20) g;

INSERT INTO webhook_endpoints (id, user_id, name, url, event_types, secret_ciphertext)
SELECT uuidv7(), u.id, 'ep' || n, 'https://example.com/hook', ARRAY['*'], '\x00'
FROM users u, generate_series(1, 3) n;

INSERT INTO events (id, user_id, event_type, payload, request_hash, created_at)
SELECT uuidv7(), u.id, (ARRAY['order.created','order.paid','payment.failed','user.signed_up'])[1 + g % 4],
       '{"n":1}', '\x00', now() - (g % 43200) * interval '1 minute'
FROM users u, generate_series(1, 20000) g;

-- 98.9% delivered, 1% dead letter, 0.1% pending and due an hour ago (what the reconciler looks for).
INSERT INTO deliveries (id, event_id, endpoint_id, user_id, status, attempt_count, cycle_attempt_count,
                        next_attempt_at, created_at, updated_at)
SELECT uuidv7(), e.id, ep.id, e.user_id,
       CASE WHEN r < 0.001 THEN 'pending' WHEN r < 0.011 THEN 'dead_letter' ELSE 'delivered' END,
       CASE WHEN r < 0.001 THEN 0 ELSE 1 END, CASE WHEN r < 0.001 THEN 0 ELSE 1 END,
       CASE WHEN r < 0.001 THEN now() - interval '1 hour' ELSE e.created_at END, e.created_at, e.created_at
FROM events e
JOIN webhook_endpoints ep ON ep.user_id = e.user_id
CROSS JOIN LATERAL (SELECT random() AS r WHERE e.id IS NOT NULL AND ep.id IS NOT NULL) x;

ANALYZE users; ANALYZE webhook_endpoints; ANALYZE events; ANALYZE deliveries;
