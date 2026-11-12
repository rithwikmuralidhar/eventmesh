// Package httpx holds the HTTP conventions shared by every handler: the one
// error shape, strict JSON decoding, the authenticated user in the request
// context, and keyset pagination cursors.
package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"eventmesh/internal/observability"
)

// Context keys set by the middleware.
const (
	ctxUserID    = "eventmesh.user_id"
	ctxSessionID = "eventmesh.session_id"
	ctxRequestID = "eventmesh.request_id"
)

// Pagination limits.
const (
	DefaultPageLimit = 25
	MaxPageLimit     = 100
)

// Error is an error with a defined HTTP representation. Services return it
// for expected failures (validation, not found, conflict); anything else is
// treated as an internal error and never shown to the client.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError builds an Error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// BadRequest is a 400 with code "invalid_request".
func BadRequest(message string) *Error {
	return NewError(http.StatusBadRequest, "invalid_request", message)
}

// Validation is a 400 carrying per-field messages.
func Validation(fields map[string]string) *Error {
	details := make(map[string]any, len(fields))
	for k, v := range fields {
		details[k] = v
	}
	return &Error{
		Status:  http.StatusBadRequest,
		Code:    "validation_failed",
		Message: "The request contains invalid fields.",
		Details: map[string]any{"fields": details},
	}
}

// NotFound is the 404 used both for missing resources and for resources
// owned by another user, so IDs cannot be probed.
func NotFound(resource string) *Error {
	return NewError(http.StatusNotFound, "not_found", resource+" not found")
}

// Conflict is a 409 with a specific code.
func Conflict(code, message string) *Error {
	return NewError(http.StatusConflict, code, message)
}

// Unauthorized is a 401.
func Unauthorized(message string) *Error {
	return NewError(http.StatusUnauthorized, "unauthorized", message)
}

type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	RequestID string         `json:"request_id"`
}

// WriteError sends the single error shape used by the whole API. An error
// that is not an *Error is logged with its full detail and reported to the
// client only as a generic 500, so SQL, stack traces and internals never leak.
func WriteError(c *gin.Context, err error) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		observability.Logger(c.Request.Context()).Error("internal error", "error", err.Error())
		apiErr = NewError(http.StatusInternalServerError, "internal_error", "An internal error occurred.")
	}
	details := apiErr.Details
	if details == nil {
		details = map[string]any{}
	}
	c.AbortWithStatusJSON(apiErr.Status, errorBody{Error: errorPayload{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		Details:   details,
		RequestID: RequestID(c),
	}})
}

// DecodeJSON reads the request body into dst. It rejects unknown fields,
// trailing data and oversized bodies (the size cap itself is installed by the
// body-limit middleware).
func DecodeJSON(c *gin.Context, dst any) error {
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return BadRequest("Request body must contain a single JSON object.")
	}
	return nil
}

func decodeError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return NewError(http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("Request body must not exceed %d bytes.", maxErr.Limit))
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return BadRequest(fmt.Sprintf("Field %q has the wrong type.", typeErr.Field))
	}
	if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
		return BadRequest("Unknown field " + strings.TrimPrefix(msg, "json: unknown field ") + ".")
	}
	if errors.Is(err, io.EOF) {
		return BadRequest("Request body is required.")
	}
	return BadRequest("Request body is not valid JSON.")
}

// SetUser records the authenticated user for later handlers.
func SetUser(c *gin.Context, userID uuid.UUID) { c.Set(ctxUserID, userID) }

// UserID returns the authenticated user. It is only called behind an
// authentication middleware; the zero UUID it returns otherwise matches no row.
func UserID(c *gin.Context) uuid.UUID {
	if v, ok := c.Get(ctxUserID); ok {
		if id, ok := v.(uuid.UUID); ok {
			return id
		}
	}
	return uuid.Nil
}

// SetSessionID records the current session.
func SetSessionID(c *gin.Context, id uuid.UUID) { c.Set(ctxSessionID, id) }

// SessionID returns the current session ID.
func SessionID(c *gin.Context) uuid.UUID {
	if v, ok := c.Get(ctxSessionID); ok {
		if id, ok := v.(uuid.UUID); ok {
			return id
		}
	}
	return uuid.Nil
}

// SetRequestID records the request ID.
func SetRequestID(c *gin.Context, id string) { c.Set(ctxRequestID, id) }

// RequestID returns the request ID.
func RequestID(c *gin.Context) string { return c.GetString(ctxRequestID) }

// PathUUID parses a UUID path parameter. A malformed ID gets the same 404 as
// a missing resource: it cannot name anything that exists.
func PathUUID(c *gin.Context, param, resource string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(param))
	if err != nil {
		return uuid.Nil, NotFound(resource)
	}
	return id, nil
}

// Cursor is a keyset pagination position: the (created_at, id) of the last
// row of the previous page.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// EncodeCursor returns the opaque form sent to clients as next_cursor.
func EncodeCursor(createdAt time.Time, id uuid.UUID) string {
	raw := strconv.FormatInt(createdAt.UTC().UnixMicro(), 10) + "," + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor produced by EncodeCursor.
func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, BadRequest("cursor is not valid.")
	}
	microStr, idStr, found := strings.Cut(string(raw), ",")
	if !found {
		return Cursor{}, BadRequest("cursor is not valid.")
	}
	micros, err := strconv.ParseInt(microStr, 10, 64)
	if err != nil {
		return Cursor{}, BadRequest("cursor is not valid.")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return Cursor{}, BadRequest("cursor is not valid.")
	}
	return Cursor{CreatedAt: time.UnixMicro(micros).UTC(), ID: id}, nil
}

// Page is the pagination input common to list routes.
type Page struct {
	Limit  int32
	Cursor *Cursor
}

// ParsePage reads ?limit= and ?cursor=.
func ParsePage(c *gin.Context) (Page, error) {
	page := Page{Limit: DefaultPageLimit}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > MaxPageLimit {
			return Page{}, BadRequest(fmt.Sprintf("limit must be an integer between 1 and %d.", MaxPageLimit))
		}
		page.Limit = int32(n)
	}
	if raw := c.Query("cursor"); raw != "" {
		cur, err := DecodeCursor(raw)
		if err != nil {
			return Page{}, err
		}
		page.Cursor = &cur
	}
	return page, nil
}

// CursorArgs splits an optional cursor into the two nullable query arguments.
func (p Page) CursorArgs() (*time.Time, *uuid.UUID) {
	if p.Cursor == nil {
		return nil, nil
	}
	return &p.Cursor.CreatedAt, &p.Cursor.ID
}

// QueryTime parses an optional RFC 3339 query parameter.
func QueryTime(c *gin.Context, name string) (*time.Time, error) {
	raw := c.Query(name)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, BadRequest(name + " must be an RFC 3339 timestamp, for example 2026-01-31T00:00:00Z.")
	}
	return &t, nil
}

// AllowQuery rejects query parameters a route does not define, so a typo in
// a filter name is an error instead of a silently unfiltered list.
func AllowQuery(c *gin.Context, allowed ...string) error {
	for name := range c.Request.URL.Query() {
		known := false
		for _, a := range allowed {
			if name == a {
				known = true
				break
			}
		}
		if !known {
			return BadRequest(fmt.Sprintf("Unknown query parameter %q.", name))
		}
	}
	return nil
}
