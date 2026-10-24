package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/sentinelproxy/sentinelproxy/internal/middleware"
	"go.opentelemetry.io/otel/trace"
)

// NewLogger creates a configured slog.Logger with JSON formatting.
func NewLogger(levelStr string, format string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if strings.ToLower(format) == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

// LogRequest emits a structured request completion log with correlated IDs.
func LogRequest(ctx context.Context, logger *slog.Logger, routeID, method, path string, status int, durationMs int64, backendID, errorClass string) {
	if logger == nil {
		return
	}

	reqID := middleware.RequestIDFromContext(ctx)
	span := trace.SpanFromContext(ctx)
	traceID := ""
	if span.SpanContext().IsValid() {
		traceID = span.SpanContext().TraceID().String()
	}

	attrs := []any{
		"request_id", reqID,
		"route_id", routeID,
		"method", method,
		"path", path,
		"status", status,
		"duration_ms", durationMs,
	}

	if traceID != "" {
		attrs = append(attrs, "trace_id", traceID)
	}
	if backendID != "" {
		attrs = append(attrs, "backend_id", backendID)
	}
	if errorClass != "" {
		attrs = append(attrs, "error_class", errorClass)
	}

	logger.InfoContext(ctx, "HTTP request handled", attrs...)
}
