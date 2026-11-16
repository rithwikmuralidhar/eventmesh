package observability

import (
	"context"
	"fmt"
	"io"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// TracerName is the instrumentation scope of every EventMesh span.
const TracerName = "eventmesh"

// Tracer returns the process tracer. Until SetupTracing installs a provider
// this is OpenTelemetry's no-op tracer, so instrumented code costs almost
// nothing when tracing is off and needs no "is tracing on" checks.
func Tracer() trace.Tracer { return otel.Tracer(TracerName) }

// propagator is the W3C Trace Context format ("traceparent" header).
var propagator = propagation.TraceContext{}

// TracingConfig selects where spans go.
type TracingConfig struct {
	Exporter     string  // "none", "stdout" or "otlp"
	OTLPEndpoint string  // host:port of an OTLP/HTTP collector (Jaeger, Tempo, the OTel Collector)
	SampleRatio  float64 // share of new traces recorded, 0..1
}

// SetupTracing installs the global tracer provider and returns its shutdown
// function, which flushes buffered spans. With exporter "none" it does
// nothing and returns a no-op shutdown.
//
// Spans are exported in batches from a background goroutine, so a slow or
// unreachable collector never blocks a request or a delivery; spans are
// dropped instead when the buffer fills.
func SetupTracing(ctx context.Context, service string, cfg TracingConfig, stdout io.Writer) (func(context.Context) error, error) {
	var exporter sdktrace.SpanExporter
	var err error
	switch cfg.Exporter {
	case "", "none":
		return func(context.Context) error { return nil }, nil
	case "stdout":
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(stdout))
	case "otlp":
		exporter, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpoint(cfg.OTLPEndpoint), otlptracehttp.WithInsecure())
	default:
		return nil, fmt.Errorf("unknown trace exporter %q", cfg.Exporter)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s trace exporter: %w", cfg.Exporter, err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		// ParentBased: a request that arrives with a sampled traceparent stays
		// sampled, so one trace is never half recorded across API and worker.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "eventmesh-"+service))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// InjectTraceContext renders the span in ctx as a W3C traceparent value, or
// "" when there is no recording span. It is what gets stored with an event.
func InjectTraceContext(ctx context.Context) string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ExtractTraceContext returns ctx with the remote span named by traceparent
// as its parent. An empty or malformed value leaves ctx unchanged, so the
// next span simply starts a new trace.
func ExtractTraceContext(ctx context.Context, traceparent string) context.Context {
	traceparent = strings.TrimSpace(traceparent)
	if traceparent == "" {
		return ctx
	}
	return propagator.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}

// TraceID returns the trace ID of the span in ctx, or "" if there is none.
// Logging it next to request_id is what connects a log line to its trace.
func TraceID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String()
	}
	return ""
}
