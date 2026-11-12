// Package middleware holds the cross-cutting HTTP concerns: request IDs and
// logging, panic recovery, body limits, CORS, CSRF and rate limiting.
package middleware

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"eventmesh/internal/httpx"
	"eventmesh/internal/observability"
	"eventmesh/internal/ratelimit"
)

// RequestObserver receives one observation per finished request; the API
// passes the Prometheus histogram's recorder.
type RequestObserver func(route, method string, status int, duration time.Duration)

// RequestContext assigns the request ID, returns it as X-Request-Id, puts a
// logger carrying it into the request context, and logs the request when it
// finishes. Bodies, headers and query strings are never logged: they can
// hold credentials and payloads.
func RequestContext(base *slog.Logger, observe RequestObserver) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := uuid.NewString()
		httpx.SetRequestID(c, requestID)
		c.Header("X-Request-Id", requestID)

		logger := base.With("request_id", requestID)
		c.Request = c.Request.WithContext(observability.WithLogger(c.Request.Context(), logger))

		c.Next()

		// FullPath is the route template ("/api/v1/events/:id"), not the raw
		// path, which keeps log and metric cardinality bounded.
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		duration := time.Since(start)
		attrs := []any{
			"method", c.Request.Method,
			"route", route,
			"status", c.Writer.Status(),
			"duration_ms", duration.Milliseconds(),
		}
		if userID := httpx.UserID(c); userID != uuid.Nil {
			attrs = append(attrs, "user_id", userID.String())
		}
		logger.Info("http request", attrs...)
		if observe != nil {
			observe(route, c.Request.Method, c.Writer.Status(), duration)
		}
	}
}

// Recover turns a panic in a handler into a generic 500. The stack goes to
// the log, never to the client.
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				observability.Logger(c.Request.Context()).Error("panic in http handler",
					"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				httpx.WriteError(c, httpx.NewError(http.StatusInternalServerError, "internal_error", "An internal error occurred."))
			}
		}()
		c.Next()
	}
}

// BodyLimit caps the request body. Reading past the cap fails with
// *http.MaxBytesError, which httpx.DecodeJSON reports as 413.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// CORS answers cross-origin requests only for origins on the allowlist.
// Other origins get no CORS headers at all, so the browser blocks them.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := originSet(allowedOrigins)
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowed[origin]; ok && origin != "" {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if c.Request.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With, Authorization, Idempotency-Key")
				h.Set("Access-Control-Max-Age", "600")
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// CSRFHeader is the custom header every state-changing dashboard request must carry.
const CSRFHeader = "X-Requested-With"

// CSRF protects cookie-authenticated, state-changing routes.
//
// A cross-site attack makes the victim's browser send a request with the
// session cookie attached. HTML forms and simple cross-origin requests cannot
// set a custom header; adding one forces the browser to send a CORS preflight
// first, and the preflight is refused for any origin not on the allowlist. So
// the presence of X-Requested-With proves the request came from JavaScript
// running on an allowed origin. As a second check, an Origin header that is
// present must be on the allowlist (same-origin requests through the nginx
// proxy carry the frontend's own origin).
func CSRF(allowedOrigins []string) gin.HandlerFunc {
	allowed := originSet(allowedOrigins)
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if c.GetHeader(CSRFHeader) == "" {
			httpx.WriteError(c, httpx.NewError(http.StatusForbidden, "csrf_rejected",
				"State-changing requests must include the X-Requested-With header."))
			return
		}
		if origin := c.GetHeader("Origin"); origin != "" {
			if _, ok := allowed[origin]; !ok && !sameOrigin(c.Request, origin) {
				httpx.WriteError(c, httpx.NewError(http.StatusForbidden, "csrf_rejected", "Origin is not allowed."))
				return
			}
		}
		c.Next()
	}
}

// sameOrigin reports whether origin is this server's own origin. A page served
// by the API itself (EM_SERVE_FRONTEND) sends its own origin, which need not be
// on the cross-origin allowlist. A browser sets Origin and Host itself, so a
// cross-site page cannot make them match.
func sameOrigin(r *http.Request, origin string) bool {
	return origin == "http://"+r.Host || origin == "https://"+r.Host
}

func originSet(origins []string) map[string]struct{} {
	set := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		set[o] = struct{}{}
	}
	return set
}

// RateLimitConfig describes one API-facing limit.
type RateLimitConfig struct {
	// Scope labels the rate-limited counter ("api" or "ingest").
	Scope string
	Rate  int
	Burst int
	// Key returns the bucket for a request.
	Key func(c *gin.Context) string
	// OnLimited is called for each refused request, for the metrics counter.
	OnLimited func(scope string)
}

// RateLimit enforces a token-bucket limit and answers 429 with Retry-After
// when the bucket is empty. It fails open if Redis is unreachable.
func RateLimit(limiter *ratelimit.Limiter, cfg RateLimitConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		decision := limiter.AllowOrFailOpen(c.Request.Context(), cfg.Key(c), float64(cfg.Rate), cfg.Burst)
		if decision.Allowed {
			c.Next()
			return
		}
		if cfg.OnLimited != nil {
			cfg.OnLimited(cfg.Scope)
		}
		// Retry-After is whole seconds; round up so the client never retries early.
		retryAfter := int(math.Ceil(decision.RetryAfter.Seconds()))
		if retryAfter < 1 {
			retryAfter = 1
		}
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		httpx.WriteError(c, httpx.NewError(http.StatusTooManyRequests, "rate_limited", "Rate limit exceeded. Retry later."))
	}
}

// ByClientIP keys a limit by the caller's IP address.
func ByClientIP(c *gin.Context) string { return ratelimit.APIKey(c.ClientIP()) }

// ByUser keys a limit by the authenticated user.
func ByUser(c *gin.Context) string { return ratelimit.IngestKey(httpx.UserID(c).String()) }
