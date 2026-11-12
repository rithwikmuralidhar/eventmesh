package tests

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"eventmesh/internal/httpapi"
	"eventmesh/internal/ssrf"
	"eventmesh/internal/webhook"
)

func (c *client) createFastEndpoint(url string, rate int, types ...string) string {
	c.t.Helper()
	r := c.do("POST", "/endpoints", map[string]any{"name": "e", "url": url, "event_types": types, "rate_limit_per_second": rate})
	require.Equal(c.t, http.StatusCreated, r.Status, string(r.Raw))
	ep, _ := r.Body["endpoint"].(map[string]any)
	return str(ep, "id")
}

func TestIdempotencyKeyCases(t *testing.T) {
	s := newStack(t, nil)
	c := s.newClient()
	c.register("a@example.com")
	key := c.createKey("k")

	first := s.ingest(key, "a.b", map[string]any{"n": 1}, "Idempotency-Key", "k1")
	require.Equal(t, http.StatusAccepted, first.Status, string(first.Raw))

	again := s.ingest(key, "a.b", map[string]any{"n": 1}, "Idempotency-Key", "k1")
	assert.Equal(t, http.StatusOK, again.Status)
	assert.Equal(t, "true", again.Header.Get("Idempotent-Replayed"))
	assert.Equal(t, first.Body["id"], again.Body["id"])

	conflict := s.ingest(key, "a.b", map[string]any{"n": 2}, "Idempotency-Key", "k1")
	assert.Equal(t, http.StatusConflict, conflict.Status)
	assert.Equal(t, "idempotency_key_conflict", str(conflict.Body["error"].(map[string]any), "code"))

	// Without a key every request is a new event.
	a, b := s.ingest(key, "a.b", map[string]any{}), s.ingest(key, "a.b", map[string]any{})
	assert.NotEqual(t, a.Body["id"], b.Body["id"])
}

func TestFiftyConcurrentRequestsWithOneKeyCreateOneEvent(t *testing.T) {
	s := newStack(t, nil)
	c := s.newClient()
	c.register("a@example.com")
	key := c.createKey("k")
	tg := newTarget(t, 200)
	c.createEndpoint("hook", tg.URL, "*")

	var wg sync.WaitGroup
	ids := make(chan any, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := s.ingest(key, "a.b", map[string]any{"n": 1}, "Idempotency-Key", "same")
			if r.Status == http.StatusAccepted || r.Status == http.StatusOK {
				ids <- r.Body["id"]
			}
		}()
	}
	wg.Wait()
	close(ids)
	unique := map[any]bool{}
	n := 0
	for id := range ids {
		unique[id] = true
		n++
	}
	assert.Equal(t, 50, n, "every request gets the winner's event back")
	assert.Len(t, unique, 1)

	var events, deliveries int
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT count(*) FROM events").Scan(&events))
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT count(*) FROM deliveries").Scan(&deliveries))
	assert.Equal(t, 1, events)
	assert.Equal(t, 1, deliveries)
}

func TestNoDoubleProcessingAcrossManyWorkers(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createFastEndpoint(tg.URL, 1000, "*")
	key := c.createKey("k")

	const events = 150
	for i := 0; i < events; i++ {
		require.Equal(t, http.StatusAccepted, s.ingest(key, "load.test", map[string]any{"i": i}).Status)
	}
	for i := 0; i < 4; i++ { // four worker "processes" competing for the same queue
		s.startWorker("w"+strconv.Itoa(i), 5, nil)
	}
	s.waitStatus("delivered", events, 30*time.Second)

	tg.mu.Lock()
	defer tg.mu.Unlock()
	assert.Len(t, tg.hits, events)
	for id, n := range tg.hits {
		assert.Equal(t, 1, n, "delivery %s was sent %d times", id, n)
	}
}

type panicSender struct{ started chan struct{} }

func (p panicSender) Send(context.Context, webhook.Request) webhook.Result {
	close(p.started)
	panic("worker crashed mid-delivery")
}

func TestCrashedWorkerDeliveryIsRecoveredAfterLeaseExpires(t *testing.T) {
	s := newStack(t, overrides{"EM_LEASE_DURATION": "1s", "EM_DELIVERY_TIMEOUT": "400ms"})
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")

	started := make(chan struct{})
	stopCrashy := s.startWorker("crashy", 1, panicSender{started})
	require.Equal(t, http.StatusAccepted, s.ingest(key, "x.y", map[string]any{}).Status)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first worker never claimed the delivery")
	}
	stopCrashy() // the process is gone; the row is still 'processing' with a lease
	assert.Equal(t, 1, s.deliveryStatuses()["processing"])

	s.startWorker("healthy", 1, nil)
	s.waitStatus("delivered", 1, 15*time.Second)
	assert.Equal(t, 1, tg.total())
}

func TestDeliveriesSurviveLossOfAllRedisData(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createFastEndpoint(tg.URL, 1000, "*")
	key := c.createKey("k")
	for i := 0; i < 20; i++ {
		s.ingest(key, "x.y", map[string]any{"i": i})
	}
	require.NoError(t, s.rdb.Del(context.Background(), s.queue.Keys()...).Err(), "simulated flush")

	s.startWorker("w", 4, nil)
	s.waitStatus("delivered", 20, 20*time.Second)
}

type failingQueue struct{}

func (failingQueue) Enqueue(context.Context, ...string) error { return errors.New("redis is down") }

func TestIngestionSucceedsWhenEnqueueFails(t *testing.T) {
	s := newStack(t, nil, func(d *httpapi.Deps) { d.Queue = failingQueue{} })
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")

	r := s.ingest(key, "x.y", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))

	s.startWorker("w", 2, nil) // the reconciler finds the delivery in PostgreSQL
	s.waitStatus("delivered", 1, 15*time.Second)
}

func TestRateLimitedDeliveriesAreDeferredWithoutAttemptsOrRetries(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createFastEndpoint(tg.URL, 1, "*") // 1 per second, burst 1
	key := c.createKey("k")
	for i := 0; i < 3; i++ {
		s.ingest(key, "x.y", map[string]any{"i": i})
	}
	s.startWorker("w", 3, nil)
	s.waitStatus("delivered", 3, 20*time.Second)

	var attempts, maxCount int
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT count(*) FROM delivery_attempts").Scan(&attempts))
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT max(attempt_count) FROM deliveries").Scan(&maxCount))
	assert.Equal(t, 3, attempts, "deferrals record no attempt")
	assert.Equal(t, 1, maxCount)
}

func TestDisabledEndpointDeliveriesAreCancelled(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	id, _ := c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")
	s.ingest(key, "x.y", map[string]any{})
	r := c.do("PATCH", "/endpoints/"+id, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	s.startWorker("w", 1, nil)
	s.waitStatus("cancelled", 1, 10*time.Second)
	assert.Equal(t, 0, tg.total())
}

func TestUserBCannotTouchUserAResources(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 500)
	a, b := s.newClient(), s.newClient()
	a.register("a@example.com")
	b.register("b@example.com")
	endpointID, _ := a.createEndpoint("hook", tg.URL, "*")
	keyA := a.createKey("ka")
	ev := s.ingest(keyA, "x.y", map[string]any{})
	eventID := str(ev.Body, "id")
	var deliveryID, apiKeyID string
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT id::text FROM deliveries").Scan(&deliveryID))
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT id::text FROM api_keys").Scan(&apiKeyID))

	for _, tc := range []struct{ method, path string }{
		{"GET", "/endpoints/" + endpointID}, {"PATCH", "/endpoints/" + endpointID},
		{"POST", "/endpoints/" + endpointID + "/rotate-secret"}, {"DELETE", "/endpoints/" + endpointID},
		{"GET", "/events/" + eventID}, {"GET", "/deliveries/" + deliveryID},
		{"POST", "/deliveries/" + deliveryID + "/replay"}, {"DELETE", "/api-keys/" + apiKeyID},
	} {
		var body any
		if tc.method == "PATCH" {
			body = map[string]any{"name": "hijacked"}
		}
		r := b.do(tc.method, tc.path, body)
		assert.Equal(t, http.StatusNotFound, r.Status, "%s %s: %s", tc.method, tc.path, r.Raw)
	}
	for _, list := range []string{"/endpoints", "/events", "/deliveries", "/api-keys"} {
		r := b.do("GET", list, nil)
		require.Equal(t, http.StatusOK, r.Status, list)
		for _, key := range []string{"items", "data"} {
			if items, ok := r.Body[key].([]any); ok {
				assert.Empty(t, items, "user B must not see user A's rows in %s", list)
			}
		}
	}
}

func TestAPIKeyAuthentication(t *testing.T) {
	s := newStack(t, nil)
	c := s.newClient()
	c.register("a@example.com")
	key := c.createKey("k")
	raw := &client{t: t, base: s.api.URL + "/api/v1", http: &http.Client{}}
	post := func(h ...string) int {
		return raw.do("POST", "/events", map[string]any{"event_type": "a.b", "payload": map[string]any{}}, h...).Status
	}

	assert.Equal(t, http.StatusAccepted, post("Authorization", "Bearer "+key))
	assert.Equal(t, http.StatusUnauthorized, post(), "missing")
	assert.Equal(t, http.StatusUnauthorized, post("Authorization", "Bearer not-a-key"), "malformed")
	assert.Equal(t, http.StatusUnauthorized, post("Authorization", "Basic abc"), "wrong scheme")

	var keyID string
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT id::text FROM api_keys").Scan(&keyID))
	require.Equal(t, http.StatusNoContent, c.do("DELETE", "/api-keys/"+keyID, nil).Status)
	assert.Equal(t, http.StatusUnauthorized, post("Authorization", "Bearer "+key), "revoked")
}

func TestSessionsAndCSRF(t *testing.T) {
	s := newStack(t, nil)
	c := s.newClient()
	c.register("a@example.com")
	assert.Equal(t, http.StatusOK, c.do("GET", "/auth/me", nil).Status)
	noHeader := c.do("POST", "/api-keys", map[string]string{"name": "x"}, "X-Requested-With", "")
	assert.Equal(t, http.StatusForbidden, noHeader.Status, "state-changing cookie request without the custom header")
	assert.Equal(t, http.StatusNoContent, c.do("POST", "/auth/logout", nil).Status)
	assert.Equal(t, http.StatusUnauthorized, c.do("GET", "/auth/me", nil).Status)

	bad := s.newClient().do("POST", "/auth/login", map[string]string{"email": "a@example.com", "password": "wrong-password-1"})
	unknown := s.newClient().do("POST", "/auth/login", map[string]string{"email": "nobody@example.com", "password": "wrong-password-1"})
	assert.Equal(t, http.StatusUnauthorized, bad.Status)
	assert.Equal(t, str(bad.Body["error"].(map[string]any), "message"), str(unknown.Body["error"].(map[string]any), "message"),
		"unknown email and wrong password are indistinguishable")
}

func TestPrivateDestinationIsRejectedUnlessAllowlisted(t *testing.T) {
	s := newStack(t, overrides{"EM_SSRF_ALLOWED_HOSTS": ""})
	c := s.newClient()
	c.register("a@example.com")
	r := c.do("POST", "/endpoints", map[string]any{"name": "x", "url": "http://127.0.0.1:9/hook", "event_types": []string{"*"}})
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, r.Status, string(r.Raw))
	r = c.do("POST", "/endpoints", map[string]any{"name": "x", "url": "http://169.254.169.254/latest", "event_types": []string{"*"}})
	assert.GreaterOrEqual(t, r.Status, 400, "cloud metadata address")
}

func TestGracefulShutdownFinishesInFlightWorkWithoutLeakingGoroutines(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")

	sender := webhook.NewClient(ssrf.NewPolicy(s.cfg.SSRFAllowedHosts, nil), webhook.TimeoutsFor(s.cfg.DeliveryTimeout), 2)
	baseline := goleak.IgnoreCurrent()
	stop := s.startWorker("w", 2, sender)
	s.ingest(key, "x.y", map[string]any{})
	s.waitStatus("delivered", 1, 10*time.Second)

	stop()
	sender.Close()
	tg.CloseClientConnections()
	goleak.VerifyNone(t, baseline, goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"))
}
