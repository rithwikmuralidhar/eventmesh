// Package httpapi wires the HTTP routes of the API process.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"eventmesh/internal/ai"
	"eventmesh/internal/apikeys"
	"eventmesh/internal/audit"
	"eventmesh/internal/auth"
	"eventmesh/internal/cache"
	"eventmesh/internal/clock"
	"eventmesh/internal/config"
	"eventmesh/internal/database/db"
	"eventmesh/internal/deliveries"
	"eventmesh/internal/endpoints"
	"eventmesh/internal/events"
	"eventmesh/internal/httpx"
	"eventmesh/internal/middleware"
	"eventmesh/internal/observability"
	"eventmesh/internal/queue"
	"eventmesh/internal/ratelimit"
	"eventmesh/internal/realtime"
	"eventmesh/internal/ssrf"
)

// Deps are the collaborators the API needs.
type Deps struct {
	Config  config.Config
	Logger  *slog.Logger
	Pool    *pgxpool.Pool
	Redis   redis.UniversalClient
	Metrics *observability.Metrics
	Hasher  auth.PasswordHasher
	// Queue and Publisher default to Redis-backed implementations; tests
	// replace them (for example with an enqueuer that always fails).
	Queue     events.Enqueuer
	Publisher realtime.Publisher
	// AIProvider and AIDocs replace the Gemini client and the pgvector store
	// (tests inject fakes). Both are nil in production.
	AIProvider ai.Provider
	AIDocs     ai.DocStore
}

// New builds the Gin engine and the live-update hub. The caller runs the hub.
func New(d Deps) (*gin.Engine, *realtime.Hub, error) {
	cfg := d.Config
	gin.SetMode(gin.ReleaseMode)
	if d.Queue == nil {
		d.Queue = queue.New(d.Redis, cfg.RedisKeyPrefix)
	}
	if d.Publisher == nil {
		d.Publisher = realtime.NewRedisPublisher(d.Redis, cfg.RedisKeyPrefix)
	}
	clk := clock.System{}
	limiter := ratelimit.New(d.Redis, cfg.RedisKeyPrefix)
	hub := realtime.NewHub(d.Redis, cfg.RedisKeyPrefix)

	cipher, err := endpoints.NewSecretCipher(cfg.SecretEncryptionKey)
	if err != nil {
		return nil, nil, err
	}
	authService, err := auth.NewService(d.Pool, d.Hasher, clk, cfg.SessionTTL)
	if err != nil {
		return nil, nil, err
	}
	authH := auth.NewHandler(authService, limiter, cfg.CookieSecure, cfg.AuthRateLimit).
		WithRegistration(cfg.AllowRegistration)
	keysH := apikeys.NewHandler(apikeys.NewService(d.Pool, clk))
	policy := ssrf.NewPolicy(cfg.SSRFAllowedHosts, nil)
	endpointsH := endpoints.NewHandler(endpoints.NewService(d.Pool, cipher, policy, d.Publisher))
	eventsSvc := events.NewService(d.Pool, d.Queue, d.Publisher, d.Metrics)
	eventsH := events.NewHandler(eventsSvc, d.Pool)
	deliveriesH := deliveries.NewHandler(d.Pool, d.Queue, d.Publisher, cfg.MaxRetries).
		WithStatsCache(cache.New(d.Redis, cfg.RedisKeyPrefix, cfg.StatsCacheTTL))
	auditH := audit.NewHandler(d.Pool)
	aiSvc, err := buildAI(context.Background(), d, limiter)
	if err != nil {
		return nil, nil, err
	}
	aiH := ai.NewHandler(aiSvc, endpointOwner(db.New(d.Pool)))

	r := gin.New()
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, nil, err
	}
	r.Use(
		middleware.Tracing(),
		middleware.RequestContext(d.Logger, d.Metrics.ObserveHTTP),
		middleware.Recover(),
		middleware.SecurityHeaders(cfg.CookieSecure),
		middleware.CORS(cfg.CORSAllowedOrigins),
		middleware.RateLimit(limiter, middleware.RateLimitConfig{
			Scope: "api", Rate: cfg.APIRateLimit, Burst: cfg.APIRateBurst,
			Key: middleware.ByClientIP, OnLimited: d.Metrics.CountRateLimited,
		}),
	)

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", readyz(d.Pool, d.Redis))
	registerDocs(r)

	v1 := r.Group("/api/v1")
	v1.Use(middleware.BodyLimit(events.MaxIngestBodyBytes))

	// Ingestion: API key, not a cookie, so no CSRF check applies.
	v1.POST("/events", keysH.RequireKey(), middleware.RateLimit(limiter, middleware.RateLimitConfig{
		Scope: "ingest", Rate: cfg.IngestRateLimit, Burst: cfg.IngestRateBurst,
		Key: middleware.ByUser, OnLimited: d.Metrics.CountRateLimited,
	}), eventsH.Ingest)

	csrf := middleware.CSRF(cfg.CORSAllowedOrigins)
	v1.POST("/auth/register", csrf, authH.Register)
	v1.POST("/auth/login", csrf, authH.Login)

	s := v1.Group("", authH.RequireSession(), csrf)
	s.POST("/auth/logout", authH.Logout)
	s.GET("/auth/me", authH.Me)
	s.POST("/auth/change-password", authH.ChangePassword)

	s.GET("/events", eventsH.List)
	s.GET("/events/:id", eventsH.Get)
	s.GET("/deliveries", deliveriesH.List)
	s.GET("/deliveries/:id", deliveriesH.Get)
	s.POST("/deliveries/:id/replay", deliveriesH.Replay)
	s.POST("/deliveries/:id/explain", aiH.Explain)
	s.POST("/ai/docs/ask", aiH.AskDocs)
	s.POST("/ai/search/events", aiH.SearchEvents)

	s.POST("/endpoints", endpointsH.Create)
	s.GET("/endpoints", endpointsH.List)
	s.GET("/endpoints/:id", endpointsH.Get)
	s.PATCH("/endpoints/:id", endpointsH.Update)
	s.POST("/endpoints/:id/rotate-secret", endpointsH.RotateSecret)
	s.DELETE("/endpoints/:id", endpointsH.Delete)

	s.POST("/api-keys", keysH.Create)
	s.GET("/api-keys", keysH.List)
	s.DELETE("/api-keys/:id", keysH.Revoke)

	s.GET("/dashboard/stats", deliveriesH.Stats)
	s.GET("/settings", settings(cfg, aiSvc))
	s.GET("/audit-logs", auditH.List)
	s.GET("/stream", hub.Stream)

	if cfg.ServeFrontend {
		serveFrontend(r, cfg.FrontendDir)
	}
	return r, hub, nil
}

func readyz(pool *pgxpool.Pool, rdb redis.UniversalClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		status, code := gin.H{"postgres": "ok", "redis": "ok"}, http.StatusOK
		if err := pool.Ping(ctx); err != nil {
			status["postgres"], code = "unreachable", http.StatusServiceUnavailable
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			status["redis"], code = "unreachable", http.StatusServiceUnavailable
		}
		c.JSON(code, status)
	}
}

// settings reports the read-only system configuration the dashboard shows.
func settings(cfg config.Config, aiSvc *ai.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		avail := aiSvc.Available()
		initial, maxDelay := cfg.RetryMillis()
		c.JSON(http.StatusOK, gin.H{
			"retry": gin.H{
				"max_retries": cfg.MaxRetries, "initial_retry_delay_ms": initial,
				"max_retry_delay_ms": maxDelay, "delivery_timeout_ms": cfg.DeliveryTimeout.Milliseconds(),
			},
			"limits": gin.H{
				"max_payload_bytes": events.MaxPayloadBytes, "default_endpoint_rate_per_second": 10,
				"ingest_rate_per_second": cfg.IngestRateLimit, "api_rate_per_second": cfg.APIRateLimit,
			},
			"ai": gin.H{"docs": avail[ai.FeatureDocs], "explain": avail[ai.FeatureExplain], "search": avail[ai.FeatureSearch]},
		})
	}
}

// serveFrontend serves the built single-page app, falling back to index.html
// for client-side routes. API routes never fall back.
func serveFrontend(r *gin.Engine, dir string) {
	index := filepath.Join(dir, "index.html")
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/healthz" || p == "/readyz" {
			httpx.WriteError(c, httpx.NotFound("route"))
			return
		}
		clean := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(filepath.Clean("/"+p), "/")))
		if info, err := os.Stat(clean); err == nil && !info.IsDir() {
			c.File(clean)
			return
		}
		c.File(index)
	})
}
