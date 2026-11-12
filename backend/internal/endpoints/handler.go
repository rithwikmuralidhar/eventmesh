package endpoints

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"eventmesh/internal/httpx"
)

func jsonUnmarshalInt(data []byte, v *int32) error { return json.Unmarshal(data, v) }

// Handler serves the /endpoints routes.
type Handler struct {
	service *Service
}

// NewHandler returns the endpoint handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Create handles POST /endpoints. The secret in this response is shown once.
func (h *Handler) Create(c *gin.Context) {
	var in CreateInput
	if err := httpx.DecodeJSON(c, &in); err != nil {
		httpx.WriteError(c, err)
		return
	}
	endpoint, secret, err := h.service.Create(c.Request.Context(), httpx.UserID(c), in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"endpoint": endpoint, "secret": secret})
}

// List handles GET /endpoints.
func (h *Handler) List(c *gin.Context) {
	items, err := h.service.List(c.Request.Context(), httpx.UserID(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Get handles GET /endpoints/:id.
func (h *Handler) Get(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "Endpoint")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	endpoint, err := h.service.Get(c.Request.Context(), httpx.UserID(c), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"endpoint": endpoint})
}

// Update handles PATCH /endpoints/:id.
func (h *Handler) Update(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "Endpoint")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	var in UpdateInput
	if err := httpx.DecodeJSON(c, &in); err != nil {
		httpx.WriteError(c, err)
		return
	}
	endpoint, err := h.service.Update(c.Request.Context(), httpx.UserID(c), id, in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"endpoint": endpoint})
}

// RotateSecret handles POST /endpoints/:id/rotate-secret.
func (h *Handler) RotateSecret(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "Endpoint")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	secret, err := h.service.RotateSecret(c.Request.Context(), httpx.UserID(c), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"secret": secret})
}

// Delete handles DELETE /endpoints/:id.
func (h *Handler) Delete(c *gin.Context) {
	id, err := httpx.PathUUID(c, "id", "Endpoint")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.service.Delete(c.Request.Context(), httpx.UserID(c), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
