package endpoints

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/audit"
	"eventmesh/internal/database"
	"eventmesh/internal/database/db"
	"eventmesh/internal/events"
	"eventmesh/internal/httpx"
	"eventmesh/internal/realtime"
)

// Limits on endpoint settings. They mirror the CHECK constraints in the schema.
const (
	DefaultRateLimit   = 10
	MinRateLimit       = 1
	MaxRateLimit       = 1000
	MaxRetriesLimit    = 20
	MinRetryDelayMS    = 100
	MaxInitialDelayMS  = 3_600_000
	MaxMaxDelayMS      = 86_400_000
	MaxEventTypes      = 50
	MaxNameLen         = 100
	MaxURLLen          = 2048
	urlValidateTimeout = 5 * time.Second
)

// URLValidator is the early SSRF check; *ssrf.Policy implements it.
type URLValidator interface {
	ValidateURL(ctx context.Context, rawURL string) error
}

// Service manages webhook endpoints.
type Service struct {
	pool      *pgxpool.Pool
	queries   *db.Queries
	cipher    *SecretCipher
	validator URLValidator
	publisher realtime.Publisher
}

// NewService returns the endpoint service.
func NewService(pool *pgxpool.Pool, cipher *SecretCipher, validator URLValidator, publisher realtime.Publisher) *Service {
	return &Service{pool: pool, queries: db.New(pool), cipher: cipher, validator: validator, publisher: publisher}
}

// Endpoint is the API view of a webhook endpoint. It never includes the secret.
type Endpoint struct {
	ID                  uuid.UUID `json:"id"`
	Name                string    `json:"name"`
	URL                 string    `json:"url"`
	EventTypes          []string  `json:"event_types"`
	Enabled             bool      `json:"enabled"`
	RateLimitPerSecond  int32     `json:"rate_limit_per_second"`
	MaxRetries          *int32    `json:"max_retries"`
	InitialRetryDelayMS *int32    `json:"initial_retry_delay_ms"`
	MaxRetryDelayMS     *int32    `json:"max_retry_delay_ms"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	Stats               *Stats    `json:"stats,omitempty"`
}

// Stats are an endpoint's delivery counts.
type Stats struct {
	Total      int64 `json:"total"`
	Delivered  int64 `json:"delivered"`
	DeadLetter int64 `json:"dead_letter"`
	InProgress int64 `json:"in_progress"`
	Cancelled  int64 `json:"cancelled"`
}

// CreateInput is the body of POST /endpoints.
type CreateInput struct {
	Name                string   `json:"name"`
	URL                 string   `json:"url"`
	EventTypes          []string `json:"event_types"`
	Enabled             *bool    `json:"enabled"`
	RateLimitPerSecond  *int32   `json:"rate_limit_per_second"`
	MaxRetries          *int32   `json:"max_retries"`
	InitialRetryDelayMS *int32   `json:"initial_retry_delay_ms"`
	MaxRetryDelayMS     *int32   `json:"max_retry_delay_ms"`
}

// NullableInt distinguishes three PATCH cases for the retry overrides:
// field absent (leave unchanged), null (clear the override, inherit the
// system default) and a number (set the override).
type NullableInt struct {
	Set   bool
	Value *int32
}

// UnmarshalJSON records that the field was present.
func (n *NullableInt) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var v int32
	if err := jsonUnmarshalInt(data, &v); err != nil {
		return err
	}
	n.Value = &v
	return nil
}

// UpdateInput is the body of PATCH /endpoints/:id. Absent fields are unchanged.
type UpdateInput struct {
	Name                *string     `json:"name"`
	URL                 *string     `json:"url"`
	EventTypes          *[]string   `json:"event_types"`
	Enabled             *bool       `json:"enabled"`
	RateLimitPerSecond  *int32      `json:"rate_limit_per_second"`
	MaxRetries          NullableInt `json:"max_retries"`
	InitialRetryDelayMS NullableInt `json:"initial_retry_delay_ms"`
	MaxRetryDelayMS     NullableInt `json:"max_retry_delay_ms"`
}

// settings is the full set of validated, user-controlled endpoint fields.
type settings struct {
	name       string
	url        string
	eventTypes []string
	enabled    bool
	rateLimit  int32
	maxRetries *int32
	initialMS  *int32
	maxMS      *int32
}

// validate checks every field and returns all problems at once. checkURL is
// false when a PATCH did not touch the URL, so an unrelated edit is not
// refused because DNS is briefly unavailable.
func (s *Service) validate(ctx context.Context, in *settings, checkURL bool) error {
	fields := map[string]string{}

	in.name = strings.TrimSpace(in.name)
	if in.name == "" || utf8.RuneCountInString(in.name) > MaxNameLen {
		fields["name"] = fmt.Sprintf("name must be between 1 and %d characters", MaxNameLen)
	}

	in.url = strings.TrimSpace(in.url)
	switch {
	case in.url == "" || len(in.url) > MaxURLLen:
		fields["url"] = fmt.Sprintf("url must be between 1 and %d characters", MaxURLLen)
	case checkURL:
		// Early feedback only. The binding SSRF check runs in the worker at
		// connection time, on the address the socket actually uses.
		vctx, cancel := context.WithTimeout(ctx, urlValidateTimeout)
		if err := s.validator.ValidateURL(vctx, in.url); err != nil {
			fields["url"] = err.Error()
		}
		cancel()
	}

	in.eventTypes = dedupe(in.eventTypes)
	switch {
	case len(in.eventTypes) == 0:
		fields["event_types"] = "at least one event type pattern is required"
	case len(in.eventTypes) > MaxEventTypes:
		fields["event_types"] = fmt.Sprintf("at most %d event type patterns are allowed", MaxEventTypes)
	default:
		for _, p := range in.eventTypes {
			if err := events.ValidatePattern(p); err != nil {
				fields["event_types"] = err.Error()
				break
			}
		}
	}

	if in.rateLimit < MinRateLimit || in.rateLimit > MaxRateLimit {
		fields["rate_limit_per_second"] = fmt.Sprintf("rate limit must be between %d and %d", MinRateLimit, MaxRateLimit)
	}
	if in.maxRetries != nil && (*in.maxRetries < 0 || *in.maxRetries > MaxRetriesLimit) {
		fields["max_retries"] = fmt.Sprintf("max retries must be between 0 and %d", MaxRetriesLimit)
	}
	if in.initialMS != nil && (*in.initialMS < MinRetryDelayMS || *in.initialMS > MaxInitialDelayMS) {
		fields["initial_retry_delay_ms"] = fmt.Sprintf("initial retry delay must be between %d and %d ms", MinRetryDelayMS, MaxInitialDelayMS)
	}
	if in.maxMS != nil && (*in.maxMS < MinRetryDelayMS || *in.maxMS > MaxMaxDelayMS) {
		fields["max_retry_delay_ms"] = fmt.Sprintf("max retry delay must be between %d and %d ms", MinRetryDelayMS, MaxMaxDelayMS)
	}
	if in.initialMS != nil && in.maxMS != nil && *in.initialMS > *in.maxMS {
		fields["initial_retry_delay_ms"] = "initial retry delay must not exceed the max retry delay"
	}

	if len(fields) > 0 {
		return httpx.Validation(fields)
	}
	return nil
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// Create stores a new endpoint and returns it with its signing secret. This
// return value and RotateSecret are the only places the secret appears.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, in CreateInput) (Endpoint, string, error) {
	set := settings{
		name: in.Name, url: in.URL, eventTypes: in.EventTypes, enabled: true, rateLimit: DefaultRateLimit,
		maxRetries: in.MaxRetries, initialMS: in.InitialRetryDelayMS, maxMS: in.MaxRetryDelayMS,
	}
	if in.Enabled != nil {
		set.enabled = *in.Enabled
	}
	if in.RateLimitPerSecond != nil {
		set.rateLimit = *in.RateLimitPerSecond
	}
	if err := s.validate(ctx, &set, true); err != nil {
		return Endpoint{}, "", err
	}

	secret, ciphertext, err := s.newSecret()
	if err != nil {
		return Endpoint{}, "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Endpoint{}, "", fmt.Errorf("generate endpoint id: %w", err)
	}

	var row db.WebhookEndpoint
	err = database.InTx(ctx, s.pool, func(q *db.Queries) error {
		row, err = q.CreateEndpoint(ctx, db.CreateEndpointParams{
			ID: id, UserID: userID, Name: set.name, Url: set.url, EventTypes: set.eventTypes,
			SecretCiphertext: ciphertext, Enabled: set.enabled, RateLimitPerSecond: set.rateLimit,
			MaxRetries: set.maxRetries, InitialRetryDelayMs: set.initialMS, MaxRetryDelayMs: set.maxMS,
		})
		if err != nil {
			return fmt.Errorf("create endpoint: %w", err)
		}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionEndpointCreated, ResourceType: audit.ResourceEndpoint, ResourceID: id,
			Metadata: map[string]any{"name": set.name, "url": set.url},
		})
	})
	if err != nil {
		return Endpoint{}, "", err
	}
	return toEndpoint(row), secret, nil
}

func (s *Service) newSecret() (plaintext string, ciphertext []byte, err error) {
	plaintext, err = GenerateSecret()
	if err != nil {
		return "", nil, err
	}
	ciphertext, err = s.cipher.Encrypt(plaintext)
	if err != nil {
		return "", nil, fmt.Errorf("encrypt endpoint secret: %w", err)
	}
	return plaintext, ciphertext, nil
}

// Get returns one endpoint with its statistics.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (Endpoint, error) {
	row, err := s.queries.GetEndpoint(ctx, db.GetEndpointParams{ID: id, UserID: userID})
	if database.IsNotFound(err) {
		return Endpoint{}, httpx.NotFound("Endpoint")
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("get endpoint: %w", err)
	}
	stats, err := s.statsByEndpoint(ctx, userID)
	if err != nil {
		return Endpoint{}, err
	}
	ep := toEndpoint(row)
	ep.Stats = statsFor(stats, id)
	return ep, nil
}

// List returns the user's endpoints with per-endpoint statistics.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Endpoint, error) {
	rows, err := s.queries.ListEndpoints(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}
	stats, err := s.statsByEndpoint(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(rows))
	for _, row := range rows {
		ep := toEndpoint(row)
		ep.Stats = statsFor(stats, row.ID)
		out = append(out, ep)
	}
	return out, nil
}

func (s *Service) statsByEndpoint(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]Stats, error) {
	rows, err := s.queries.ListEndpointStats(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list endpoint stats: %w", err)
	}
	out := make(map[uuid.UUID]Stats, len(rows))
	for _, r := range rows {
		out[r.EndpointID] = Stats{
			Total: r.Total, Delivered: r.Delivered, DeadLetter: r.DeadLetter,
			InProgress: r.InProgress, Cancelled: r.Cancelled,
		}
	}
	return out, nil
}

func statsFor(all map[uuid.UUID]Stats, id uuid.UUID) *Stats {
	st := all[id] // zero counts for an endpoint with no deliveries yet
	return &st
}

// Update applies a PATCH. The row is read and written in one transaction so
// two concurrent edits cannot interleave field by field.
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, in UpdateInput) (Endpoint, error) {
	var updated db.WebhookEndpoint
	err := database.InTx(ctx, s.pool, func(q *db.Queries) error {
		current, err := q.GetEndpoint(ctx, db.GetEndpointParams{ID: id, UserID: userID})
		if database.IsNotFound(err) {
			return httpx.NotFound("Endpoint")
		}
		if err != nil {
			return fmt.Errorf("get endpoint: %w", err)
		}

		set := settings{
			name: current.Name, url: current.Url, eventTypes: current.EventTypes, enabled: current.Enabled,
			rateLimit: current.RateLimitPerSecond, maxRetries: current.MaxRetries,
			initialMS: current.InitialRetryDelayMs, maxMS: current.MaxRetryDelayMs,
		}
		changed := applyPatch(&set, in)
		urlChanged := in.URL != nil && strings.TrimSpace(*in.URL) != current.Url
		if err := s.validate(ctx, &set, urlChanged); err != nil {
			return err
		}

		updated, err = q.UpdateEndpoint(ctx, db.UpdateEndpointParams{
			ID: id, UserID: userID, Name: set.name, Url: set.url, EventTypes: set.eventTypes,
			Enabled: set.enabled, RateLimitPerSecond: set.rateLimit,
			MaxRetries: set.maxRetries, InitialRetryDelayMs: set.initialMS, MaxRetryDelayMs: set.maxMS,
		})
		if err != nil {
			return fmt.Errorf("update endpoint: %w", err)
		}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionEndpointUpdated, ResourceType: audit.ResourceEndpoint, ResourceID: id,
			Metadata: map[string]any{"name": set.name, "changed": changed},
		})
	})
	if err != nil {
		return Endpoint{}, err
	}
	return toEndpoint(updated), nil
}

// applyPatch overlays the fields present in the PATCH and returns their names.
func applyPatch(set *settings, in UpdateInput) []string {
	changed := []string{}
	if in.Name != nil {
		set.name = *in.Name
		changed = append(changed, "name")
	}
	if in.URL != nil {
		set.url = *in.URL
		changed = append(changed, "url")
	}
	if in.EventTypes != nil {
		set.eventTypes = *in.EventTypes
		changed = append(changed, "event_types")
	}
	if in.Enabled != nil {
		set.enabled = *in.Enabled
		changed = append(changed, "enabled")
	}
	if in.RateLimitPerSecond != nil {
		set.rateLimit = *in.RateLimitPerSecond
		changed = append(changed, "rate_limit_per_second")
	}
	if in.MaxRetries.Set {
		set.maxRetries = in.MaxRetries.Value
		changed = append(changed, "max_retries")
	}
	if in.InitialRetryDelayMS.Set {
		set.initialMS = in.InitialRetryDelayMS.Value
		changed = append(changed, "initial_retry_delay_ms")
	}
	if in.MaxRetryDelayMS.Set {
		set.maxMS = in.MaxRetryDelayMS.Value
		changed = append(changed, "max_retry_delay_ms")
	}
	return changed
}

// RotateSecret replaces the signing secret and returns the new one, once.
func (s *Service) RotateSecret(ctx context.Context, userID, id uuid.UUID) (string, error) {
	secret, ciphertext, err := s.newSecret()
	if err != nil {
		return "", err
	}
	err = database.InTx(ctx, s.pool, func(q *db.Queries) error {
		_, err := q.UpdateEndpointSecret(ctx, db.UpdateEndpointSecretParams{ID: id, UserID: userID, SecretCiphertext: ciphertext})
		if database.IsNotFound(err) {
			return httpx.NotFound("Endpoint")
		}
		if err != nil {
			return fmt.Errorf("rotate endpoint secret: %w", err)
		}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionEndpointSecretRot, ResourceType: audit.ResourceEndpoint, ResourceID: id,
		})
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}

// Delete soft-deletes an endpoint and cancels its open deliveries in the same
// transaction. History (deliveries and attempts) is kept.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	var cancelled []uuid.UUID
	err := database.InTx(ctx, s.pool, func(q *db.Queries) error {
		_, err := q.SoftDeleteEndpoint(ctx, db.SoftDeleteEndpointParams{ID: id, UserID: userID})
		if database.IsNotFound(err) {
			return httpx.NotFound("Endpoint")
		}
		if err != nil {
			return fmt.Errorf("delete endpoint: %w", err)
		}
		cancelled, err = q.CancelOpenDeliveriesForEndpoint(ctx, db.CancelOpenDeliveriesForEndpointParams{EndpointID: id, UserID: userID})
		if err != nil {
			return fmt.Errorf("cancel open deliveries: %w", err)
		}
		return audit.Record(ctx, q, audit.Entry{
			UserID: userID, Action: audit.ActionEndpointDeleted, ResourceType: audit.ResourceEndpoint, ResourceID: id,
			Metadata: map[string]any{"cancelled_deliveries": len(cancelled)},
		})
	})
	if err != nil {
		return err
	}
	// The cancelled jobs are left in Redis on purpose: a worker that picks one
	// up fails the row claim (status is no longer pending) and drops it.
	for _, deliveryID := range cancelled {
		s.publisher.Publish(ctx, userID, realtime.Update{
			Type: realtime.TypeDeliveryUpdated, DeliveryID: deliveryID.String(), EndpointID: id.String(), Status: "cancelled",
		})
	}
	return nil
}

func toEndpoint(row db.WebhookEndpoint) Endpoint {
	return Endpoint{
		ID: row.ID, Name: row.Name, URL: row.Url, EventTypes: row.EventTypes, Enabled: row.Enabled,
		RateLimitPerSecond: row.RateLimitPerSecond, MaxRetries: row.MaxRetries,
		InitialRetryDelayMS: row.InitialRetryDelayMs, MaxRetryDelayMS: row.MaxRetryDelayMs,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
