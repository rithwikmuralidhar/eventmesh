// Command evaldocs measures retrieval over the indexed docs with
// eval/docs_questions.json and writes docs/ai-evaluation.md. It calls the
// embedding model once per question; it does not call the chat model.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/redis/go-redis/v9"

	"eventmesh/internal/ai"
	"eventmesh/internal/config"
	"eventmesh/internal/database"
	"eventmesh/internal/ratelimit"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "evaldocs:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if !cfg.AI.Configured() {
		return fmt.Errorf("AI is not configured: set EM_AI_PROVIDER=gemini and EM_AI_API_KEY (docs/ai-evaluation.md is not written)")
	}
	cfg.AI.FeatureDocs = true
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pool, err := database.Connect(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
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
	questionFile := filepath.Join("eval", "docs_questions.json")
	if v := os.Getenv("EM_EVAL_QUESTIONS"); v != "" {
		questionFile = v
	}
	qs, err := ai.LoadEvalQuestions(questionFile)
	if err != nil {
		return err
	}
	report, err := svc.EvaluateRetrieval(ctx, qs)
	if err != nil {
		return err
	}
	md := report.Markdown(time.Now(), cfg.AI.ChatModel, cfg.AI.EmbedModel, cfg.AI.EmbedDimensions)
	out := filepath.Join(cfg.AI.DocsDir, "docs", "ai-evaluation.md")
	if v := os.Getenv("EM_EVAL_OUTPUT"); v != "" {
		out = v
	}
	if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Print(md)
	fmt.Println("written to", out)
	return nil
}
