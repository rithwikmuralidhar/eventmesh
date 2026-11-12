package httpapi

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"eventmesh/internal/ai"
	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/ratelimit"
)

// buildAI constructs the optional AI service. With no provider or key no
// client is created at all, so nothing can call out. A fake Provider can be
// injected through Deps (tests only).
func buildAI(ctx context.Context, d Deps, limiter *ratelimit.Limiter) (*ai.Service, error) {
	cfg := d.Config.AI
	provider := d.AIProvider
	if provider == nil && cfg.Configured() {
		g, err := ai.NewGemini(ctx, cfg.APIKey, cfg.ChatModel, cfg.EmbedModel, cfg.EmbedDimensions)
		if err != nil {
			return nil, err
		}
		provider = g
	}
	var docs ai.DocStore
	switch {
	case d.AIDocs != nil:
		docs = d.AIDocs
	case cfg.Configured() && cfg.FeatureDocs:
		if err := ai.EnsureDocsSchema(ctx, d.Pool, cfg.EmbedDimensions); err != nil {
			d.Logger.Warn("AI docs feature unavailable", "error", err.Error())
		} else {
			docs = ai.NewPgVectorStore(d.Pool)
		}
	}
	return ai.NewService(ai.Deps{Config: cfg, Provider: provider, Limiter: limiter, Pool: d.Pool, Docs: docs}), nil
}

// endpointOwner reports whether an endpoint belongs to the user.
func endpointOwner(q *db.Queries) ai.OwnsEndpoint {
	return func(ctx context.Context, userID, endpointID uuid.UUID) (bool, error) {
		_, err := q.GetEndpoint(ctx, db.GetEndpointParams{ID: endpointID, UserID: userID})
		if database.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("check endpoint owner: %w", err)
		}
		return true, nil
	}
}
