// Package workers runs the delivery engine: a bounded pool of workers fed by
// one dispatcher, plus the scheduler, reaper and reconciler loops.
package workers

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"eventmesh/internal/clock"
	"eventmesh/internal/database/db"
	"eventmesh/internal/endpoints"
	"eventmesh/internal/observability"
	"eventmesh/internal/queue"
	"eventmesh/internal/ratelimit"
	"eventmesh/internal/realtime"
	"eventmesh/internal/retry"
	"eventmesh/internal/webhook"
)

// Sender performs one delivery attempt. *webhook.Client implements it; tests
// substitute senders that block or panic to exercise crash recovery.
type Sender interface {
	Send(ctx context.Context, req webhook.Request) webhook.Result
}

// Config is the tuning of one worker process.
type Config struct {
	// ProcessID identifies this worker process in locked_by and in logs.
	ProcessID   string
	WorkerCount int
	// Lease is how long a claimed delivery is protected from other workers.
	// It must be comfortably longer than the delivery timeout: if a lease
	// could expire while its request is in flight, a second worker would
	// send the same delivery at the same time.
	Lease              time.Duration
	PollInterval       time.Duration
	SchedulerInterval  time.Duration
	ReaperInterval     time.Duration
	ReconcilerInterval time.Duration
	ReconcilerGrace    time.Duration
	ShutdownTimeout    time.Duration
	// DefaultPolicy is the system retry policy; endpoints may override it.
	DefaultPolicy retry.Policy
}

// Deps are the collaborators of the pool.
type Deps struct {
	DB        *pgxpool.Pool
	Queue     *queue.Queue
	Limiter   *ratelimit.Limiter
	Sender    Sender
	Cipher    *endpoints.SecretCipher
	Publisher realtime.Publisher
	Metrics   *observability.Metrics
	Logger    *slog.Logger
	Clock     clock.Clock
	Rand      retry.Rand
}

// Pool is the delivery worker pool.
type Pool struct {
	cfg     Config
	deps    Deps
	queries *db.Queries
}

const (
	loopBatchSize       = 500
	queueDepthInterval  = 5 * time.Second
	defaultPollInterval = 100 * time.Millisecond
)

// NewPool validates the configuration and returns a pool.
func NewPool(cfg Config, deps Deps) (*Pool, error) {
	if cfg.WorkerCount < 1 {
		return nil, fmt.Errorf("worker count must be at least 1, got %d", cfg.WorkerCount)
	}
	if cfg.Lease <= 0 {
		return nil, fmt.Errorf("lease must be positive")
	}
	if cfg.ProcessID == "" {
		return nil, fmt.Errorf("process id is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	return &Pool{cfg: cfg, deps: deps, queries: db.New(deps.DB)}, nil
}

// Run processes deliveries until ctx is cancelled, then shuts down gracefully:
// it stops claiming new work, lets in-flight deliveries finish for up to
// ShutdownTimeout, and returns. A delivery that does not finish in time is
// simply abandoned with its lease still set; the reaper returns it to the
// queue when the lease expires. There is no special-case shutdown recovery.
func (p *Pool) Run(ctx context.Context) error {
	// In-flight work must outlive ctx (which is cancelled by SIGTERM), so it
	// runs on a context that is only cancelled when the grace period ends.
	workCtx, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()

	// jobs is unbuffered and free holds one token per idle worker. The
	// dispatcher takes a token before it claims from Redis, so a job is only
	// ever claimed when a worker is ready for it: no leased job waits in a
	// channel while its lease runs down.
	jobs := make(chan string)
	free := make(chan struct{}, p.cfg.WorkerCount)
	for i := 0; i < p.cfg.WorkerCount; i++ {
		free <- struct{}{}
	}

	var workers sync.WaitGroup
	for i := 0; i < p.cfg.WorkerCount; i++ {
		workers.Add(1)
		workerID := fmt.Sprintf("%s/%d", p.cfg.ProcessID, i)
		go func() {
			defer workers.Done()
			p.work(workCtx, workerID, jobs, free)
		}()
	}

	// The dispatcher and the maintenance loops stop as soon as ctx is cancelled.
	loops, loopCtx := errgroup.WithContext(ctx)
	loops.Go(func() error { return p.dispatch(loopCtx, jobs, free) })
	loops.Go(func() error { return p.every(loopCtx, "scheduler", p.cfg.SchedulerInterval, p.promoteDue) })
	loops.Go(func() error { return p.every(loopCtx, "reaper", p.cfg.ReaperInterval, p.reap) })
	loops.Go(func() error { return p.every(loopCtx, "reconciler", p.cfg.ReconcilerInterval, p.reconcile) })
	loops.Go(func() error { return p.every(loopCtx, "queue-depth", queueDepthInterval, p.reportDepths) })
	err := loops.Wait()

	// Only the dispatcher sends on jobs, and it has returned, so closing is safe.
	close(jobs)
	p.drain(&workers, cancelWork)
	return err
}

// drain waits for in-flight deliveries, cancelling them when the grace period ends.
func (p *Pool) drain(workers *sync.WaitGroup, cancelWork context.CancelFunc) {
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	timer := time.NewTimer(p.cfg.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
		p.deps.Logger.Info("worker pool drained")
	case <-timer.C:
		p.deps.Logger.Warn("shutdown timeout reached; abandoning in-flight deliveries to lease recovery")
		cancelWork()
		<-done
	}
}

// dispatch is the single goroutine that claims jobs from Redis.
func (p *Pool) dispatch(ctx context.Context, jobs chan<- string, free chan struct{}) error {
	for {
		// Wait for an idle worker before claiming anything.
		select {
		case <-ctx.Done():
			return nil
		case <-free:
		}

		id, ok, err := p.deps.Queue.Claim(ctx, p.cfg.Lease)
		if err != nil || !ok {
			free <- struct{}{} // nothing claimed; hand the token back
			if err != nil && ctx.Err() == nil {
				p.deps.Logger.Warn("claim from queue failed", "error", err.Error())
			}
			// The claim is non-blocking, so an empty queue (or an unreachable
			// Redis) is polled at a fixed, gentle interval.
			if !sleep(ctx, p.cfg.PollInterval) {
				return nil
			}
			continue
		}

		// A worker is guaranteed to be idle (we hold its token), so this send
		// completes as soon as that worker reaches its receive.
		jobs <- id
	}
}

// work is one worker: it processes jobs one at a time until the channel closes.
func (p *Pool) work(ctx context.Context, workerID string, jobs <-chan string, free chan<- struct{}) {
	for id := range jobs {
		p.deps.Metrics.WorkersBusy.Inc()
		p.processSafely(ctx, workerID, id)
		p.deps.Metrics.WorkersBusy.Dec()
		free <- struct{}{}
	}
}

// processSafely confines a panic to the delivery that caused it. The row
// stays leased, so the reaper retries it after the lease expires, and the
// rest of the pool keeps running.
func (p *Pool) processSafely(ctx context.Context, workerID, deliveryID string) {
	defer func() {
		if r := recover(); r != nil {
			p.deps.Logger.Error("panic while processing delivery",
				"worker_id", workerID, "delivery_id", deliveryID,
				"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	p.process(ctx, workerID, deliveryID)
}

// every runs fn at a fixed interval until ctx is cancelled. A failed run is
// logged and retried on the next tick: these loops must survive Redis and
// PostgreSQL outages, because they are what repairs the system afterwards.
func (p *Pool) every(ctx context.Context, name string, interval time.Duration, fn func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				p.deps.Logger.Warn("background loop failed", "loop", name, "error", err.Error())
			}
		}
	}
}

// sleep waits for d or until ctx is cancelled; it reports whether d elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
