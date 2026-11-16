package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/auth"
	"eventmesh/internal/clock"
	"eventmesh/internal/config"
	"eventmesh/internal/database"
	"eventmesh/internal/endpoints"
	"eventmesh/internal/httpapi"
	"eventmesh/internal/observability"
	"eventmesh/internal/queue"
	"eventmesh/internal/ratelimit"
	"eventmesh/internal/realtime"
	"eventmesh/internal/retry"
	"eventmesh/internal/ssrf"
	"eventmesh/internal/webhook"
	"eventmesh/internal/workers"
)

// stack is one isolated EventMesh: its own database, its own Redis prefix, an
// API behind httptest, and (when started) a worker pool.
type stack struct {
	t      *testing.T
	cfg    config.Config
	pool   *pgxpool.Pool
	rdb    *redis.Client
	prefix string
	api    *httptest.Server
	queue  *queue.Queue
	logs   *syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// newDatabase creates eventmesh_test_<random>, migrates it and drops it at the
// end of the test (unless EM_TEST_KEEP_DB=1).
func newDatabase(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	adminURL := requireEnv(t, "EM_TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err, "cannot reach PostgreSQL at EM_TEST_DATABASE_URL")
	name := "eventmesh_test_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)

	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.Path = "/" + name
	dbURL := u.String()
	require.NoError(t, database.MigrateUp(dbURL))
	pool, err := database.Connect(ctx, dbURL, 30)
	require.NoError(t, err)

	t.Cleanup(func() {
		pool.Close()
		if strings.TrimSpace(os.Getenv("EM_TEST_KEEP_DB")) != "1" {
			_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		}
		_ = admin.Close(ctx)
	})
	return pool, dbURL
}

type overrides map[string]string

func newStack(t *testing.T, o overrides, mods ...func(*httpapi.Deps)) *stack {
	t.Helper()
	pool, dbURL := newDatabase(t)
	rdb, prefix := newRedis(t)

	env := map[string]string{
		"EM_DATABASE_URL": dbURL, "EM_REDIS_KEY_PREFIX": prefix, "EM_SSRF_ALLOWED_HOSTS": "127.0.0.1",
		"EM_INITIAL_RETRY_DELAY": "20ms", "EM_MAX_RETRY_DELAY": "100ms", "EM_MAX_RETRIES": "2",
		"EM_DELIVERY_TIMEOUT": "2s", "EM_LEASE_DURATION": "4s", "EM_AUTH_RATE_LIMIT_PER_MINUTE": "1000",
		"EM_API_RATE_LIMIT": "10000", "EM_API_RATE_BURST": "10000", "EM_INGEST_RATE_LIMIT": "10000", "EM_INGEST_RATE_BURST": "10000",
		"EM_RECONCILER_GRACE": "300ms", "EM_RECONCILER_INTERVAL": "200ms", "EM_REAPER_INTERVAL": "200ms",
		"EM_SCHEDULER_INTERVAL": "20ms",
		"EM_STATS_CACHE_TTL":    "0", // statistics are read fresh unless a test opts in
	}
	for k, v := range o {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	require.NoError(t, err)

	logs := &syncBuf{}
	logger := observability.NewLogger(logs, "test", -4)
	deps := httpapi.Deps{
		Config: cfg, Logger: logger, Pool: pool, Redis: rdb, Metrics: observability.NewMetrics(),
		Hasher: auth.PasswordHasher{MemoryKiB: 8, Iterations: 1, Parallelism: 1},
	}
	for _, m := range mods {
		m(&deps)
	}
	engine, hub, err := httpapi.New(deps)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = hub.Run(ctx) }()
	srv := httptest.NewServer(engine)
	t.Cleanup(func() { srv.Close(); cancel() })
	return &stack{t: t, cfg: cfg, pool: pool, rdb: rdb, prefix: prefix, api: srv,
		queue: queue.New(rdb, prefix), logs: logs}
}

// startWorker runs a worker pool until the returned stop function is called.
func (s *stack) startWorker(id string, count int, sender workers.Sender) (stop func()) {
	s.t.Helper()
	cipher, err := endpoints.NewSecretCipher(s.cfg.SecretEncryptionKey)
	require.NoError(s.t, err)
	if sender == nil {
		sender = webhook.NewClient(ssrf.NewPolicy(s.cfg.SSRFAllowedHosts, nil), webhook.TimeoutsFor(s.cfg.DeliveryTimeout), count)
	}
	pw, err := workers.NewPool(workers.Config{
		ProcessID: id, WorkerCount: count, Lease: s.cfg.LeaseDuration, PollInterval: 10 * time.Millisecond,
		SchedulerInterval: s.cfg.SchedulerInterval, ReaperInterval: s.cfg.ReaperInterval,
		ReconcilerInterval: s.cfg.ReconcilerInterval, ReconcilerGrace: s.cfg.ReconcilerGrace, ShutdownTimeout: 5 * time.Second,
		DefaultPolicy: retry.Policy{MaxRetries: s.cfg.MaxRetries, InitialDelay: s.cfg.InitialRetryDelay, MaxDelay: s.cfg.MaxRetryDelay},
	}, workers.Deps{
		DB: s.pool, Queue: s.queue, Limiter: ratelimit.New(s.rdb, s.prefix), Sender: sender, Cipher: cipher,
		Publisher: realtime.NewRedisPublisher(s.rdb, s.prefix), Metrics: observability.NewMetrics(),
		Logger: observability.NewLogger(s.logs, "worker", -4), Clock: clock.System{}, Rand: retry.NewLockedRand(1),
	})
	require.NoError(s.t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = pw.Run(ctx) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	s.t.Cleanup(stop)
	return stop
}

// client is an HTTP client with a cookie jar, acting as one signed-in user.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (s *stack) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: s.t, base: s.api.URL + "/api/v1", http: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
}

type resp struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    []byte
}

func (c *client) do(method, path string, body any, headers ...string) resp {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(c.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, rd)
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.http.Do(req)
	require.NoError(c.t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func (c *client) register(email string) {
	c.t.Helper()
	r := c.do("POST", "/auth/register", map[string]string{"email": email, "password": "correct-horse-1"})
	require.Equal(c.t, http.StatusCreated, r.Status, string(r.Raw))
}

func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// createEndpoint creates an endpoint and returns its id and one-time secret.
func (c *client) createEndpoint(name, url string, types ...string) (id, secret string) {
	c.t.Helper()
	r := c.do("POST", "/endpoints", map[string]any{"name": name, "url": url, "event_types": types})
	require.Equal(c.t, http.StatusCreated, r.Status, string(r.Raw))
	ep, _ := r.Body["endpoint"].(map[string]any)
	return str(ep, "id"), str(r.Body, "secret")
}

func (c *client) createKey(name string) string {
	c.t.Helper()
	r := c.do("POST", "/api-keys", map[string]string{"name": name})
	require.Equal(c.t, http.StatusCreated, r.Status, string(r.Raw))
	return str(r.Body, "key")
}

// ingest submits an event with an API key (no cookie jar needed).
func (s *stack) ingest(key, eventType string, payload any, headers ...string) resp {
	s.t.Helper()
	c := &client{t: s.t, base: s.api.URL + "/api/v1", http: &http.Client{Timeout: 20 * time.Second}}
	h := append([]string{"Authorization", "Bearer " + key}, headers...)
	return c.do("POST", "/events", map[string]any{"event_type": eventType, "payload": payload}, h...)
}

func (s *stack) deliveryStatuses() map[string]int {
	rows, err := s.pool.Query(context.Background(), "SELECT status, count(*) FROM deliveries GROUP BY status")
	require.NoError(s.t, err)
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		require.NoError(s.t, rows.Scan(&st, &n))
		out[st] = n
	}
	return out
}

func (s *stack) waitStatus(status string, want int, d time.Duration) {
	s.t.Helper()
	require.Eventually(s.t, func() bool { return s.deliveryStatuses()[status] == want }, d, 25*time.Millisecond,
		"want %d deliveries in %q", want, status)
}
