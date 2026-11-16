package tests

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
)

// oneDelivery ingests one event for one endpoint (no worker running) and
// returns the pending delivery's ID.
func oneDelivery(t *testing.T, s *stack) uuid.UUID {
	t.Helper()
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")
	require.Equal(t, http.StatusAccepted, s.ingest(key, "x.y", map[string]any{}).Status)
	var id uuid.UUID
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT id FROM deliveries").Scan(&id))
	return id
}

// The row claim is one UPDATE whose WHERE clause names the expected status.
// Under READ COMMITTED a second UPDATE on the same row waits for the first,
// then re-evaluates its WHERE clause against the committed row and matches
// nothing. This test races many claims, each on its own connection, released
// at the same instant.
func TestConcurrentClaimsOfOneDeliveryAdmitExactlyOne(t *testing.T) {
	s := newStack(t, nil)
	id := oneDelivery(t, s)
	q := db.New(s.pool)

	var level string
	require.NoError(t, s.pool.QueryRow(context.Background(), "SHOW transaction_isolation").Scan(&level))
	require.Equal(t, "read committed", level, "the design is documented against READ COMMITTED")

	const claimers = 32
	var won, lost atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			worker := "racer/" + uuid.NewString()[:8]
			<-start
			_, err := q.ClaimDelivery(context.Background(), db.ClaimDeliveryParams{WorkerID: &worker, LeaseSeconds: 60, ID: id})
			switch {
			case err == nil:
				won.Add(1)
			case database.IsNotFound(err):
				lost.Add(1)
			default:
				t.Errorf("claim %d: unexpected error: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, won.Load(), "exactly one claim may succeed")
	assert.EqualValues(t, claimers-1, lost.Load())

	var status string
	var lockedBy *string
	require.NoError(t, s.pool.QueryRow(context.Background(),
		"SELECT status, locked_by FROM deliveries WHERE id = $1", id).Scan(&status, &lockedBy))
	assert.Equal(t, "processing", status)
	require.NotNil(t, lockedBy)
}

// The same race, but with the first claim held open in an uncommitted
// transaction: the second claim must block on the row lock (not read the old
// 'pending' version and proceed) and must find nothing once the first commits.
func TestSecondClaimWaitsForTheFirstThenFindsNothing(t *testing.T) {
	s := newStack(t, nil)
	id := oneDelivery(t, s)
	ctx := context.Background()

	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	first := "first"
	_, err = db.New(tx).ClaimDelivery(ctx, db.ClaimDeliveryParams{WorkerID: &first, LeaseSeconds: 60, ID: id})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		second := "second"
		_, err := db.New(s.pool).ClaimDelivery(ctx, db.ClaimDeliveryParams{WorkerID: &second, LeaseSeconds: 60, ID: id})
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("second claim returned (%v) while the first transaction was still open; it should wait on the row lock", err)
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, tx.Commit(ctx))
	select {
	case err := <-done:
		assert.True(t, database.IsNotFound(err), "after the first commit the second claim must match zero rows, got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("second claim never returned")
	}
}

// And if the first claim rolls back, the waiting claim wins: the lock, not a
// stale read, decided the outcome.
func TestSecondClaimWinsWhenTheFirstRollsBack(t *testing.T) {
	s := newStack(t, nil)
	id := oneDelivery(t, s)
	ctx := context.Background()

	tx, err := s.pool.Begin(ctx)
	require.NoError(t, err)
	first := "first"
	_, err = db.New(tx).ClaimDelivery(ctx, db.ClaimDeliveryParams{WorkerID: &first, LeaseSeconds: 60, ID: id})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		second := "second"
		_, err := db.New(s.pool).ClaimDelivery(ctx, db.ClaimDeliveryParams{WorkerID: &second, LeaseSeconds: 60, ID: id})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, tx.Rollback(ctx))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("second claim never returned")
	}
	var lockedBy string
	require.NoError(t, s.pool.QueryRow(ctx, "SELECT locked_by FROM deliveries WHERE id = $1", id).Scan(&lockedBy))
	assert.Equal(t, "second", lockedBy)
}

// Fencing: a worker whose lease was taken over cannot record its result.
func TestStaleWorkerCannotOverwriteANewerClaim(t *testing.T) {
	s := newStack(t, nil)
	id := oneDelivery(t, s)
	ctx := context.Background()
	q := db.New(s.pool)

	old := "old-worker"
	_, err := q.ClaimDelivery(ctx, db.ClaimDeliveryParams{WorkerID: &old, LeaseSeconds: 60, ID: id})
	require.NoError(t, err)

	// The old worker stalls; its lease expires, the reaper resets the row and a new worker claims it.
	_, err = s.pool.Exec(ctx, "UPDATE deliveries SET lease_expires_at = now() - interval '1 second' WHERE id = $1", id)
	require.NoError(t, err)
	reaped, err := q.ReapExpiredDeliveries(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{id}, reaped)
	fresh := "new-worker"
	_, err = q.ClaimDelivery(ctx, db.ClaimDeliveryParams{WorkerID: &fresh, LeaseSeconds: 60, ID: id})
	require.NoError(t, err)

	// The old worker wakes up and tries to record "delivered".
	now := time.Now()
	_, err = q.FinishDelivery(ctx, db.FinishDeliveryParams{
		Status: "delivered", NextAttemptAt: now, LastAttemptAt: &now, ID: id, WorkerID: &old,
	})
	assert.True(t, database.IsNotFound(err), "the stale worker's write must match zero rows, got %v", err)

	var status, lockedBy string
	require.NoError(t, s.pool.QueryRow(ctx, "SELECT status, locked_by FROM deliveries WHERE id = $1", id).Scan(&status, &lockedBy))
	assert.Equal(t, "processing", status)
	assert.Equal(t, "new-worker", lockedBy)
}
