// Package config reads every setting from environment variables, once, at
// startup, into one typed struct. Invalid configuration stops the process
// with a message listing every problem.
package config

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Environment names.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
	EnvTest        = "test"
)

// DevSecretEncryptionKey is the built-in development key (base64 of 32
// bytes). It is public, so the process refuses to start with it in production.
const DevSecretEncryptionKey = "ZXZlbnRtZXNoLWRldi1vbmx5LWtleS0zMi1ieXRlcyE="

// Config is the full runtime configuration, shared by all binaries. Each
// binary uses the fields it needs.
type Config struct {
	Env string

	HTTPAddr          string
	MetricsAddr       string // API metrics listener, always on 127.0.0.1
	WorkerMetricsAddr string

	// RedisKeyPrefix namespaces every Redis key (queue, rate limits, pub/sub).
	RedisKeyPrefix string
	// ServeFrontend makes the API serve the built SPA from FrontendDir.
	ServeFrontend bool
	FrontendDir   string

	DatabaseURL string
	// DBMaxConns of 0 means "derive from the process": see PoolSize.
	DBMaxConns int
	RedisURL   string

	SecretEncryptionKey []byte
	CORSAllowedOrigins  []string
	CookieSecure        bool
	SessionTTL          time.Duration

	WorkerCount     int
	ShutdownTimeout time.Duration

	MaxRetries        int
	InitialRetryDelay time.Duration
	MaxRetryDelay     time.Duration
	DeliveryTimeout   time.Duration

	LeaseDuration      time.Duration
	SchedulerInterval  time.Duration
	ReaperInterval     time.Duration
	ReconcilerInterval time.Duration
	ReconcilerGrace    time.Duration

	SSRFAllowedHosts []string

	APIRateLimit    int
	APIRateBurst    int
	IngestRateLimit int
	IngestRateBurst int
	AuthRateLimit   int // attempts per minute, per IP and per email
	TrustedProxies  []string
	ReceiverAddr    string
	ReceiverSecret  string
	ReceiverBaseURL string
	APIBaseURL      string
	DemoEventCount  int
}

// Load reads the configuration through getenv (os.Getenv in production, a map
// lookup in tests). It reports every invalid value at once rather than
// stopping at the first.
func Load(getenv func(string) string) (Config, error) {
	// Every variable is read with the EM_ prefix.
	r := &reader{getenv: func(k string) string { return getenv("EM_" + k) }}

	cfg := Config{
		Env:               r.str("ENV", EnvDevelopment),
		HTTPAddr:          r.str("HTTP_ADDR", "127.0.0.1:8080"),
		MetricsAddr:       "127.0.0.1:" + strconv.Itoa(r.integer("METRICS_PORT", 9090, 1, 65535)),
		WorkerMetricsAddr: "127.0.0.1:" + strconv.Itoa(r.integer("WORKER_METRICS_PORT", 9091, 1, 65535)),
		RedisKeyPrefix:    r.str("REDIS_KEY_PREFIX", "em"),
		ServeFrontend:     r.boolean("SERVE_FRONTEND", false),
		FrontendDir:       r.str("FRONTEND_DIR", "../frontend/dist"),

		DatabaseURL: r.str("DATABASE_URL", "postgres://eventmesh:eventmesh@127.0.0.1:5432/eventmesh?sslmode=disable"),
		DBMaxConns:  r.integer("DB_MAX_CONNS", 0, 0, 1000),
		RedisURL:    r.str("REDIS_URL", "redis://127.0.0.1:6379/0"),

		CORSAllowedOrigins: r.list("CORS_ALLOWED_ORIGINS", "http://127.0.0.1:3000,http://localhost:3000"),
		SessionTTL:         r.duration("SESSION_TTL", 7*24*time.Hour, time.Minute, 90*24*time.Hour),

		WorkerCount:     r.integer("WORKER_COUNT", 10, 1, 1000),
		ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", 20*time.Second, time.Second, 10*time.Minute),

		MaxRetries:        r.integer("MAX_RETRIES", 5, 0, 20),
		InitialRetryDelay: r.duration("INITIAL_RETRY_DELAY", 2*time.Second, time.Millisecond, time.Hour),
		MaxRetryDelay:     r.duration("MAX_RETRY_DELAY", 5*time.Minute, time.Millisecond, 24*time.Hour),
		DeliveryTimeout:   r.duration("DELIVERY_TIMEOUT", 15*time.Second, 100*time.Millisecond, 5*time.Minute),

		LeaseDuration:      r.duration("LEASE_DURATION", 60*time.Second, 100*time.Millisecond, time.Hour),
		SchedulerInterval:  r.duration("SCHEDULER_INTERVAL", 500*time.Millisecond, 10*time.Millisecond, time.Minute),
		ReaperInterval:     r.duration("REAPER_INTERVAL", 10*time.Second, 10*time.Millisecond, 10*time.Minute),
		ReconcilerInterval: r.duration("RECONCILER_INTERVAL", 30*time.Second, 10*time.Millisecond, time.Hour),
		ReconcilerGrace:    r.duration("RECONCILER_GRACE", 30*time.Second, 0, time.Hour),

		SSRFAllowedHosts: r.list("SSRF_ALLOWED_HOSTS", ""),

		APIRateLimit:    r.integer("API_RATE_LIMIT", 100, 1, 1_000_000),
		APIRateBurst:    r.integer("API_RATE_BURST", 200, 1, 1_000_000),
		IngestRateLimit: r.integer("INGEST_RATE_LIMIT", 50, 1, 1_000_000),
		IngestRateBurst: r.integer("INGEST_RATE_BURST", 100, 1, 1_000_000),
		AuthRateLimit:   r.integer("AUTH_RATE_LIMIT_PER_MINUTE", 10, 1, 1_000_000),
		TrustedProxies:  r.list("TRUSTED_PROXIES", ""),

		ReceiverAddr:    r.str("RECEIVER_ADDR", "127.0.0.1:9000"),
		ReceiverSecret:  r.str("RECEIVER_SECRET", ""),
		ReceiverBaseURL: r.str("RECEIVER_BASE_URL", "http://127.0.0.1:9000"),
		APIBaseURL:      r.str("API_BASE_URL", "http://127.0.0.1:8080"),
		DemoEventCount:  r.integer("DEMO_EVENT_COUNT", 40, 0, 10_000),
	}

	cfg.CookieSecure = cfg.Env == EnvProduction // development runs over plain HTTP
	keyB64 := r.str("SECRET_ENCRYPTION_KEY", DevSecretEncryptionKey)
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		r.fail("SECRET_ENCRYPTION_KEY must be base64 of exactly 32 bytes (generate one with: openssl rand -base64 32)")
	}
	cfg.SecretEncryptionKey = key

	cfg.validate(r, keyB64)

	if len(r.problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(r.problems, "\n  - "))
	}
	return cfg, nil
}

func (c Config) validate(r *reader, keyB64 string) {
	switch c.Env {
	case EnvDevelopment, EnvProduction, EnvTest:
	default:
		r.fail(fmt.Sprintf("ENV must be %s, %s or %s", EnvDevelopment, EnvProduction, EnvTest))
	}
	if c.InitialRetryDelay > c.MaxRetryDelay {
		r.fail("INITIAL_RETRY_DELAY must not exceed MAX_RETRY_DELAY")
	}
	// If a lease could expire while its HTTP request is still in flight, a
	// second worker would send the same delivery concurrently.
	if c.LeaseDuration < 2*c.DeliveryTimeout {
		r.fail("LEASE_DURATION must be at least twice DELIVERY_TIMEOUT")
	}
	if c.APIRateBurst < c.APIRateLimit {
		r.fail("API_RATE_BURST must be at least API_RATE_LIMIT")
	}
	if c.IngestRateBurst < c.IngestRateLimit {
		r.fail("INGEST_RATE_BURST must be at least INGEST_RATE_LIMIT")
	}
	if c.Env == EnvProduction {
		if keyB64 == DevSecretEncryptionKey {
			r.fail("SECRET_ENCRYPTION_KEY is the public development default; set a real key in production")
		}
		if len(c.SSRFAllowedHosts) > 0 {
			r.fail("SSRF_ALLOWED_HOSTS must be empty in production")
		}
		if strings.Contains(c.DatabaseURL, "eventmesh:eventmesh@") {
			r.fail("DATABASE_URL uses the default development password; set real credentials in production")
		}
	}
}

// PoolSize returns the pgx pool size for a process. A worker process needs
// one connection per worker (each holds at most one at a time: claim, then
// record) plus a few for the background loops; the API gets a fixed pool.
func (c Config) PoolSize(isWorker bool) int32 {
	if c.DBMaxConns > 0 {
		return int32(c.DBMaxConns)
	}
	if isWorker {
		return int32(c.WorkerCount + 4)
	}
	return 20
}

// RetryMillis are the system retry defaults in the units the API reports.
func (c Config) RetryMillis() (initial, maxDelay int64) {
	return c.InitialRetryDelay.Milliseconds(), c.MaxRetryDelay.Milliseconds()
}

type reader struct {
	getenv   func(string) string
	problems []string
}

func (r *reader) fail(msg string) { r.problems = append(r.problems, msg) }

func (r *reader) str(key, def string) string {
	if v := strings.TrimSpace(r.getenv(key)); v != "" {
		return v
	}
	return def
}

func (r *reader) integer(key string, def, lo, hi int) int {
	raw := strings.TrimSpace(r.getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		r.fail(fmt.Sprintf("%s must be an integer between %d and %d, got %q", key, lo, hi, raw))
		return def
	}
	return v
}

func (r *reader) duration(key string, def, lo, hi time.Duration) time.Duration {
	raw := strings.TrimSpace(r.getenv(key))
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v < lo || v > hi {
		r.fail(fmt.Sprintf("%s must be a duration between %s and %s (for example \"2s\"), got %q", key, lo, hi, raw))
		return def
	}
	return v
}

func (r *reader) boolean(key string, def bool) bool {
	raw := strings.TrimSpace(r.getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		r.fail(fmt.Sprintf("%s must be true or false, got %q", key, raw))
		return def
	}
	return v
}

func (r *reader) list(key, def string) []string {
	raw := r.str(key, def)
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
