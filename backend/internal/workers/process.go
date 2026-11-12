package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/deliveries"
	"eventmesh/internal/ratelimit"
	"eventmesh/internal/realtime"
	"eventmesh/internal/retry"
	"eventmesh/internal/webhook"
)

// Attempt triggers stored on delivery_attempts.
const (
	triggerInitial = "initial"
	triggerRetry   = "retry"
	triggerReplay  = "replay"
)

// process handles one claimed job end to end. Every early return leaves the
// system in a state the background loops can repair: nothing here may lose a
// delivery, only delay it.
func (p *Pool) process(ctx context.Context, workerID, rawID string) {
	log := p.deps.Logger.With("worker_id", workerID, "delivery_id", rawID)

	id, err := uuid.Parse(rawID)
	if err != nil {
		log.Error("queue held a malformed delivery id; dropping it", "error", err.Error())
		p.ack(ctx, rawID)
		return
	}

	// Layer two of the double-processing protection: the row claim in
	// PostgreSQL. Redis only said "you may look at this one".
	row, err := p.queries.ClaimDelivery(ctx, db.ClaimDeliveryParams{
		WorkerID: &workerID, LeaseSeconds: p.cfg.Lease.Seconds(), ID: id,
	})
	if err != nil {
		if database.IsNotFound(err) {
			p.handleUnclaimable(ctx, log, id)
			return
		}
		// PostgreSQL trouble: leave the job leased in Redis. The reaper returns
		// it when the lease expires, and the row was not changed.
		log.Warn("claim delivery row failed", "error", err.Error())
		return
	}
	log = log.With("event_id", row.EventID.String(), "endpoint_id", row.EndpointID.String())

	// A disabled or deleted endpoint gets nothing, even for deliveries that
	// were created while it was still active.
	if !row.Enabled || row.DeletedAt != nil {
		p.cancel(ctx, log, workerID, row)
		return
	}

	if !p.takeEndpointToken(ctx, log, workerID, row) {
		return
	}

	secret, err := p.deps.Cipher.Decrypt(row.SecretCiphertext)
	if err != nil {
		// Without the secret the request cannot be signed. Treat it as a
		// non-retryable failure of this delivery instead of looping forever.
		log.Error("decrypt endpoint secret failed", "error", err.Error())
		p.finishWithError(ctx, log, workerID, row, fmt.Errorf("endpoint secret unavailable"))
		return
	}

	body, err := webhook.BuildBody(row.EventID, row.EventType, row.EventCreatedAt, row.Payload)
	if err != nil {
		log.Error("build webhook body failed", "error", err.Error())
		p.finishWithError(ctx, log, workerID, row, fmt.Errorf("build body: %w", err))
		return
	}

	attemptNumber := int(row.AttemptCount) + 1
	started := p.deps.Clock.Now()
	result := p.deps.Sender.Send(ctx, webhook.Request{
		URL: row.Url, Secret: secret, EventID: row.EventID, EventType: row.EventType,
		DeliveryID: row.ID, Attempt: attemptNumber, Timestamp: started, Body: body,
	})
	p.record(ctx, log, workerID, row, started, result)
}

// handleUnclaimable runs when the row claim returned nothing. Either the job
// is stale (already handled, or held by another worker), or it was claimed a
// moment before it was due.
func (p *Pool) handleUnclaimable(ctx context.Context, log *slog.Logger, id uuid.UUID) {
	sched, err := p.queries.GetDeliverySchedule(ctx, id)
	if err == nil && (sched.Status == string(deliveries.StatusPending) || sched.Status == string(deliveries.StatusRetryScheduled)) {
		if wait := time.Until(sched.NextAttemptAt); wait > 0 {
			// Not due yet: park it instead of dropping it.
			if err := p.deps.Queue.Schedule(ctx, id.String(), wait); err != nil {
				p.deps.Logger.Warn("reschedule early job failed", "delivery_id", id.String(), "error", err.Error())
			}
			return
		}
	}
	// Terminal, processing elsewhere, or unknown: this queue entry is a
	// duplicate or stale. Dropping it is the intended, harmless outcome.
	log.Info("dropping stale queue entry")
	p.ack(ctx, id.String())
}

func (p *Pool) cancel(ctx context.Context, log *slog.Logger, workerID string, row db.ClaimDeliveryRow) {
	if _, err := p.queries.CancelClaimedDelivery(ctx, db.CancelClaimedDeliveryParams{ID: row.ID, WorkerID: &workerID}); err != nil {
		if !database.IsNotFound(err) {
			log.Warn("cancel delivery failed", "error", err.Error())
		}
		return
	}
	p.ack(ctx, row.ID.String())
	p.deps.Metrics.DeliveriesCompleted.WithLabelValues(string(deliveries.StatusCancelled)).Inc()
	p.publish(ctx, row, deliveries.StatusCancelled)
}

// takeEndpointToken applies the endpoint's outgoing rate limit. It returns
// true when the delivery may proceed. A refusal is not a failure: the row goes
// back to pending with a later due time, with no attempt and no retry count.
func (p *Pool) takeEndpointToken(ctx context.Context, log *slog.Logger, workerID string, row db.ClaimDeliveryRow) bool {
	rate := int(row.RateLimitPerSecond)
	decision, err := p.deps.Limiter.Allow(ctx, ratelimit.EndpointKey(row.EndpointID.String()), float64(rate), rate)
	if err != nil {
		// The limiter protects the receiver, not us; if Redis hiccups we send.
		log.Warn("endpoint rate limiter unavailable, sending anyway", "error", err.Error())
		return true
	}
	if decision.Allowed {
		return true
	}
	wait := decision.RetryAfter + time.Duration(p.deps.Rand.Int63n(int64(50*time.Millisecond)+1))
	next := p.deps.Clock.Now().Add(wait)
	if _, err := p.queries.DeferDelivery(ctx, db.DeferDeliveryParams{NextAttemptAt: next, ID: row.ID, WorkerID: &workerID}); err != nil {
		if !database.IsNotFound(err) {
			log.Warn("defer delivery failed", "error", err.Error())
		}
		return false
	}
	if err := p.deps.Queue.Schedule(ctx, row.ID.String(), wait); err != nil {
		log.Warn("schedule deferred delivery failed; the reconciler will pick it up", "error", err.Error())
	}
	p.deps.Metrics.CountRateLimited("endpoint")
	p.publish(ctx, row, deliveries.StatusPending)
	return false
}

func (p *Pool) policyFor(row db.ClaimDeliveryRow) retry.Policy {
	pol := p.cfg.DefaultPolicy
	if row.MaxRetries != nil {
		pol.MaxRetries = int(*row.MaxRetries)
	}
	if row.InitialRetryDelayMs != nil {
		pol.InitialDelay = time.Duration(*row.InitialRetryDelayMs) * time.Millisecond
	}
	if row.MaxRetryDelayMs != nil {
		pol.MaxDelay = time.Duration(*row.MaxRetryDelayMs) * time.Millisecond
	}
	return pol
}

// finishWithError records an internal, non-retryable failure as a dead letter.
func (p *Pool) finishWithError(ctx context.Context, log *slog.Logger, workerID string, row db.ClaimDeliveryRow, cause error) {
	p.record(ctx, log, workerID, row, p.deps.Clock.Now(), webhook.Result{Err: fmt.Errorf("%w: %s", errInternal, cause.Error())})
}

var errInternal = errors.New("internal delivery error")

// record classifies the outcome, writes attempt and new state in one
// transaction, then updates Redis and notifies the dashboard.
func (p *Pool) record(ctx context.Context, log *slog.Logger, workerID string, row db.ClaimDeliveryRow, started time.Time, res webhook.Result) {
	class := retry.Classify(res.StatusCode, res.Err)
	if errors.Is(res.Err, errInternal) {
		class = retry.Classification{Category: retry.CategoryNetwork}
	}
	policy := p.policyFor(row)
	now := p.deps.Clock.Now()
	cycleAfter := int(row.CycleAttemptCount) + 1

	status := deliveries.StatusDelivered
	var delay time.Duration
	switch {
	case class.Success:
	case retry.ShouldRetry(class, cycleAfter, policy):
		status = deliveries.StatusRetryScheduled
		var retryAfter time.Duration
		if class.HonorRetryAfter {
			retryAfter, _ = retry.ParseRetryAfter(res.RetryAfter, now)
		}
		// cycleAfter-1 retries have happened; this is retry number cycleAfter-1.
		delay = retry.NextDelay(policy, cycleAfter-1, retryAfter, class.HonorRetryAfter, p.deps.Rand)
	default:
		status = deliveries.StatusDeadLetter
	}

	trigger := triggerRetry
	switch {
	case row.AttemptCount == 0:
		trigger = triggerInitial
	case row.CycleAttemptCount == 0 && row.ReplayCount > 0:
		trigger = triggerReplay
	}

	var statusCode *int32
	var excerpt, errCategory, errMessage *string
	if res.Err == nil {
		sc := int32(res.StatusCode)
		statusCode = &sc
		if res.BodyExcerpt != "" {
			excerpt = &res.BodyExcerpt
		}
	}
	if !class.Success {
		errCategory = &class.Category
		msg := failureMessage(class, res)
		errMessage = &msg
	}

	finish := db.FinishDeliveryParams{
		Status: string(status), NextAttemptAt: now.Add(delay), LastAttemptAt: &started,
		LastResponseStatus: statusCode, LastErrorCategory: errCategory, LastError: errMessage,
		ID: row.ID, WorkerID: &workerID,
	}
	attempt := db.InsertAttemptParams{
		ID: newID(), DeliveryID: row.ID, AttemptNumber: row.AttemptCount + 1, Trigger: trigger,
		WorkerID: workerID, StartedAt: started, ResponseStatus: statusCode, ResponseBodyExcerpt: excerpt,
		DurationMs: int32(res.Duration.Milliseconds()), ErrorCategory: errCategory, ErrorMessage: errMessage,
	}

	// Recording must not be cut short by shutdown: the request was already sent.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	lost := false
	err := database.InTx(recCtx, p.deps.DB, func(q *db.Queries) error {
		// Fencing: only the worker that still owns the row may write its result.
		if _, err := q.FinishDelivery(recCtx, finish); err != nil {
			if database.IsNotFound(err) {
				lost = true
				return nil
			}
			return err
		}
		return q.InsertAttempt(recCtx, attempt)
	})
	if err != nil {
		// The row stays 'processing'; the lease expires and the delivery is
		// retried. At-least-once: the endpoint may see it twice.
		log.Warn("recording delivery outcome failed; lease recovery will retry it", "error", err.Error())
		return
	}
	if lost {
		log.Warn("lease lost before the result was recorded; discarding this result")
		return
	}

	outcome := "failure"
	if class.Success {
		outcome = "success"
	}
	label := class.Category
	p.deps.Metrics.DeliveryAttempts.WithLabelValues(outcome, label).Inc()
	p.deps.Metrics.AttemptDuration.WithLabelValues(outcome).Observe(res.Duration.Seconds())

	if status == deliveries.StatusRetryScheduled {
		if err := p.deps.Queue.Schedule(recCtx, row.ID.String(), delay); err != nil {
			log.Warn("schedule retry failed; the reconciler will pick it up", "error", err.Error())
		}
	} else {
		p.ack(recCtx, row.ID.String())
		p.deps.Metrics.DeliveriesCompleted.WithLabelValues(string(status)).Inc()
		if status == deliveries.StatusDelivered {
			p.deps.Metrics.DeliveryLatency.Observe(now.Sub(row.EventCreatedAt).Seconds())
		}
	}
	p.publish(recCtx, row, status)
	p.deps.Logger.Info("delivery attempt finished",
		"worker_id", workerID, "delivery_id", row.ID.String(), "event_id", row.EventID.String(),
		"endpoint_id", row.EndpointID.String(), "attempt", attempt.AttemptNumber,
		"outcome", string(status), "error_category", label, "duration_ms", attempt.DurationMs)
}

// failureMessage is a short human-readable reason stored with the attempt. It
// never includes the request body or headers.
func failureMessage(c retry.Classification, res webhook.Result) string {
	if res.Err != nil {
		return truncate(res.Err.Error(), 500)
	}
	return fmt.Sprintf("endpoint returned HTTP %d (%s)", res.StatusCode, c.Category)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (p *Pool) ack(ctx context.Context, id string) {
	if err := p.deps.Queue.Ack(context.WithoutCancel(ctx), id); err != nil {
		p.deps.Logger.Warn("ack failed; the lease will expire and the entry will be dropped as stale", "delivery_id", id, "error", err.Error())
	}
}

func (p *Pool) publish(ctx context.Context, row db.ClaimDeliveryRow, status deliveries.Status) {
	p.deps.Publisher.Publish(ctx, row.UserID, realtime.Update{
		Type: realtime.TypeDeliveryUpdated, EventID: row.EventID.String(),
		DeliveryID: row.ID.String(), EndpointID: row.EndpointID.String(), Status: string(status),
	})
}

func newID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails if the system random source does; nothing sane can continue.
		panic(fmt.Sprintf("generate uuid: %v", err))
	}
	return id
}

// promoteDue moves due retries and deferrals from the delayed set to ready.
func (p *Pool) promoteDue(ctx context.Context) error {
	for {
		n, err := p.deps.Queue.PromoteDue(ctx, loopBatchSize)
		if err != nil || n < loopBatchSize {
			return err
		}
	}
}

// reap recovers deliveries whose worker vanished, in both stores. The Redis
// side returns expired leases to ready; the PostgreSQL side resets rows still
// marked processing past their lease, and enqueues them right away rather
// than waiting for the reconciler.
func (p *Pool) reap(ctx context.Context) error {
	if _, err := p.deps.Queue.ReapExpired(ctx, loopBatchSize); err != nil {
		return err
	}
	ids, err := p.queries.ReapExpiredDeliveries(ctx, loopBatchSize)
	if err != nil {
		return fmt.Errorf("reap expired deliveries: %w", err)
	}
	return p.enqueueIDs(ctx, ids)
}

// reconcile re-enqueues deliveries PostgreSQL says are due but Redis lost.
// Duplicates are harmless because the row claim admits only one worker.
func (p *Pool) reconcile(ctx context.Context) error {
	ids, err := p.queries.ListDueDeliveries(ctx, db.ListDueDeliveriesParams{
		GraceSeconds: p.cfg.ReconcilerGrace.Seconds(), BatchSize: loopBatchSize,
	})
	if err != nil {
		return fmt.Errorf("list due deliveries: %w", err)
	}
	return p.enqueueIDs(ctx, ids)
}

func (p *Pool) enqueueIDs(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	return p.deps.Queue.Enqueue(ctx, strs...)
}

func (p *Pool) reportDepths(ctx context.Context) error {
	d, err := p.deps.Queue.Depths(ctx)
	if err != nil {
		return err
	}
	g := p.deps.Metrics.QueueDepth
	g.WithLabelValues("ready").Set(float64(d.Ready))
	g.WithLabelValues("delayed").Set(float64(d.Delayed))
	g.WithLabelValues("processing").Set(float64(d.Processing))
	return nil
}
