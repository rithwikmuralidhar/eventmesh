// Command indexdocs embeds the project documentation into the doc_chunks
// table (pgvector) for the AI docs feature. It is idempotent: chunks whose
// content hash is unchanged are skipped. Needs EM_AI_PROVIDER=gemini and
// EM_AI_API_KEY; sends the contents of README.md and docs/ to the provider.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/redis/go-redis/v9"

	"eventmesh/internal/ai"
	"eventmesh/internal/config"
	"eventmesh/internal/database"
	"eventmesh/internal/ratelimit"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "indexdocs:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if !cfg.AI.Configured() {
		return fmt.Errorf("AI is not configured: set EM_AI_PROVIDER=gemini and EM_AI_API_KEY")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pool, err := database.Connect(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := ai.EnsureDocsSchema(ctx, pool, cfg.AI.EmbedDimensions); err != nil {
		return err
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opts)
	defer func() { _ = rdb.Close() }()
	g, err := ai.NewGemini(ctx, cfg.AI.APIKey, cfg.AI.ChatModel, cfg.AI.EmbedModel, cfg.AI.EmbedDimensions)
	if err != nil {
		return err
	}
	svc := ai.NewService(ai.Deps{
		Config: cfg.AI, Provider: g, Limiter: ratelimit.New(rdb, cfg.RedisKeyPrefix), Pool: pool, Docs: ai.NewPgVectorStore(pool),
	})
	st, err := svc.IndexDocs(ctx, cfg.AI.DocsDir)
	if err != nil {
		return err
	}
	fmt.Printf("indexed %d files, %d chunks: %d embedded, %d unchanged, %d stale removed (embedding model %s)\n",
		st.Files, st.Chunks, st.Embedded, st.Skipped, st.Deleted, cfg.AI.EmbedModel)
	return nil
}
