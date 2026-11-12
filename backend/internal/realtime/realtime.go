// Package realtime carries live updates from the processes that change state
// (the API and the workers) to the dashboard.
//
// The worker and the API are separate processes, so an in-process channel
// cannot connect them. Updates are published on a Redis pub/sub channel per
// user; each API process subscribes and relays them to its SSE clients.
// Pub/sub is fire-and-forget: a lost message only means the dashboard
// refreshes a moment later, when it reconnects or its next query runs.
package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"eventmesh/internal/httpx"
	"eventmesh/internal/observability"
)

// Update types sent to the browser.
const (
	TypeDeliveryUpdated = "delivery.updated"
	TypeEventCreated    = "event.created"
)

const (
	channelSuffix = ":updates:"
	// clientBuffer is how many undelivered updates one SSE client may queue.
	clientBuffer      = 64
	heartbeatInterval = 15 * time.Second
)

// Update is one live notification. It carries IDs and the new status only;
// the browser reacts by refetching, so no payload or secret ever travels here.
type Update struct {
	Type       string `json:"type"`
	EventID    string `json:"event_id,omitempty"`
	DeliveryID string `json:"delivery_id,omitempty"`
	EndpointID string `json:"endpoint_id,omitempty"`
	Status     string `json:"status,omitempty"`
}

// Publisher sends updates. Publishing is best-effort by contract: it never
// returns an error, because a failed notification must not fail the delivery
// or request that caused it.
type Publisher interface {
	Publish(ctx context.Context, userID uuid.UUID, update Update)
}

// Channel returns the pub/sub channel for a user: "<prefix>:updates:<user_id>".
func Channel(keyPrefix string, userID uuid.UUID) string {
	return keyPrefix + channelSuffix + userID.String()
}

// RedisPublisher publishes updates on Redis pub/sub.
type RedisPublisher struct {
	rdb    redis.UniversalClient
	prefix string
}

// NewRedisPublisher returns a publisher on the given client.
func NewRedisPublisher(rdb redis.UniversalClient, keyPrefix string) *RedisPublisher {
	return &RedisPublisher{rdb: rdb, prefix: keyPrefix}
}

// Publish sends one update to the user's channel.
func (p *RedisPublisher) Publish(ctx context.Context, userID uuid.UUID, update Update) {
	data, err := json.Marshal(update)
	if err != nil {
		observability.Logger(ctx).Warn("encode live update failed", "error", err.Error())
		return
	}
	// WithoutCancel: the update describes a change that is already committed,
	// so it should still go out if the request context was just cancelled.
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := p.rdb.Publish(pubCtx, Channel(p.prefix, userID), data).Err(); err != nil {
		observability.Logger(ctx).Warn("publish live update failed", "error", err.Error())
	}
}

// client is one connected SSE stream.
type client struct {
	ch chan []byte
}

// Hub relays pub/sub messages to the SSE clients of this API process.
type Hub struct {
	rdb    redis.UniversalClient
	prefix string

	mu      sync.Mutex
	clients map[uuid.UUID]map[*client]struct{}
}

// NewHub returns a hub. Call Run to start relaying.
func NewHub(rdb redis.UniversalClient, keyPrefix string) *Hub {
	return &Hub{rdb: rdb, prefix: keyPrefix, clients: make(map[uuid.UUID]map[*client]struct{})}
}

// Run subscribes to every user's channel with one pattern subscription and
// dispatches until ctx is cancelled. go-redis re-subscribes on its own after
// a connection loss.
func (h *Hub) Run(ctx context.Context) error {
	pubsub := h.rdb.PSubscribe(ctx, h.prefix+channelSuffix+"*")
	defer func() { _ = pubsub.Close() }()

	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			h.closeAll()
			return nil
		case msg, ok := <-messages:
			if !ok {
				h.closeAll()
				return fmt.Errorf("redis pub/sub channel closed")
			}
			userID, err := uuid.Parse(strings.TrimPrefix(msg.Channel, h.prefix+channelSuffix))
			if err != nil {
				continue
			}
			h.dispatch(userID, []byte(msg.Payload))
		}
	}
}

// dispatch hands a message to each of the user's clients without ever
// blocking. A client whose buffer is full is too slow to keep up: it is
// dropped (its channel closed) rather than allowed to stall the publisher
// and, through it, every other client. EventSource reconnects by itself, and
// the dashboard refetches everything on reconnect, so nothing is lost.
func (h *Hub) dispatch(userID uuid.UUID, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients[userID] {
		select {
		case c.ch <- data:
		default:
			h.removeLocked(userID, c)
		}
	}
}

func (h *Hub) subscribe(userID uuid.UUID) *client {
	c := &client{ch: make(chan []byte, clientBuffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[userID] == nil {
		h.clients[userID] = make(map[*client]struct{})
	}
	h.clients[userID][c] = struct{}{}
	return c
}

func (h *Hub) unsubscribe(userID uuid.UUID, c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(userID, c)
}

// removeLocked removes and closes a client exactly once. The map lookup
// guards against closing a channel twice (dropped as slow, then unsubscribed).
func (h *Hub) removeLocked(userID uuid.UUID, c *client) {
	if _, present := h.clients[userID][c]; !present {
		return
	}
	delete(h.clients[userID], c)
	if len(h.clients[userID]) == 0 {
		delete(h.clients, userID)
	}
	close(c.ch)
}

func (h *Hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for userID, set := range h.clients {
		for c := range set {
			close(c.ch)
		}
		delete(h.clients, userID)
	}
}

// ClientCount returns the number of connected streams, for tests.
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.clients {
		n += len(set)
	}
	return n
}

// Stream handles GET /stream: a Server-Sent Events stream of the signed-in
// user's updates. The user comes from the session, so a client can only ever
// receive its own updates.
func (h *Hub) Stream(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		httpx.WriteError(c, fmt.Errorf("response writer does not support streaming"))
		return
	}
	userID := httpx.UserID(c)
	cl := h.subscribe(userID)
	defer h.unsubscribe(userID, cl)

	header := c.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no") // tells nginx not to buffer this response
	c.Writer.WriteHeader(http.StatusOK)

	// "retry" sets the browser's reconnect delay; the comment line confirms the stream is open.
	if _, err := fmt.Fprint(c.Writer, "retry: 2000\n: connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case data, open := <-cl.ch:
			if !open {
				return // dropped as a slow client, or the hub shut down
			}
			if err := writeEvent(c.Writer, data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			// A comment line: ignored by EventSource, but it keeps proxies
			// from closing an idle connection.
			if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, data []byte) error {
	var update Update
	if err := json.Unmarshal(data, &update); err != nil {
		return nil // not ours; skip it rather than end the stream
	}
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", update.Type, data)
	return err
}

// NopPublisher discards updates. Used where live updates are irrelevant.
type NopPublisher struct{}

// Publish does nothing.
func (NopPublisher) Publish(context.Context, uuid.UUID, Update) {}
