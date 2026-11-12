package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/webhook"
)

// target is a webhook receiver that counts requests per delivery id.
type target struct {
	*httptest.Server
	mu     sync.Mutex
	hits   map[string]int
	status int
}

func newTarget(t *testing.T, status int) *target {
	tg := &target{hits: map[string]int{}, status: status}
	tg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.mu.Lock()
		tg.hits[r.Header.Get(webhook.HeaderDeliveryID)]++
		st := tg.status
		tg.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(tg.Close)
	return tg
}

func (tg *target) setStatus(s int) { tg.mu.Lock(); tg.status = s; tg.mu.Unlock() }
func (tg *target) total() int {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	n := 0
	for _, c := range tg.hits {
		n += c
	}
	return n
}

func TestEventIsDeliveredAndRecorded(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "payment.*")
	key := c.createKey("k")
	s.startWorker("w1", 3, nil)

	r := s.ingest(key, "payment.completed", map[string]any{"amount": 5})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	assert.EqualValues(t, 1, r.Body["deliveries"])

	s.waitStatus("delivered", 1, 10*time.Second)
	assert.Equal(t, 1, tg.total())

	var attempts int
	require.NoError(t, s.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM delivery_attempts WHERE response_status = 200 AND trigger = 'initial'").Scan(&attempts))
	assert.Equal(t, 1, attempts)
}

func TestNonMatchingEventHasNoDeliveries(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "payment.*")
	key := c.createKey("k")
	r := s.ingest(key, "user.created", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	assert.EqualValues(t, 0, r.Body["deliveries"])
}

func TestRetriesThenDeadLetterThenReplay(t *testing.T) {
	s := newStack(t, nil) // 2 retries => 3 attempts
	tg := newTarget(t, 500)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")
	s.startWorker("w1", 2, nil)

	require.Equal(t, http.StatusAccepted, s.ingest(key, "x.y", map[string]any{}).Status)
	s.waitStatus("dead_letter", 1, 15*time.Second)
	assert.Equal(t, 3, tg.total(), "one attempt plus two retries")

	var id string
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT id::text FROM deliveries").Scan(&id))
	tg.setStatus(200)
	r := c.do("POST", "/deliveries/"+id+"/replay", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	s.waitStatus("delivered", 1, 15*time.Second)

	var n int
	var trigger string
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT count(*) FROM delivery_attempts").Scan(&n))
	assert.Equal(t, 4, n, "old attempts are kept")
	require.NoError(t, s.pool.QueryRow(context.Background(),
		"SELECT trigger FROM delivery_attempts ORDER BY attempt_number DESC LIMIT 1").Scan(&trigger))
	assert.Equal(t, "replay", trigger)
}

func TestClientErrorGoesStraightToDeadLetter(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 400)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")
	s.startWorker("w1", 2, nil)
	s.ingest(key, "x.y", map[string]any{})
	s.waitStatus("dead_letter", 1, 10*time.Second)
	assert.Equal(t, 1, tg.total())
}
