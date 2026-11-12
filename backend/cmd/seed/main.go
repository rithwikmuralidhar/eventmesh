// Command seed creates the demo account, demo endpoints and demo events. It is
// idempotent: running it again changes nothing. With -demo-key it instead
// creates a fresh API key for the demo account and prints it once.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"

	"eventmesh/internal/apikeys"
	"eventmesh/internal/auth"
	"eventmesh/internal/clock"
	"eventmesh/internal/config"
	"eventmesh/internal/database"
	"eventmesh/internal/endpoints"
	"eventmesh/internal/events"
	"eventmesh/internal/observability"
	"eventmesh/internal/queue"
	"eventmesh/internal/realtime"
	"eventmesh/internal/ssrf"
)

const (
	demoEmail    = "demo@eventmesh.local"
	demoPassword = "eventmesh-demo-1"
)

func main() {
	demoKey := flag.Bool("demo-key", false, "create a fresh API key for the demo account and print it once")
	flag.Parse()
	if err := run(*demoKey); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(demoKey bool) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, cfg.DatabaseURL, 5)
	if err != nil {
		return err
	}
	defer pool.Close()
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("parse EM_REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opts)
	defer func() { _ = rdb.Close() }()

	authSvc, err := auth.NewService(pool, auth.DefaultPasswordHasher(), clock.System{}, cfg.SessionTTL)
	if err != nil {
		return err
	}
	user, err := authSvc.EnsureUser(ctx, demoEmail, demoPassword)
	if err != nil {
		return fmt.Errorf("ensure demo user: %w", err)
	}

	keys := apikeys.NewService(pool, clock.System{})
	if demoKey {
		_, plaintext, err := keys.Create(ctx, user.ID, "demo key")
		if err != nil {
			return err
		}
		fmt.Println("Demo API key (shown once):")
		fmt.Println(plaintext)
		return nil
	}

	cipher, err := endpoints.NewSecretCipher(cfg.SecretEncryptionKey)
	if err != nil {
		return err
	}
	publisher := realtime.NewRedisPublisher(rdb, cfg.RedisKeyPrefix)
	epSvc := endpoints.NewService(pool, cipher, ssrf.NewPolicy(cfg.SSRFAllowedHosts, nil), publisher)

	existing, err := epSvc.List(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		if cfg.ReceiverSecret == "" {
			return fmt.Errorf("EM_RECEIVER_SECRET must be set so demo endpoints share a secret with the receiver")
		}
		base := strings.TrimRight(cfg.ReceiverBaseURL, "/")
		one := int32(1)
		demo := []endpoints.CreateInput{
			{Name: "Healthy (always 200)", URL: base + "/hooks/ok", EventTypes: []string{"*"}},
			{Name: "Always failing (500)", URL: base + "/hooks/fail", EventTypes: []string{"payment.*"}},
			{Name: "Flaky (fails twice, then 200)", URL: base + "/hooks/flaky?fail_first=2", EventTypes: []string{"order.*"}},
			{Name: "Slow (2 s)", URL: base + "/hooks/slow?ms=2000", EventTypes: []string{"user.*"}},
			{Name: "Rate limited (1 per second)", URL: base + "/hooks/ok", EventTypes: []string{"*"}, RateLimitPerSecond: &one},
		}
		for _, in := range demo {
			if _, err := epSvc.CreateWithSecret(ctx, user.ID, in, cfg.ReceiverSecret); err != nil {
				return fmt.Errorf("create demo endpoint %q: %w", in.Name, err)
			}
		}
	}

	// Demo events go through the real ingestion path. Idempotency keys make a
	// second run a no-op instead of doubling the history.
	svc := events.NewService(pool, queue.New(rdb, cfg.RedisKeyPrefix), publisher, observability.NewMetrics())
	types := []string{"payment.completed", "payment.failed", "order.created", "order.shipped", "user.signed_up"}
	for i := 0; i < cfg.DemoEventCount; i++ {
		payload, _ := json.Marshal(map[string]any{"demo": true, "sequence": i, "amount": 1000 + i*25})
		if _, err := svc.Ingest(ctx, user.ID, events.IngestInput{
			EventType: types[i%len(types)], Payload: payload, IdempotencyKey: fmt.Sprintf("seed-%d", i),
		}); err != nil {
			return fmt.Errorf("ingest demo event %d: %w", i, err)
		}
	}
	fmt.Printf("Seed complete. Sign in as %s with the password printed in the README.\n", demoEmail)
	return nil
}
