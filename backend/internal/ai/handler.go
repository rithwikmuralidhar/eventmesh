package ai

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"eventmesh/internal/httpx"
)

// Handler serves the three AI endpoints. All are session-authenticated (the
// router mounts them behind RequireSession and CSRF).
type Handler struct {
	svc  *Service
	owns OwnsEndpoint
}

// NewHandler returns the AI handler. owns checks endpoint ownership for search.
func NewHandler(svc *Service, owns OwnsEndpoint) *Handler { return &Handler{svc: svc, owns: owns} }

// AskDocs handles POST /ai/docs/ask.
func (h *Handler) AskDocs(c *gin.Context) {
	var req struct {
		Question string `json:"question"`
	}
	// Feature gate first, so a disabled feature answers 503 whatever the body.
	if err := h.svc.gate(FeatureDocs); err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := httpx.DecodeJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	ans, err := h.svc.AskDocs(c.Request.Context(), httpx.UserID(c), req.Question)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, ans)
}

// Explain handles POST /deliveries/:id/explain.
func (h *Handler) Explain(c *gin.Context) {
	if err := h.svc.gate(FeatureExplain); err != nil {
		httpx.WriteError(c, err)
		return
	}
	id, err := httpx.PathUUID(c, "id", "Delivery")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	var req struct {
		IncludeExcerpt bool `json:"include_response_excerpt"`
	}
	if c.Request.ContentLength != 0 {
		if err := httpx.DecodeJSON(c, &req); err != nil {
			httpx.WriteError(c, err)
			return
		}
	}
	out, err := h.svc.Explain(c.Request.Context(), httpx.UserID(c), id, req.IncludeExcerpt)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"explanation": out})
}

// SearchEvents handles POST /ai/search/events.
func (h *Handler) SearchEvents(c *gin.Context) {
	if err := h.svc.gate(FeatureSearch); err != nil {
		httpx.WriteError(c, err)
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := httpx.DecodeJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	out, err := h.svc.SearchEvents(c.Request.Context(), httpx.UserID(c), req.Query, h.owns)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
