// Package observability provides structured logging and Prometheus metrics.
package observability

import (
	"context"
	"io"
	"log/slog"
)

type loggerKey struct{}

// NewLogger returns a JSON logger writing to w, tagged with the service name.
func NewLogger(w io.Writer, service string, level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With("service", service)
}

// WithLogger stores a logger, already carrying request- or delivery-scoped
// fields, in the context.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// Logger returns the logger carried by ctx, or the process default.
func Logger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}
