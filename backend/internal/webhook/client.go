package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"eventmesh/internal/ssrf"
)

// Response handling limits.
const (
	// MaxResponseRead is the most that is ever read from an endpoint's
	// response, so a huge or endless body cannot exhaust worker memory.
	MaxResponseRead = 64 * 1024
	// MaxExcerpt is the most that is stored per attempt.
	MaxExcerpt = 2 * 1024
)

// Timeouts bound each phase of an outgoing request. A slow endpoint can hold
// a worker for at most Total.
type Timeouts struct {
	Dial           time.Duration
	TLSHandshake   time.Duration
	ResponseHeader time.Duration
	Total          time.Duration
}

// TimeoutsFor returns the standard phase timeouts for a total budget:
// dial 5 s, TLS handshake 5 s, response header 10 s, each capped at the total.
func TimeoutsFor(total time.Duration) Timeouts {
	capAt := func(d time.Duration) time.Duration {
		if d > total {
			return total
		}
		return d
	}
	return Timeouts{
		Dial:           capAt(5 * time.Second),
		TLSHandshake:   capAt(5 * time.Second),
		ResponseHeader: capAt(10 * time.Second),
		Total:          total,
	}
}

// Client sends signed webhook requests.
type Client struct {
	http    *http.Client
	timeout time.Duration
}

// NewClient builds the delivery HTTP client. The SSRF policy is injected and
// enforced inside the dialer, at connection time. maxConnsPerHost should be
// the worker count: a process never has more requests in flight than workers.
func NewClient(policy *ssrf.Policy, timeouts Timeouts, maxConnsPerHost int) *Client {
	transport := &http.Transport{
		// No proxy, ever: with a proxy the socket would connect to the proxy's
		// address and the dial-time SSRF check would be checking the wrong host.
		Proxy:                 nil,
		DialContext:           policy.DialContext(&net.Dialer{Timeout: timeouts.Dial, KeepAlive: 30 * time.Second}),
		TLSHandshakeTimeout:   timeouts.TLSHandshake,
		ResponseHeaderTimeout: timeouts.ResponseHeader,
		// Keep-alive connections are reused across deliveries to the same
		// endpoint, which saves a TCP and TLS handshake per request.
		MaxIdleConns:        maxConnsPerHost * 4,
		MaxIdleConnsPerHost: maxConnsPerHost,
		MaxConnsPerHost:     maxConnsPerHost,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   timeouts.Total,
			// Redirects are not followed: the target of a redirect was never
			// validated, and a 3xx is classified as a failure instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		timeout: timeouts.Total,
	}
}

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Request is one delivery attempt to send.
type Request struct {
	URL        string
	Secret     string
	EventID    uuid.UUID
	EventType  string
	DeliveryID uuid.UUID
	Attempt    int
	Timestamp  time.Time
	Body       []byte
}

// Result is the outcome of one attempt. Exactly one of Err and StatusCode is meaningful.
type Result struct {
	StatusCode  int
	BodyExcerpt string
	// RetryAfter is the raw Retry-After response header, if any.
	RetryAfter string
	Duration   time.Duration
	Err        error
}

// Envelope is the JSON body of every webhook request.
type Envelope struct {
	ID        uuid.UUID       `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// BuildBody encodes the request body for an event.
func BuildBody(eventID uuid.UUID, eventType string, createdAt time.Time, payload json.RawMessage) ([]byte, error) {
	body, err := json.Marshal(Envelope{ID: eventID, Type: eventType, CreatedAt: createdAt.UTC(), Data: payload})
	if err != nil {
		return nil, fmt.Errorf("encode webhook body: %w", err)
	}
	return body, nil
}

// Send performs one signed POST and reports what happened. It never returns
// a Go error separately: transport failures are carried in Result.Err so the
// caller classifies every outcome through the same path.
func (c *Client) Send(ctx context.Context, req Request) Result {
	start := time.Now()
	result := c.send(ctx, req)
	result.Duration = time.Since(start)
	return result
}

func (c *Client) send(ctx context.Context, req Request) Result {
	// Scheme and credentials are checked again here, not only when the
	// endpoint was saved: this is the path that actually opens sockets.
	if _, err := ssrf.ParseURL(req.URL); err != nil {
		return Result{Err: fmt.Errorf("%w: %s", ssrf.ErrBlocked, err.Error())}
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return Result{Err: fmt.Errorf("build request: %w", err)}
	}
	timestamp := req.Timestamp.Unix()
	h := httpReq.Header
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", UserAgent)
	h.Set(HeaderEventID, req.EventID.String())
	h.Set(HeaderEventType, req.EventType)
	h.Set(HeaderDeliveryID, req.DeliveryID.String())
	h.Set(HeaderAttempt, strconv.Itoa(req.Attempt))
	h.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	h.Set(HeaderSignature, Sign(req.Secret, timestamp, req.Body))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Result{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// LimitReader stops after 64 KB no matter how much the endpoint sends.
	// A read error here does not change the outcome: the status line already
	// arrived, and the status decides success or failure.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseRead))
	return Result{
		StatusCode:  resp.StatusCode,
		BodyExcerpt: Excerpt(body),
		RetryAfter:  resp.Header.Get("Retry-After"),
	}
}

// Excerpt reduces a response body to at most MaxExcerpt bytes of valid UTF-8
// that PostgreSQL can store as text (no NUL bytes).
func Excerpt(body []byte) string {
	s := strings.ToValidUTF8(string(body), "�")
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > MaxExcerpt {
		// Cut on a rune boundary so a multi-byte character is never split.
		cut := MaxExcerpt
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}
