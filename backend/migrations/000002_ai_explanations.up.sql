-- Milestone 11: cache of AI dead-letter explanations (24 hours, see internal/ai).
-- Plain PostgreSQL; the pgvector table for the docs feature is created at runtime
-- by internal/ai (EnsureDocsSchema), so the core migrations never need pgvector.
CREATE TABLE delivery_ai_explanations (
    delivery_id     uuid PRIMARY KEY REFERENCES deliveries (id) ON DELETE CASCADE,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    explanation     text        NOT NULL,
    model           text        NOT NULL,
    include_excerpt boolean     NOT NULL DEFAULT false,
    generated_at    timestamptz NOT NULL DEFAULT now()
);
