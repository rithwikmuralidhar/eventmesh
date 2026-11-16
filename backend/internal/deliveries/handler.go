package deliveries

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/audit"
	"eventmesh/internal/cache"
	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/httpx"
	"eventmesh/internal/observability"
	"eventmesh/internal/realtime"
	"eventmesh/internal/webhook"
)

// Enqueuer is the part of the queue a replay needs.
type Enqueuer interface {
	Enqueue(ctx context.Context, ids ...string) error
}

// Handler serves the /deliveries and /dashboard routes.
type Handler struct {
	pool      *pgxpool.Pool
	queries   *db.Queries
	enqueuer  Enqueuer
	publisher realtime.Publisher
	// defaultMaxRetries is the system default, used to report how many
	// attempts a delivery is allowed when its endpoint has no override.
	defaultMaxRetries int
	// statsCache serves dashboard statistics; nil means "always compute".
	statsCache *cache.Cache
}

// NewHandler returns the deliveries handler.
func NewHandler(pool *pgxpool.Pool, enqueuer Enqueuer, publisher realtime.Publisher, defaultMaxRetries int) *Handler {
	return &Handler{
		pool: pool, queries: db.New(pool), enqueuer: enqueuer, publisher: publisher,
		defaultMaxRetries: defaultMaxRetries,
	}
}

// Summary is one row of a delivery list.
type Summary struct {
	ID                 uuid.UUID  `json:"id"`
	EventID            uuid.UUID  `json:"event_id"`
	EndpointID         uuid.UUID  `json:"endpoint_id"`
	EventType          string     `json:"event_type"`
	EndpointName       string     `json:"endpoint_name"`
	Status             string     `json:"status"`
	AttemptCount       int32      `json:"attempt_count"`
	ReplayCount        int32      `json:"replay_count"`
	NextAttemptAt      *time.Time `json:"next_attempt_at"`
	LastAttemptAt      *time.Time `json:"last_attempt_at"`
	LastResponseStatus *int32     `json:"last_response_status"`
	LastErrorCategory  *string    `json:"last_error_category"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// SummaryFromRow converts a list row to its API form.
func SummaryFromRow(r db.ListDeliveriesRow) Summary {
	return Summary{
		ID: r.ID, EventID: r.EventID, EndpointID: r.EndpointID, EventType: r.EventType,
		EndpointName: r.EndpointName, Status: r.Status, AttemptCount: r.AttemptCount,
		ReplayCount: r.ReplayCount, NextAttemptAt: nextAttempt(Status(r.Status), r.NextAttemptAt),
		LastAttemptAt: r.LastAttemptAt, LastResponseStatus: r.LastResponseStatus,
		LastErrorCategory: r.LastErrorCategory, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// nextAttempt hides next_attempt_at unless a retry is actually scheduled; the
// column keeps its old value in other states, where it would mislead.
func nextAttempt(status Status, at time.Time) *time.Time {
	if status == StatusRetryScheduled || status == StatusPending {
		return &at
	}
	return nil
}

// List handles GET /deliveries.
func (h *Handler) List(c *gin.Context) {
	params, limit, err := parseListParams(c)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	rows, err := h.queries.ListDeliveries(c.Request.Context(), params)
	if err != nil {
		httpx.WriteError(c, fmt.Errorf("list deliveries: %w", err))
		return
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
		items = append(items, SummaryFromRow(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "next_cursor": next})
}

func parseListParams(c *gin.Context) (db.ListDeliveriesParams, int32, error) {
	none := db.ListDeliveriesParams{}
	if err := httpx.AllowQuery(c, "limit", "cursor", "status", "endpoint_id", "event_id", "event_type", "from", "to"); err != nil {
		return none, 0, err
	}
	page, err := httpx.ParsePage(c)
	if err != nil {
		return none, 0, err
	}
	cursorAt, cursorID := page.CursorArgs()
	params := db.ListDeliveriesParams{
		UserID: httpx.UserID(c), CursorCreatedAt: cursorAt, CursorID: cursorID, PageLimit: page.Limit + 1,
	}
	if v := c.Query("status"); v != "" {
		if !Status(v).Valid() {
			return none, 0, httpx.BadRequest("status must be one of pending, processing, retry_scheduled, delivered, dead_letter, cancelled.")
		}
		params.Status = &v
	}
	if params.EndpointID, err = queryUUID(c, "endpoint_id"); err != nil {
		return none, 0, err
	}
	if params.EventID, err = queryUUID(c, "event_id"); err != nil {
		return none, 0, err
	}
	if v := c.Query("event_type"); v != "" {
		if len(v) > 100 {
			return none, 0, httpx.BadRequest("event_type filter is too long.")
		}
		params.EventType = &v
	}
	if params.CreatedFrom, err = httpx.QueryTime(c, "from"); err != nil {
		return none, 0, err
	}
	if params.CreatedTo, err = httpx.QueryTime(c, "to"); err != nil {
		return none, 0, err
	}
	return params, page.Limit, nil
}

func queryUUID(c *gin.Context, name string) (*uuid.UUID, error) {
	raw := c.Query(name)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, httpx.BadRequest(name + " must be a UUID.")
	}
	return &id, nil
}

// Attempt is one HTTP attempt in a delivery's history.
type Attempt struct {
	ID                  uuid.UUID `json:"id"`
	AttemptNumber       int32     `json:"attempt_number"`
	Trigger             string    `json:"trigger"`
	WorkerID            string    `json:"worker_id"`
	StartedAt           time.Time `json:"started_at"`
	ResponseStatus      *int32    `json:"response_status"`
	ResponseBodyExcerpt *string   `json:"response_body_excerpt"`
	DurationMS          int32     `json:"duration_ms"`
	ErrorCategory       *string   `json:"error_category"`
	ErrorMessage        *string   `json:"error_message"`
}

// Detail is the response of GET /deliveries/:id.
type Detail struct {
	Summary
	CycleAttemptCount int32   `json:"cycle_attempt_count"`
	LastError         *string `json:"last_error"`
	EndpointURL       string  `json:"endpoint_url"`
	EndpointEnabled   bool    `json:"endpoint_enabled"`
	EndpointDeleted   bool    `json:"endpoint_deleted"`
	// MaxAttempts is the attempt budget per cycle: retries + 1.
	MaxAttempts int32 `json:"max_attempts"`
	CanReplay   bool  `json:"can_replay"`
	// RequestHeaders are the headers of the most recent attempt. The
	// signature value is not stored, so it is described rather than shown.
	RequestHeaders []Header  `json:"request_headers"`
	Attempts       []Attempt `json:"attempts"`
}

// Header is one request header, kept as an ordered pair for display.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Get handles GET /deliveries/:id.
func (h *Handler) Get(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "Delivery")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	detail, err := h.load(c.Request.Context(), httpx.UserID(c), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"delivery": detail})
}

func (h *Handler) load(ctx context.Context, userID, id uuid.UUID) (Detail, error) {
	row, err := h.queries.GetDelivery(ctx, db.GetDeliveryParams{ID: id, UserID: userID})
	if database.IsNotFound(err) {
		return Detail{}, httpx.NotFound("Delivery")
	}
	if err != nil {
		return Detail{}, fmt.Errorf("get delivery: %w", err)
	}
	attemptRows, err := h.queries.ListAttempts(ctx, db.ListAttemptsParams{DeliveryID: id, UserID: userID})
	if err != nil {
		return Detail{}, fmt.Errorf("list attempts: %w", err)
	}

	maxRetries := int32(h.defaultMaxRetries)
	if row.EndpointMaxRetries != nil {
		maxRetries = *row.EndpointMaxRetries
	}
	status := Status(row.Status)
	deleted := row.EndpointDeletedAt != nil
	detail := Detail{
		Summary: Summary{
			ID: row.ID, EventID: row.EventID, EndpointID: row.EndpointID, EventType: row.EventType,
			EndpointName: row.EndpointName, Status: row.Status, AttemptCount: row.AttemptCount,
			ReplayCount: row.ReplayCount, NextAttemptAt: nextAttempt(status, row.NextAttemptAt),
			LastAttemptAt: row.LastAttemptAt, LastResponseStatus: row.LastResponseStatus,
			LastErrorCategory: row.LastErrorCategory, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		},
		CycleAttemptCount: row.CycleAttemptCount,
		LastError:         row.LastError,
		EndpointURL:       row.EndpointUrl,
		EndpointEnabled:   row.EndpointEnabled,
		EndpointDeleted:   deleted,
		MaxAttempts:       maxRetries + 1,
		CanReplay:         status == StatusDeadLetter && row.EndpointEnabled && !deleted,
		RequestHeaders:    []Header{},
		Attempts:          make([]Attempt, 0, len(attemptRows)),
	}
	for _, a := range attemptRows {
		detail.Attempts = append(detail.Attempts, Attempt{
			ID: a.ID, AttemptNumber: a.AttemptNumber, Trigger: a.Trigger, WorkerID: a.WorkerID,
			StartedAt: a.StartedAt, ResponseStatus: a.ResponseStatus, ResponseBodyExcerpt: a.ResponseBodyExcerpt,
			DurationMS: a.DurationMs, ErrorCategory: a.ErrorCategory, ErrorMessage: a.ErrorMessage,
		})
	}
	if n := len(detail.Attempts); n > 0 {
		detail.RequestHeaders = sentHeaders(row, detail.Attempts[n-1])
	}
	return detail, nil
}

// sentHeaders rebuilds the headers of an attempt from stored facts. The
// signature is an HMAC that depends on the secret and is never persisted.
func sentHeaders(row db.GetDeliveryRow, last Attempt) []Header {
	return []Header{
		{"Content-Type", "application/json"},
		{"User-Agent", webhook.UserAgent},
		{webhook.HeaderEventID, row.EventID.String()},
		{webhook.HeaderEventType, row.EventType},
		{webhook.HeaderDeliveryID, row.ID.String()},
		{webhook.HeaderAttempt, strconv.Itoa(int(last.AttemptNumber))},
		{webhook.HeaderTimestamp, strconv.FormatInt(last.StartedAt.Unix(), 10)},
		{webhook.HeaderSignature, "v1=<HMAC-SHA256 of \"timestamp.body\"; not stored>"},
	}
}

// Replay handles POST /deliveries/:id/replay.
func (h *Handler) Replay(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "Delivery")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	userID := httpx.UserID(c)
	if err := h.replay(c.Request.Context(), userID, id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	detail, err := h.load(c.Request.Context(), userID, id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"delivery": detail})
}

// replay resets a dead-lettered delivery and re-enqueues it. It reuses the
// same row, so the delivery ID that consumers deduplicate on stays stable and
// the (event, endpoint) uniqueness holds. Attempt history is untouched.
func (h *Handler) replay(ctx context.Context, userID, id uuid.UUID) error {
	if err := ValidateTransition(StatusDeadLetter, StatusPending); err != nil {
		return err
	}
	var endpointID, eventID uuid.UUID
	err := database.InTx(ctx, h.pool, func(q *db.Queries) error {
		row, err := q.ReplayDelivery(ctx, db.ReplayDeliveryParams{ID: id, UserID: userID})
		if database.IsNotFound(err) {
			// The conditional UPDATE matched nothing; find out why, to answer precisely.
			return h.replayRefusal(ctx, q, userID, id)
		}
		if err != nil {
			return fmt.Errorf("replay delivery: %w", err)
		}
		endpointID, eventID = row.EndpointID, row.EventID
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionDeliveryReplayed, ResourceType: audit.ResourceDelivery, ResourceID: id,
			Metadata: map[string]any{"replay_count": row.ReplayCount},
		})
	})
	if err != nil {
		return err
	}

	// Fast path only: if this fails, the row is pending and due in PostgreSQL
	// and the reconciler will enqueue it.
	bg := context.WithoutCancel(ctx)
	enqueueCtx, cancel := context.WithTimeout(bg, 2*time.Second)
	defer cancel()
	if err := h.enqueuer.Enqueue(enqueueCtx, id.String()); err != nil {
		observability.Logger(ctx).Warn("enqueue after replay failed; the reconciler will pick it up",
			"delivery_id", id.String(), "error", err.Error())
	}
	h.publisher.Publish(bg, userID, realtime.Update{
		Type: realtime.TypeDeliveryUpdated, DeliveryID: id.String(), EventID: eventID.String(),
		EndpointID: endpointID.String(), Status: string(StatusPending),
	})
	return nil
}

func (h *Handler) replayRefusal(ctx context.Context, q *db.Queries, userID, id uuid.UUID) error {
	row, err := q.GetDelivery(ctx, db.GetDeliveryParams{ID: id, UserID: userID})
	if database.IsNotFound(err) {
		return httpx.NotFound("Delivery")
	}
	if err != nil {
		return fmt.Errorf("get delivery: %w", err)
	}
	switch {
	case Status(row.Status) != StatusDeadLetter:
		return httpx.Conflict("not_replayable",
			fmt.Sprintf("Only dead-lettered deliveries can be replayed; this one is %q.", row.Status))
	case row.EndpointDeletedAt != nil:
		return httpx.Conflict("endpoint_unavailable", "The endpoint for this delivery has been deleted.")
	default:
		return httpx.Conflict("endpoint_unavailable", "The endpoint for this delivery is disabled. Enable it, then replay.")
	}
}

// WithStatsCache makes GET /dashboard/stats read through c. It returns h.
func (h *Handler) WithStatsCache(c *cache.Cache) *Handler {
	h.statsCache = c
	return h
}
