package events

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/httpx"
	"eventmesh/internal/observability"
	"eventmesh/internal/realtime"
)

// Event statuses, derived from the event's deliveries.
const (
	StatusNoSubscribers = "no_subscribers"
	StatusInProgress    = "in_progress"
	StatusDelivered     = "delivered"
	StatusFailed        = "failed"
)

// Ingestion results, used as the metric label.
const (
	ResultCreated  = "created"
	ResultReplayed = "replayed"
	ResultConflict = "conflict"
	ResultInvalid  = "invalid"
	ResultError    = "error"
)

// Enqueuer is the part of the queue ingestion needs. It is an interface so a
// test can make enqueueing fail and prove the delivery is still processed.
type Enqueuer interface {
	Enqueue(ctx context.Context, ids ...string) error
}

// Service implements event ingestion.
type Service struct {
	pool      *pgxpool.Pool
	queries   *db.Queries
	enqueuer  Enqueuer
	publisher realtime.Publisher
	metrics   *observability.Metrics
}

// NewService returns the ingestion service.
func NewService(pool *pgxpool.Pool, enqueuer Enqueuer, publisher realtime.Publisher, metrics *observability.Metrics) *Service {
	return &Service{pool: pool, queries: db.New(pool), enqueuer: enqueuer, publisher: publisher, metrics: metrics}
}

// IngestInput is one ingestion request.
type IngestInput struct {
	EventType string
	Payload   json.RawMessage
	// IdempotencyKey is empty when the header was not sent.
	IdempotencyKey string
}

// IngestResult describes the stored event.
type IngestResult struct {
	ID         uuid.UUID `json:"id"`
	Status     string    `json:"status"`
	Deliveries int       `json:"deliveries"`
	CreatedAt  time.Time `json:"created_at"`
	// Replayed is true when the idempotency key matched an earlier request
	// and no new event was created.
	Replayed bool `json:"-"`
}

var errIdempotencyConflict = httpx.Conflict("idempotency_key_conflict",
	"This Idempotency-Key was already used with a different request body.")

// errKeyTaken is internal: the insert hit the idempotency unique constraint.
var errKeyTaken = errors.New("idempotency key already used")

// Ingest validates and stores an event, creates its deliveries, and hands
// them to the queue. It never waits for a delivery.
func (s *Service) Ingest(ctx context.Context, userID uuid.UUID, in IngestInput) (IngestResult, error) {
	result, err := s.ingest(ctx, userID, in)
	s.metrics.EventsIngested.WithLabelValues(resultLabel(result, err)).Inc()
	return result, err
}

func resultLabel(result IngestResult, err error) string {
	var apiErr *httpx.Error
	switch {
	case err == nil && result.Replayed:
		return ResultReplayed
	case err == nil:
		return ResultCreated
	case errors.Is(err, errIdempotencyConflict):
		return ResultConflict
	case errors.As(err, &apiErr):
		return ResultInvalid
	default:
		return ResultError
	}
}

func (s *Service) ingest(ctx context.Context, userID uuid.UUID, in IngestInput) (IngestResult, error) {
	fields := map[string]string{}
	if err := ValidateEventType(in.EventType); err != nil {
		fields["event_type"] = err.Error()
	}
	canonical, err := CanonicalPayload(in.Payload)
	if err != nil {
		fields["payload"] = err.Error()
	}
	if len(in.IdempotencyKey) > MaxIdempotencyKeyLen {
		fields["Idempotency-Key"] = fmt.Sprintf("Idempotency-Key must be at most %d characters", MaxIdempotencyKeyLen)
	}
	if len(fields) > 0 {
		return IngestResult{}, httpx.Validation(fields)
	}
	requestHash := RequestHash(in.EventType, canonical)

	// A key-taken insert is followed by a read of the existing event. In the
	// rare case that the transaction which held the key rolled back in
	// between, the read finds nothing and the insert is simply tried again.
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, deliveryIDs, err := s.insert(ctx, userID, in, canonical, requestHash)
		if err == nil {
			s.afterCommit(ctx, userID, result, deliveryIDs)
			return result, nil
		}
		if !errors.Is(err, errKeyTaken) {
			return IngestResult{}, err
		}
		existing, found, err := s.existing(ctx, userID, in.IdempotencyKey, requestHash)
		if err != nil {
			return IngestResult{}, err
		}
		if found {
			return existing, nil
		}
	}
	return IngestResult{}, fmt.Errorf("ingest event: idempotency key state did not settle after %d attempts", maxAttempts)
}

// insert is the ingestion transaction: the event and one delivery per
// matching endpoint are written together or not at all. The fan-out is fixed
// here; endpoints created later never receive this event.
func (s *Service) insert(ctx context.Context, userID uuid.UUID, in IngestInput, canonical, requestHash []byte) (IngestResult, []string, error) {
	eventID, err := uuid.NewV7()
	if err != nil {
		return IngestResult{}, nil, fmt.Errorf("generate event id: %w", err)
	}
	var idempotencyKey *string
	if in.IdempotencyKey != "" {
		idempotencyKey = &in.IdempotencyKey
	}

	var result IngestResult
	var deliveryIDs []string
	err = database.InTx(ctx, s.pool, func(q *db.Queries) error {
		row, err := q.InsertEvent(ctx, db.InsertEventParams{
			ID: eventID, UserID: userID, EventType: in.EventType, Payload: canonical,
			IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		})
		if database.IsNotFound(err) {
			// ON CONFLICT DO NOTHING returned no row: the unique constraint on
			// (user_id, idempotency_key) says this key already has an event.
			return errKeyTaken
		}
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}

		endpoints, err := q.ListActiveEndpointsForUser(ctx, userID)
		if err != nil {
			return fmt.Errorf("list active endpoints: %w", err)
		}
		ids := make([]uuid.UUID, 0, len(endpoints))
		endpointIDs := make([]uuid.UUID, 0, len(endpoints))
		for _, ep := range endpoints {
			if !MatchesAny(ep.EventTypes, in.EventType) {
				continue
			}
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("generate delivery id: %w", err)
			}
			ids = append(ids, id)
			endpointIDs = append(endpointIDs, ep.ID)
		}
		if len(ids) > 0 {
			err := q.InsertDeliveries(ctx, db.InsertDeliveriesParams{
				Ids: ids, EventID: eventID, EndpointIds: endpointIDs, UserID: userID,
			})
			if err != nil {
				return fmt.Errorf("insert deliveries: %w", err)
			}
		}

		status := StatusNoSubscribers
		if len(ids) > 0 {
			status = StatusInProgress
		}
		result = IngestResult{ID: row.ID, Status: status, Deliveries: len(ids), CreatedAt: row.CreatedAt}
		deliveryIDs = make([]string, len(ids))
		for i, id := range ids {
			deliveryIDs[i] = id.String()
		}
		return nil
	})
	if err != nil {
		return IngestResult{}, nil, err
	}
	return result, deliveryIDs, nil
}

// afterCommit is the fast path to the workers. The event is already durable,
// so a failure here must not fail the request: PostgreSQL holds the pending
// deliveries, and the reconciler enqueues anything this step missed.
func (s *Service) afterCommit(ctx context.Context, userID uuid.UUID, result IngestResult, deliveryIDs []string) {
	// WithoutCancel: if the client disconnects right after the commit, the
	// enqueue should still be attempted rather than left to the reconciler.
	bg := context.WithoutCancel(ctx)
	if len(deliveryIDs) > 0 {
		enqueueCtx, cancel := context.WithTimeout(bg, 2*time.Second)
		if err := s.enqueuer.Enqueue(enqueueCtx, deliveryIDs...); err != nil {
			observability.Logger(ctx).Warn("enqueue after commit failed; the reconciler will pick the deliveries up",
				"event_id", result.ID.String(), "deliveries", len(deliveryIDs), "error", err.Error())
		}
		cancel()
	}
	s.publisher.Publish(bg, userID, realtime.Update{
		Type: realtime.TypeEventCreated, EventID: result.ID.String(), Status: result.Status,
	})
}

// existing loads the event that already owns an idempotency key and decides
// between "replay" (same request) and "conflict" (different request).
func (s *Service) existing(ctx context.Context, userID uuid.UUID, key string, requestHash []byte) (IngestResult, bool, error) {
	row, err := s.queries.GetEventByIdempotencyKey(ctx, db.GetEventByIdempotencyKeyParams{UserID: userID, IdempotencyKey: &key})
	if database.IsNotFound(err) {
		return IngestResult{}, false, nil
	}
	if err != nil {
		return IngestResult{}, false, fmt.Errorf("load event by idempotency key: %w", err)
	}
	if subtle.ConstantTimeCompare(row.RequestHash, requestHash) != 1 {
		return IngestResult{}, false, errIdempotencyConflict
	}
	event, err := s.queries.GetEvent(ctx, db.GetEventParams{ID: row.ID, UserID: userID})
	if err != nil {
		return IngestResult{}, false, fmt.Errorf("load original event: %w", err)
	}
	return IngestResult{
		ID: event.ID, Status: event.Status, Deliveries: int(event.DeliveryCount),
		CreatedAt: event.CreatedAt, Replayed: true,
	}, true, nil
}

// ListResult is one page of the event list.
type ListResult struct {
	Items      []Summary
	NextCursor *string
}

// List runs the event list query. It is the single implementation behind
// GET /events and the natural-language search, so both apply identical
// ownership and filter rules. limit is the page size; params.PageLimit is
// set here (one extra row detects a next page).
func (s *Service) List(ctx context.Context, params db.ListEventsParams, limit int32) (ListResult, error) {
	params.PageLimit = limit + 1
	rows, err := s.queries.ListEvents(ctx, params)
	if err != nil {
		return ListResult{}, fmt.Errorf("list events: %w", err)
	}
	var next *string
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		cur := httpx.EncodeCursor(last.CreatedAt, last.ID)
		next = &cur
	}
	items := make([]Summary, 0, len(rows))
	for _, r := range rows {
		items = append(items, Summary{
			ID: r.ID, EventType: r.EventType, Status: r.Status, DeliveryCount: r.DeliveryCount,
			IdempotencyKey: r.IdempotencyKey, CreatedAt: r.CreatedAt,
		})
	}
	return ListResult{Items: items, NextCursor: next}, nil
}
