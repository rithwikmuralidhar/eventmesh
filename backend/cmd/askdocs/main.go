// Command askdocs is a diagnostic for the docs Q&A feature: it prints the
// chunks retrieved for a question (with similarity scores and the threshold),
// then the model's answer and the sources the API would return. It writes no
// audit row and needs the same environment as the API (EM_AI_*, database).
//
//	go run ./cmd/askdocs "How are failed deliveries retried?"
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"eventmesh/internal/ai"
	"eventmesh/internal/config"
	"eventmesh/internal/database"
	"eventmesh/internal/ratelimit"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "askdocs:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: go run ./cmd/askdocs \"your question\"")
	}
	question := strings.Join(os.Args[1:], " ")
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if !cfg.AI.Configured() {
		return fmt.Errorf("AI is not configured: set EM_AI_PROVIDER=gemini and EM_AI_API_KEY")
	}
	cfg.AI.FeatureDocs = true
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pool, err := database.Connect(ctx, cfg.DatabaseURL, 2)
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
	// No Pool in Deps on purpose: the service then writes no audit rows.
	svc := ai.NewService(ai.Deps{
		Config: cfg.AI, Provider: g, Limiter: ratelimit.New(rdb, cfg.RedisKeyPrefix), Docs: ai.NewPgVectorStore(pool),
	})

	threshold := cfg.AI.MinSimilarity
	if threshold == 0 {
		threshold = ai.MinSimilarity
	}
	fmt.Printf("question: %s\nchat model: %s   embed model: %s   threshold: %.2f\n\n", question, cfg.AI.ChatModel, cfg.AI.EmbedModel, threshold)
	hits, took, err := svc.Retrieve(ctx, uuid.Nil, question)
	if err != nil {
		return err
	}
	fmt.Printf("retrieved %d chunks in %d ms:\n", len(hits), took.Milliseconds())
	for i, h := range hits {
		body := strings.Join(strings.Fields(h.Content), " ")
		if len(body) > 160 {
			body = body[:160] + "..."
		}
		fmt.Printf("  %d. score %.3f  %s > %s (lines %s)\n     %s\n", i+1, h.Score, h.SourcePath, h.HeadingPath, h.LineRange(), body)
	}
	if len(hits) == 0 || hits[0].Score < threshold {
		fmt.Println("\nbest score is below the threshold: the API answers \"Not found in the docs.\" WITHOUT calling the chat model.")
		return nil
	}
	ans, err := svc.AskDocs(ctx, uuid.Nil, question)
	if err != nil {
		return err
	}
	fmt.Printf("\nmodel answer:\n%s\n\nsources returned:\n", ans.Answer)
	if len(ans.Sources) == 0 {
		fmt.Println("  (none)")
	}
	for _, s := range ans.Sources {
		fmt.Printf("  %s > %s (lines %s)\n", s.SourcePath, s.HeadingPath, s.LineRange)
	}
	return nil
}
