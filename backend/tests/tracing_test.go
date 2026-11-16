package tests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans installs an in-memory tracer provider for the test and restores
// the previous (no-op) provider afterwards.
func recordSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})
	return exp
}

func spanNamed(spans tracetest.SpanStubs, name string) (tracetest.SpanStub, bool) {
	for _, s := range spans {
		if s.Name == name {
			return s, true
		}
	}
	return tracetest.SpanStub{}, false
}

// One event's journey is one trace: the API request, the ingestion, and the
// delivery made later by a worker that only ever saw a delivery ID in Redis.
func TestOneEventIsOneTraceAcrossAPIAndWorker(t *testing.T) {
	exp := recordSpans(t)
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL+"/private/path?token=sekret", "*")
	key := c.createKey("k")
	exp.Reset()

	r := s.ingest(key, "order.paid", map[string]any{"card": "4242424242424242", "note": "payload-marker"})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	s.startWorker("w1", 2, nil)
	s.waitStatus("delivered", 1, 10*time.Second)

	var spans tracetest.SpanStubs
	require.Eventually(t, func() bool {
		spans = exp.GetSpans()
		_, ok := spanNamed(spans, "delivery.process")
		return ok
	}, 5*time.Second, 20*time.Millisecond)

	httpSpan, ok := spanNamed(spans, "POST /api/v1/events")
	require.True(t, ok, "spans: %v", names(spans))
	ingest, ok := spanNamed(spans, "events.ingest")
	require.True(t, ok)
	process, _ := spanNamed(spans, "delivery.process")
	traceID := httpSpan.SpanContext.TraceID()

	for _, name := range []string{"events.ingest", "db.ingest_tx", "queue.enqueue", "delivery.process", "db.claim_delivery", "ratelimit.endpoint", "webhook.send", "delivery.record"} {
		sp, ok := spanNamed(spans, name)
		require.True(t, ok, "missing span %q in %v", name, names(spans))
		assert.Equal(t, traceID, sp.SpanContext.TraceID(), "span %q is in a different trace", name)
	}
	assert.Equal(t, httpSpan.SpanContext.SpanID(), ingest.Parent.SpanID(), "ingest is a child of the HTTP request")
	assert.Equal(t, ingest.SpanContext.SpanID(), process.Parent.SpanID(), "the worker's span hangs off the ingestion span")
	assert.True(t, process.Parent.IsRemote(), "the parent came from the stored traceparent, not from in-process context")

	// The stored link is a W3C traceparent naming that trace.
	var stored string
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT trace_context FROM events").Scan(&stored))
	assert.Regexp(t, `^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`, stored)
	assert.Contains(t, stored, traceID.String())

	// Spans never carry the payload, the URL path or query, or the secret.
	dump := fmt.Sprintf("%+v", spans)
	for _, banned := range []string{"payload-marker", "4242424242424242", "/private/path", "sekret", "whsec_", key} {
		assert.NotContains(t, dump, banned)
	}
	send, _ := spanNamed(spans, "webhook.send")
	assert.Contains(t, fmt.Sprint(send.Attributes), "127.0.0.1:")
	assert.Contains(t, fmt.Sprint(process.Attributes), "delivered")
}

// A client that sends its own traceparent has its trace continued.
func TestIncomingTraceparentIsContinued(t *testing.T) {
	exp := recordSpans(t)
	s := newStack(t, nil)
	c := s.newClient()
	c.register("a@example.com")
	key := c.createKey("k")
	exp.Reset()

	const upstream = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	r := s.ingest(key, "x.y", map[string]any{}, "traceparent", upstream)
	require.Equal(t, http.StatusAccepted, r.Status)
	sp, ok := spanNamed(exp.GetSpans(), "POST /api/v1/events")
	require.True(t, ok)
	assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", sp.SpanContext.TraceID().String())
	assert.Equal(t, "b7ad6b7169203331", sp.Parent.SpanID().String())

	// Garbage in the header starts a fresh trace instead of failing the request.
	exp.Reset()
	r = s.ingest(key, "x.y", map[string]any{}, "traceparent", "not-a-traceparent")
	require.Equal(t, http.StatusAccepted, r.Status)
	sp, ok = spanNamed(exp.GetSpans(), "POST /api/v1/events")
	require.True(t, ok)
	assert.False(t, sp.Parent.IsValid())
}

// With tracing off (the default) nothing is stored and nothing breaks.
func TestTracingOffStoresNoTraceContext(t *testing.T) {
	s := newStack(t, nil)
	tg := newTarget(t, 200)
	c := s.newClient()
	c.register("a@example.com")
	c.createEndpoint("hook", tg.URL, "*")
	key := c.createKey("k")
	require.Equal(t, http.StatusAccepted, s.ingest(key, "x.y", map[string]any{}).Status)
	s.startWorker("w1", 1, nil)
	s.waitStatus("delivered", 1, 10*time.Second)
	var stored *string
	require.NoError(t, s.pool.QueryRow(context.Background(), "SELECT trace_context FROM events").Scan(&stored))
	assert.Nil(t, stored)
}

func names(spans tracetest.SpanStubs) string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Name)
	}
	return strings.Join(out, ", ")
}
