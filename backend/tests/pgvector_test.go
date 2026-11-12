package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/ai"
	"eventmesh/internal/config"
)

// TestPgVectorDocsStore exercises the real pgvector store with the fake
// embedder. It is opt-in (EM_TEST_PGVECTOR=1) because pgvector is optional and
// the core suite must pass on a PostgreSQL without it; when opted in and the
// extension is missing, it fails loudly.
func TestPgVectorDocsStore(t *testing.T) {
	if os.Getenv("EM_TEST_PGVECTOR") == "" {
		t.Skip("EM_TEST_PGVECTOR not set: pgvector store NOT tested (needs the pgvector extension, see docs/windows-setup.md)")
	}
	pool, _ := newDatabase(t)
	ctx := context.Background()
	require.NoError(t, ai.EnsureDocsSchema(ctx, pool, ai.FakeDims))
	require.NoError(t, ai.EnsureDocsSchema(ctx, pool, ai.FakeDims), "schema creation is idempotent")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	write := func(p, body string) { require.NoError(t, os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)) }
	write("README.md", "# EventMesh\nA webhook delivery platform.\n")
	write("docs/reliability.md", "# Reliability\n\n## Retries\nA failed delivery is retried with exponential backoff and jitter. After the retry budget is spent the delivery moves to dead letter state.\n\n## Leases\nWorkers lease a delivery for sixty seconds before the reaper reclaims it.\n")
	write("docs/security.md", "# Security\n\n## Signatures\nEvery webhook carries an HMAC SHA256 signature header computed with the endpoint secret.\n")

	cfg := config.AI{
		Provider: "gemini", APIKey: "x", ChatModel: "c", EmbedModel: "e", MaxOutputTokens: 100, MaxInputChars: 8000,
		RequestsPerMin: 1000, FeatureDocs: true,
	}
	fake := &ai.Fake{}
	svc := ai.NewService(ai.Deps{Config: cfg, Provider: fake, Pool: pool, Docs: ai.NewPgVectorStore(pool)})

	st, err := svc.IndexDocs(ctx, root)
	require.NoError(t, err)
	require.Positive(t, st.Embedded)
	again, err := svc.IndexDocs(ctx, root)
	require.NoError(t, err)
	require.Zero(t, again.Embedded, "unchanged docs are not re-embedded")
	require.Equal(t, st.Embedded, again.Skipped)

	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM doc_chunks").Scan(&n))
	require.Equal(t, st.Embedded, n)

	hits, _, err := svc.Retrieve(ctx, uuid.Nil, "failed delivery retried with exponential backoff and jitter")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, "docs/reliability.md", hits[0].SourcePath)
	require.Equal(t, "Reliability > Retries", hits[0].HeadingPath)
	require.Greater(t, hits[0].Score, 0.5, "fake-embedder scale")

	// Removing a file deletes its chunks.
	require.NoError(t, os.Remove(filepath.Join(root, "docs", "security.md")))
	gone, err := svc.IndexDocs(ctx, root)
	require.NoError(t, err)
	require.Positive(t, gone.Deleted)
}
