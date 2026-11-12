package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/audit"
	"eventmesh/internal/config"
	"eventmesh/internal/database/db"
	"eventmesh/internal/httpx"
	"eventmesh/internal/observability"
	"eventmesh/internal/ratelimit"
)

// Features.
const (
	FeatureDocs    = "docs"
	FeatureExplain = "explain"
	FeatureSearch  = "search"
)

// aiLimiterKey is the single token bucket every AI call draws from. The
// provider quota belongs to the API key, so the bucket is global, not per user.
const aiLimiterKey = "ai"

// Limiter is the part of ratelimit.Limiter the AI package uses.
type Limiter interface {
	Allow(ctx context.Context, key string, ratePerSecond float64, burst int) (ratelimit.Decision, error)
}

// Deps are the collaborators of Service.
type Deps struct {
	Config   config.AI
	Provider Provider // nil when AI is not configured; no client is ever built then
	Limiter  Limiter
	Pool     *pgxpool.Pool
	Docs     DocStore // nil when the docs feature is unavailable
}

// Service is the AI feature facade: gating, limits, rate limiting and audit
// in one place, so no feature can skip them.
type Service struct {
	cfg      config.AI
	provider Provider
	limiter  Limiter
	pool     *pgxpool.Pool
	queries  *db.Queries
	docs     DocStore
	now      func() time.Time
}

// NewService returns the AI service. A nil Provider is valid and means "not
// configured": every feature then answers ai_not_configured.
func NewService(d Deps) *Service {
	s := &Service{cfg: d.Config, provider: d.Provider, limiter: d.Limiter, pool: d.Pool, docs: d.Docs, now: time.Now}
	if d.Pool != nil {
		s.queries = db.New(d.Pool)
	}
	if !d.Config.Configured() {
		s.provider = nil // never call out without provider + key
	}
	return s
}

// Available reports, per feature, whether a request would be served: the flag
// is on, the provider and key are set and (for docs) the index store exists.
// It feeds GET /settings and never exposes the key.
func (s *Service) Available() map[string]bool {
	ok := s.cfg.Configured() && s.provider != nil
	return map[string]bool{
		FeatureDocs:    ok && s.cfg.FeatureDocs && s.docs != nil,
		FeatureExplain: ok && s.cfg.FeatureExplain,
		FeatureSearch:  ok && s.cfg.FeatureSearch,
	}
}

// Errors returned to clients.
var (
	errFeatureDisabled = httpx.NewError(http.StatusServiceUnavailable, "feature_disabled", "This AI feature is turned off.")
	errNotConfigured   = httpx.NewError(http.StatusServiceUnavailable, "ai_not_configured", "No AI provider key is configured.")
	errDocsUnavailable = httpx.NewError(http.StatusServiceUnavailable, "docs_not_indexed", "The documentation index is not available (pgvector missing or docs not indexed).")
)

// gate checks the feature flag, then the provider and key. It runs before any
// work, so a disabled or unconfigured feature can never reach the provider.
func (s *Service) gate(feature string) error {
	var enabled bool
	switch feature {
	case FeatureDocs:
		enabled = s.cfg.FeatureDocs
	case FeatureExplain:
		enabled = s.cfg.FeatureExplain
	case FeatureSearch:
		enabled = s.cfg.FeatureSearch
	}
	if !enabled {
		return errFeatureDisabled
	}
	if !s.cfg.Configured() || s.provider == nil {
		return errNotConfigured
	}
	return nil
}

// throttle takes one token from the shared AI bucket.
func (s *Service) throttle(ctx context.Context, wait bool) error {
	if s.limiter == nil {
		return nil
	}
	rate := float64(s.cfg.RequestsPerMin) / 60
	for {
		dec, err := s.limiter.Allow(ctx, aiLimiterKey, rate, s.cfg.RequestsPerMin)
		if err != nil {
			return httpx.NewError(http.StatusServiceUnavailable, "ai_unavailable", "The AI rate limiter is unavailable.")
		}
		if dec.Allowed {
			return nil
		}
		if !wait {
			e := httpx.NewError(http.StatusTooManyRequests, "ai_rate_limited", "Too many AI requests; try again shortly.")
			e.Details = map[string]any{"retry_after_ms": dec.RetryAfter.Milliseconds()}
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dec.RetryAfter + 50*time.Millisecond):
		}
	}
}

// generate enforces the limits and records the audit row. The prompt is cut
// to MaxInputChars and the output to MaxOutputTokens before the call.
func (s *Service) generate(ctx context.Context, userID uuid.UUID, feature string, req GenerateRequest) (GenerateResponse, error) {
	if err := s.throttle(ctx, false); err != nil {
		s.audit(ctx, userID, feature, "", 0, 0, "rate_limited")
		return GenerateResponse{}, err
	}
	req.Prompt = Truncate(req.Prompt, s.cfg.MaxInputChars)
	if req.MaxOutputTokens <= 0 || req.MaxOutputTokens > s.cfg.MaxOutputTokens {
		req.MaxOutputTokens = s.cfg.MaxOutputTokens
	}
	var resp GenerateResponse
	var err error
	for attempt := 0; ; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		resp, err = s.provider.Generate(callCtx, req)
		cancel()
		if err == nil || !isTransient(err) || attempt >= len(transientBackoffs) {
			break
		}
		observability.Logger(ctx).Warn("ai provider temporarily unavailable; retrying",
			"feature", feature, "attempt", attempt+1, "wait", transientBackoffs[attempt].String())
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(transientBackoffs[attempt]):
			continue
		}
		break
	}
	if err != nil {
		s.audit(ctx, userID, feature, s.cfg.ChatModel, 0, 0, "provider_error")
		observability.Logger(ctx).Warn("ai generate failed", "feature", feature, "error", err.Error())
		return GenerateResponse{}, httpx.NewError(http.StatusBadGateway, "ai_provider_error", "The AI provider could not answer. Try again later.")
	}
	s.audit(ctx, userID, feature, resp.Model, resp.InputTokens, resp.OutputTokens, "ok")
	return resp, nil
}

// embed is the audited, rate-limited embedding call. wait=true (the indexer)
// sleeps for a token instead of failing.
func (s *Service) embed(ctx context.Context, userID uuid.UUID, feature string, texts []string, task EmbedTask, wait bool) ([][]float32, error) {
	vecs, _, err := s.embedTimed(ctx, userID, feature, texts, task, wait)
	return vecs, err
}

// maxQuotaRetries bounds how often a waiting caller (indexer, evaluation)
// retries after the provider says its quota is exhausted.
// providerTimeout bounds one provider call, so a slow or hung provider cannot hold a user request open.
// (A live run saw Google answer 504 after ~88 s.) A variable so tests can shorten it.
var providerTimeout = 30 * time.Second

const maxQuotaRetries = 8

// quotaRetryPad is added to the provider's suggested delay (a variable so tests can zero it).
var quotaRetryPad = 2 * time.Second

var retryDelayRE = regexp.MustCompile(`(?i)retry(?: in |Delay:\s*)([0-9.]+)\s*s`)

// quotaRetryDelay reports whether err is a provider quota error (HTTP 429 /
// RESOURCE_EXHAUSTED) and how long the provider asked us to wait.
func quotaRetryDelay(err error) (time.Duration, bool) {
	msg := err.Error()
	if !strings.Contains(msg, "429") && !strings.Contains(msg, "RESOURCE_EXHAUSTED") {
		return 0, false
	}
	if m := retryDelayRE.FindStringSubmatch(msg); m != nil {
		if secs, perr := strconv.ParseFloat(m[1], 64); perr == nil {
			return time.Duration(secs*float64(time.Second)) + quotaRetryPad, true
		}
	}
	return 30 * time.Second, true
}

// embedTimed is embed plus the duration of the successful provider call alone
// (excluding limiter waits and quota back-off), so latency numbers are honest.
// With wait=true a provider quota error is retried after the delay the provider
// suggested; with wait=false it fails at once.
func (s *Service) embedTimed(ctx context.Context, userID uuid.UUID, feature string, texts []string, task EmbedTask, wait bool) ([][]float32, time.Duration, error) {
	limited := make([]string, len(texts))
	for i, t := range texts {
		limited[i] = Truncate(t, s.cfg.MaxInputChars)
	}
	for attempt := 0; ; attempt++ {
		if err := s.throttle(ctx, wait); err != nil {
			if userID != uuid.Nil {
				s.audit(ctx, userID, feature, "", 0, 0, "rate_limited")
			}
			return nil, 0, err
		}
		start := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		vecs, err := s.provider.Embed(callCtx, limited, task)
		cancel()
		took := time.Since(start)
		if err == nil {
			if userID != uuid.Nil {
				s.audit(ctx, userID, feature, s.cfg.EmbedModel, 0, 0, "ok")
			}
			return vecs, took, nil
		}
		if delay, quota := quotaRetryDelay(err); wait && quota && attempt < maxQuotaRetries {
			observability.Logger(ctx).Warn("ai provider quota reached; waiting before retry",
				"feature", feature, "wait", delay.String(), "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		if userID != uuid.Nil {
			s.audit(ctx, userID, feature, s.cfg.EmbedModel, 0, 0, "provider_error")
		}
		observability.Logger(ctx).Warn("ai embed failed", "feature", feature, "error", err.Error())
		return nil, 0, httpx.NewError(http.StatusBadGateway, "ai_provider_error", "The AI provider could not answer. Try again later.")
	}
}

// audit writes one audit row: user, feature, model, token counts and outcome.
// The prompt, the question and the model output are deliberately not stored.
func (s *Service) audit(ctx context.Context, userID uuid.UUID, feature, model string, in, out int, outcome string) {
	if s.queries == nil {
		return
	}
	meta := map[string]any{"feature": feature, "model": model, "outcome": outcome}
	if in > 0 || out > 0 {
		meta["input_tokens"], meta["output_tokens"] = in, out
	}
	bg := context.WithoutCancel(ctx)
	if err := audit.Record(bg, s.queries, audit.Entry{
		UserID: userID, Action: audit.ActionAIRequest, ResourceType: audit.ResourceAI, Metadata: meta,
	}); err != nil && !errors.Is(err, context.Canceled) {
		observability.Logger(ctx).Warn("ai audit write failed", "error", fmt.Sprint(err))
	}
}

// transientBackoffs are the waits before retrying a provider that answered
// 503 / UNAVAILABLE ("high demand"). Two short retries, then give up, so a real
// outage still fails within a few seconds. A variable so tests can zero it.
var transientBackoffs = []time.Duration{time.Second, 3 * time.Second}

// isTransient reports a temporary provider capacity error (HTTP 503).
func isTransient(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "503") || strings.Contains(msg, "UNAVAILABLE")
}
