package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/deliveries"
	"eventmesh/internal/httpx"
)

// MaxIngestBodyBytes caps the ingestion request body: the 256 KB payload
// limit plus room for the small JSON envelope around it.
const MaxIngestBodyBytes = MaxPayloadBytes + 1024

// uuidPrefixRE restricts the ID search to characters a UUID can contain, so
// the value is safe to use in a LIKE pattern (no % or _ can get through).
var uuidPrefixRE = regexp.MustCompile(`^[0-9a-f-]{1,36}$`)

// Handler serves the /events routes.
type Handler struct {
	service *Service
	queries *db.Queries
}

// NewHandler returns the events handler.
func NewHandler(service *Service, pool *pgxpool.Pool) *Handler {
	return &Handler{service: service, queries: db.New(pool)}
}

type ingestRequest struct {
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

// Ingest handles POST /events (API key auth).
func (h *Handler) Ingest(c *gin.Context) {
	var req ingestRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	result, err := h.service.Ingest(c.Request.Context(), httpx.UserID(c), IngestInput{
		EventType:      req.EventType,
		Payload:        req.Payload,
		IdempotencyKey: strings.TrimSpace(c.GetHeader("Idempotency-Key")),
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if result.Replayed {
		// 200, not 202: nothing new was accepted. The header lets a client
		// tell a replay from a first submission.
		c.Header("Idempotent-Replayed", "true")
		c.JSON(http.StatusOK, result)
		return
	}
	c.JSON(http.StatusAccepted, result)
}

// Summary is one row of the event list.
type Summary struct {
	ID             uuid.UUID `json:"id"`
	EventType      string    `json:"event_type"`
	Status         string    `json:"status"`
	DeliveryCount  int32     `json:"delivery_count"`
	IdempotencyKey *string   `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

var validStatuses = map[string]bool{
	StatusNoSubscribers: true, StatusInProgress: true, StatusDelivered: true, StatusFailed: true,
}

// List handles GET /events.
func (h *Handler) List(c *gin.Context) {
	params, limit, err := parseListParams(c)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	res, err := h.service.List(c.Request.Context(), params, limit)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": res.Items, "next_cursor": res.NextCursor})
}

func parseListParams(c *gin.Context) (db.ListEventsParams, int32, error) {
	if err := httpx.AllowQuery(c, "limit", "cursor", "event_type", "status", "from", "to", "id", "endpoint_id"); err != nil {
		return db.ListEventsParams{}, 0, err
	}
	page, err := httpx.ParsePage(c)
	if err != nil {
		return db.ListEventsParams{}, 0, err
	}
	cursorAt, cursorID := page.CursorArgs()
	// One row beyond the page shows whether a next page exists.
	params := db.ListEventsParams{
		UserID: httpx.UserID(c), CursorCreatedAt: cursorAt, CursorID: cursorID, PageLimit: page.Limit + 1,
	}

	if v := c.Query("event_type"); v != "" {
		if err := ValidateEventType(v); err != nil {
			return db.ListEventsParams{}, 0, httpx.BadRequest("event_type filter is not a valid event type.")
		}
		params.EventType = &v
	}
	if v := c.Query("status"); v != "" {
		if !validStatuses[v] {
			return db.ListEventsParams{}, 0, httpx.BadRequest("status must be one of no_subscribers, in_progress, delivered, failed.")
		}
		params.Status = &v
	}
	if v := strings.ToLower(c.Query("id")); v != "" {
		if !uuidPrefixRE.MatchString(v) {
			return db.ListEventsParams{}, 0, httpx.BadRequest("id must be a full or partial event ID.")
		}
		params.IDPrefix = &v
	}
	if raw := c.Query("endpoint_id"); raw != "" {
		eid, perr := uuid.Parse(raw)
		if perr != nil {
			return db.ListEventsParams{}, 0, httpx.BadRequest("endpoint_id must be a UUID.")
		}
		params.EndpointID = &eid
	}
	if params.CreatedFrom, err = httpx.QueryTime(c, "from"); err != nil {
		return db.ListEventsParams{}, 0, err
	}
	if params.CreatedTo, err = httpx.QueryTime(c, "to"); err != nil {
		return db.ListEventsParams{}, 0, err
	}
	return params, page.Limit, nil
}

// Detail is the response of GET /events/:id.
type Detail struct {
	Summary
	Payload    json.RawMessage      `json:"payload"`
	Deliveries []deliveries.Summary `json:"deliveries"`
}

// maxDeliveriesPerEvent bounds the deliveries embedded in an event. An event
// has at most one delivery per endpoint, and endpoints per user are few.
const maxDeliveriesPerEvent = 100

// Get handles GET /events/:id.
func (h *Handler) Get(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "Event")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	userID := httpx.UserID(c)
	row, err := h.queries.GetEvent(c.Request.Context(), db.GetEventParams{ID: id, UserID: userID})
	if database.IsNotFound(err) {
		httpx.WriteError(c, httpx.NotFound("Event"))
		return
	}
	if err != nil {
		httpx.WriteError(c, fmt.Errorf("get event: %w", err))
		return
	}
	deliveryRows, err := h.queries.ListDeliveries(c.Request.Context(), db.ListDeliveriesParams{
		UserID: userID, EventID: &id, PageLimit: maxDeliveriesPerEvent,
	})
	if err != nil {
		httpx.WriteError(c, fmt.Errorf("list event deliveries: %w", err))
		return
	}
	detail := Detail{
		Summary: Summary{
			ID: row.ID, EventType: row.EventType, Status: row.Status, DeliveryCount: row.DeliveryCount,
			IdempotencyKey: row.IdempotencyKey, CreatedAt: row.CreatedAt,
		},
		Payload:    row.Payload,
		Deliveries: make([]deliveries.Summary, 0, len(deliveryRows)),
	}
	for _, d := range deliveryRows {
		detail.Deliveries = append(detail.Deliveries, deliveries.SummaryFromRow(d))
	}
	c.JSON(http.StatusOK, gin.H{"event": detail})
}
