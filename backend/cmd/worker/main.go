// Command worker runs the delivery worker pool and the scheduler, reaper and
// reconciler loops.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"eventmesh/internal/clock"
	"eventmesh/internal/config"
	"eventmesh/internal/database"
	"eventmesh/internal/endpoints"
	"eventmesh/internal/observability"
	"eventmesh/internal/queue"
	"eventmesh/internal/ratelimit"
	"eventmesh/internal/realtime"
	"eventmesh/internal/retry"
	"eventmesh/internal/ssrf"
	"eventmesh/internal/webhook"
	"eventmesh/internal/workers"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, "worker", slog.LevelInfo)
	if len(cfg.SSRFAllowedHosts) > 0 {
		logger.Warn("SSRF allowlist is active: webhook destinations on these hosts skip IP checks (development only)",
			"hosts", cfg.SSRFAllowedHosts)
	}

	// Ctrl+C (os.Interrupt) starts the graceful shutdown in Pool.Run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	shutdownTracing, err := observability.SetupTracing(ctx, "worker", observability.TracingConfig{
		Exporter: cfg.Tracing.Exporter, OTLPEndpoint: cfg.Tracing.OTLPEndpoint, SampleRatio: cfg.Tracing.SampleRatio,
	}, os.Stderr)
	if err != nil {
		return err
	}
	// Flush buffered spans on the way out, on a context that outlives ctx.
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(flushCtx)
	}()

	pool, err := database.Connect(ctx, cfg.DatabaseURL, cfg.PoolSize(true))
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

	cipher, err := endpoints.NewSecretCipher(cfg.SecretEncryptionKey)
	if err != nil {
		return err
	}
	// One HTTP connection per worker per host is the most a pool of this size
	// can use, so the transport is sized to the worker count.
	client := webhook.NewClient(ssrf.NewPolicy(cfg.SSRFAllowedHosts, nil), webhook.TimeoutsFor(cfg.DeliveryTimeout), cfg.WorkerCount)
	defer client.Close()

	metrics := observability.NewMetrics()
	pw, err := workers.NewPool(workers.Config{
		ProcessID: "w-" + uuid.NewString()[:8], WorkerCount: cfg.WorkerCount, Dispatchers: cfg.DispatcherCount,
		Lease:             cfg.LeaseDuration,
		SchedulerInterval: cfg.SchedulerInterval, ReaperInterval: cfg.ReaperInterval,
		ReconcilerInterval: cfg.ReconcilerInterval, ReconcilerGrace: cfg.ReconcilerGrace,
		ShutdownTimeout: cfg.ShutdownTimeout,
		DefaultPolicy: retry.Policy{
			MaxRetries: cfg.MaxRetries, InitialDelay: cfg.InitialRetryDelay, MaxDelay: cfg.MaxRetryDelay,
		},
	}, workers.Deps{
		DB: pool, Queue: queue.New(rdb, cfg.RedisKeyPrefix), Limiter: ratelimit.New(rdb, cfg.RedisKeyPrefix),
		Sender: client, Cipher: cipher, Publisher: realtime.NewRedisPublisher(rdb, cfg.RedisKeyPrefix),
		Metrics: metrics, Logger: logger, Clock: clock.System{}, Rand: retry.NewLockedRand(time.Now().UnixNano()),
	})
	if err != nil {
		return err
	}

	metricsSrv := &http.Server{Addr: cfg.WorkerMetricsAddr, ReadHeaderTimeout: 10 * time.Second}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	metricsSrv.Handler = mux

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return pw.Run(gctx) })
	g.Go(func() error {
		logger.Info("worker metrics listening", "addr", metricsSrv.Addr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("metrics listen: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return metricsSrv.Shutdown(sctx)
	})
	logger.Info("worker started", "workers", cfg.WorkerCount)
	return g.Wait()
}
