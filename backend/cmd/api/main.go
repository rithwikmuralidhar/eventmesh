// Command api serves the HTTP API, the SSE stream and (optionally) the built frontend.
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

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"eventmesh/internal/auth"
	"eventmesh/internal/config"
	"eventmesh/internal/database"
	"eventmesh/internal/httpapi"
	"eventmesh/internal/observability"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, "api", slog.LevelInfo)
	if len(cfg.SSRFAllowedHosts) > 0 {
		logger.Warn("SSRF allowlist is active: webhook destinations on these hosts skip IP checks (development only)",
			"hosts", cfg.SSRFAllowedHosts)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	shutdownTracing, err := observability.SetupTracing(ctx, "api", observability.TracingConfig{
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

	pool, err := database.Connect(ctx, cfg.DatabaseURL, cfg.PoolSize(false))
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

	metrics := observability.NewMetrics()
	engine, hub, err := httpapi.New(httpapi.Deps{
		Config: cfg, Logger: logger, Pool: pool, Redis: rdb, Metrics: metrics,
		Hasher: auth.DefaultPasswordHasher(),
	})
	if err != nil {
		return err
	}

	apiSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: engine, ReadHeaderTimeout: 10 * time.Second}
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: metricsMux(metrics), ReadHeaderTimeout: 10 * time.Second}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return hub.Run(gctx) })
	g.Go(func() error { return serve(logger, apiSrv) })
	g.Go(func() error { return serve(logger, metricsSrv) })
	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = metricsSrv.Shutdown(shutdownCtx)
		return apiSrv.Shutdown(shutdownCtx)
	})
	return g.Wait()
}

func metricsMux(m *observability.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	return mux
}

func serve(logger *slog.Logger, srv *http.Server) error {
	logger.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}
	return nil
}
