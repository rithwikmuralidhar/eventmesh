// Package audit records account activity. Every mutating management action
// writes one row here, in the same transaction as the change itself, so the
// trail can never disagree with what actually happened.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/database/db"
	"eventmesh/internal/httpx"
)

// Actions. The value is what the Activity page shows.
const (
	ActionUserRegistered    = "user.registered"
	ActionSessionCreated    = "session.created"
	ActionSessionEnded      = "session.ended"
	ActionPasswordChanged   = "user.password_changed"
	ActionAPIKeyCreated     = "api_key.created"
	ActionAPIKeyRevoked     = "api_key.revoked"
	ActionEndpointCreated   = "endpoint.created"
	ActionEndpointUpdated   = "endpoint.updated"
	ActionEndpointDeleted   = "endpoint.deleted"
	ActionEndpointSecretRot = "endpoint.secret_rotated"
	ActionDeliveryReplayed  = "delivery.replayed"
	ActionAIRequest         = "ai.request"
	ResourceAI              = "ai"
	ResourceUser            = "user"
	ResourceSession         = "session"
	ResourceAPIKey          = "api_key"
	ResourceEndpoint        = "endpoint"
	ResourceDelivery        = "delivery"
)

// Entry is one audit record to write.
type Entry struct {
	UserID       uuid.UUID
	Action       string
	ResourceType string
	ResourceID   uuid.UUID
	// Metadata must never contain secrets, keys, tokens or payloads.
	Metadata map[string]any
}

// Record writes an entry using q, which the caller has bound to the
// transaction performing the audited change.
func Record(ctx context.Context, q *db.Queries, e Entry) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate audit id: %w", err)
	}
	metadata := e.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	var resourceID *uuid.UUID
	if e.ResourceID != uuid.Nil {
		resourceID = &e.ResourceID
	}
	err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{
		ID:           id,
		UserID:       e.UserID,
		Action:       e.Action,
		ResourceType: e.ResourceType,
		ResourceID:   resourceID,
		Metadata:     raw,
	})
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

// Handler serves the audit log read API.
type Handler struct {
	queries *db.Queries
}

// NewHandler returns the audit log handler.
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{queries: db.New(pool)}
}

type logJSON struct {
	ID           uuid.UUID       `json:"id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   *uuid.UUID      `json:"resource_id"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
}

// List handles GET /audit-logs.
func (h *Handler) List(c *gin.Context) {
	if err := httpx.AllowQuery(c, "limit", "cursor"); err != nil {
		httpx.WriteError(c, err)
		return
	}
	page, err := httpx.ParsePage(c)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	cursorAt, cursorID := page.CursorArgs()
	// One extra row tells us whether another page exists without a COUNT.
	rows, err := h.queries.ListAuditLogs(c.Request.Context(), db.ListAuditLogsParams{
		UserID:          httpx.UserID(c),
		CursorCreatedAt: cursorAt,
		CursorID:        cursorID,
		PageLimit:       page.Limit + 1,
	})
	if err != nil {
		httpx.WriteError(c, fmt.Errorf("list audit logs: %w", err))
		return
	}
	var next *string
	if len(rows) > int(page.Limit) {
		rows = rows[:page.Limit]
		last := rows[len(rows)-1]
		cur := httpx.EncodeCursor(last.CreatedAt, last.ID)
		next = &cur
	}
	items := make([]logJSON, 0, len(rows))
	for _, r := range rows {
		items = append(items, logJSON{
			ID: r.ID, Action: r.Action, ResourceType: r.ResourceType, ResourceID: r.ResourceID,
			Metadata: r.Metadata, CreatedAt: r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "next_cursor": next})
}
