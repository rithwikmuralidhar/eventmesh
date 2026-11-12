package apikeys

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/audit"
	"eventmesh/internal/clock"
	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/httpx"
)

// lastUsedInterval is how stale last_used_at may get. Ingestion is the hot
// path; it must not write to the key row on every request.
const lastUsedInterval = time.Minute

// ErrInvalidKey means the presented key is malformed, unknown or revoked.
// Callers answer all three with the same 401.
var ErrInvalidKey = errors.New("invalid api key")

// Service manages API keys.
type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	clock   clock.Clock
}

// NewService returns the API key service.
func NewService(pool *pgxpool.Pool, clk clock.Clock) *Service {
	return &Service{pool: pool, queries: db.New(pool), clock: clk}
}

// Key is the stored, non-secret view of an API key.
type Key struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// Create makes a new key and returns it with the plaintext, which exists
// only in this return value and is never stored or logged.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, name string) (Key, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return Key{}, "", httpx.Validation(map[string]string{"name": "name must be between 1 and 100 characters"})
	}
	generated, err := Generate()
	if err != nil {
		return Key{}, "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Key{}, "", fmt.Errorf("generate key id: %w", err)
	}

	var key Key
	err = database.InTx(ctx, s.pool, func(q *db.Queries) error {
		row, err := q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
			ID: id, UserID: userID, Name: name, KeyPrefix: generated.Prefix, KeyHash: generated.Hash,
		})
		if err != nil {
			return fmt.Errorf("create api key: %w", err)
		}
		key = Key{ID: row.ID, Name: row.Name, Prefix: row.KeyPrefix, CreatedAt: row.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionAPIKeyCreated, ResourceType: audit.ResourceAPIKey, ResourceID: id,
			Metadata: map[string]any{"name": name, "prefix": generated.Prefix},
		})
	})
	if err != nil {
		return Key{}, "", err
	}
	return key, generated.Plaintext, nil
}

// List returns the user's keys, newest first.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Key, error) {
	rows, err := s.queries.ListAPIKeys(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	keys := make([]Key, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, Key{
			ID: r.ID, Name: r.Name, Prefix: r.KeyPrefix,
			LastUsedAt: r.LastUsedAt, CreatedAt: r.CreatedAt, RevokedAt: r.RevokedAt,
		})
	}
	return keys, nil
}

// Revoke disables a key. The query is scoped by user, so another user's key
// ID yields "not found".
func (s *Service) Revoke(ctx context.Context, userID, keyID uuid.UUID) error {
	return database.InTx(ctx, s.pool, func(q *db.Queries) error {
		row, err := q.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{ID: keyID, UserID: userID})
		if database.IsNotFound(err) {
			return httpx.NotFound("API key")
		}
		if err != nil {
			return fmt.Errorf("revoke api key: %w", err)
		}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionAPIKeyRevoked, ResourceType: audit.ResourceAPIKey, ResourceID: keyID,
			Metadata: map[string]any{"name": row.Name, "prefix": row.KeyPrefix},
		})
	})
}

// Authenticate resolves a presented key to its owner.
func (s *Service) Authenticate(ctx context.Context, plaintext string) (uuid.UUID, error) {
	if _, err := Parse(plaintext); err != nil {
		return uuid.Nil, ErrInvalidKey
	}
	row, err := s.queries.GetAPIKeyByHash(ctx, Hash(plaintext))
	if database.IsNotFound(err) {
		return uuid.Nil, ErrInvalidKey
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("look up api key: %w", err)
	}
	if row.RevokedAt != nil {
		return uuid.Nil, ErrInvalidKey
	}
	if row.LastUsedAt == nil || s.clock.Now().Sub(*row.LastUsedAt) >= lastUsedInterval {
		if err := s.queries.TouchAPIKeyLastUsed(ctx, row.ID); err != nil {
			return uuid.Nil, fmt.Errorf("update api key last used: %w", err)
		}
	}
	return row.UserID, nil
}

// Handler serves the /api-keys routes and the API key middleware.
type Handler struct {
	service *Service
}

// NewHandler returns the API key handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type createRequest struct {
	Name string `json:"name"`
}

// Create handles POST /api-keys. The response is the only place the full key ever appears.
func (h *Handler) Create(c *gin.Context) {
	var req createRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	key, plaintext, err := h.service.Create(c.Request.Context(), httpx.UserID(c), req.Name)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"api_key": key, "key": plaintext})
}

// List handles GET /api-keys.
func (h *Handler) List(c *gin.Context) {
	keys, err := h.service.List(c.Request.Context(), httpx.UserID(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": keys})
}

// Revoke handles DELETE /api-keys/:id.
func (h *Handler) Revoke(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "API key")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.service.Revoke(c.Request.Context(), httpx.UserID(c), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RequireKey is the middleware for the ingestion route. It accepts
// "Authorization: Bearer em_..." and sets the key's owner as the user.
func (h *Handler) RequireKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		plaintext, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !ok {
			httpx.WriteError(c, httpx.Unauthorized("Provide an API key as \"Authorization: Bearer <key>\"."))
			return
		}
		userID, err := h.service.Authenticate(c.Request.Context(), strings.TrimSpace(plaintext))
		if errors.Is(err, ErrInvalidKey) {
			httpx.WriteError(c, httpx.Unauthorized("The API key is invalid or has been revoked."))
			return
		}
		if err != nil {
			httpx.WriteError(c, err)
			return
		}
		httpx.SetUser(c, userID)
		c.Next()
	}
}
