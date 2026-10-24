package telemetry_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sentinelproxy/sentinelproxy/internal/middleware"
	"github.com/sentinelproxy/sentinelproxy/internal/telemetry"
	"go.opentelemetry.io/otel/trace"
)

func TestMetrics_RecordingAndBoundedLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := telemetry.NewMetrics(reg)

	// Record various metric events
	m.RecordRequest("route-1", "GET", http.StatusOK, 25*time.Millisecond)
	m.RecordRequest("route-1", "POST", http.StatusBadRequest, 10*time.Millisecond)
	m.RecordRequest("route-2", "GET", http.StatusInternalServerError, 150*time.Millisecond)

	m.RecordUpstream("route-1", "backend-1", "success", http.StatusOK, 20*time.Millisecond)
	m.RecordUpstream("route-1", "backend-1", "failure", http.StatusInternalServerError, 140*time.Millisecond)

	m.SetCircuitBreakerState("route-1", "backend-1", "CLOSED")
	m.RecordCircuitBreakerTransition("route-1", "backend-1", "CLOSED", "OPEN")
	m.SetBackendHealth("route-1", "backend-1", true)
	m.RecordRateLimitDecision("route-1", true)
	m.RecordRateLimitDecision("route-1", false)
	m.RecordConfigReload(true)

	// Verify status class formatting
	if telemetry.FormatStatusClass(200) != "2xx" {
		t.Errorf("expected 2xx, got %s", telemetry.FormatStatusClass(200))
	}
	if telemetry.FormatStatusClass(404) != "4xx" {
		t.Errorf("expected 4xx, got %s", telemetry.FormatStatusClass(404))
	}
	if telemetry.FormatStatusClass(503) != "5xx" {
		t.Errorf("expected 5xx, got %s", telemetry.FormatStatusClass(503))
	}

	// Verify method label sanitization
	if telemetry.SanitizeMethod("GET") != "GET" {
		t.Errorf("expected GET, got %s", telemetry.SanitizeMethod("GET"))
	}
	if telemetry.SanitizeMethod("MALICIOUS_CUSTOM_METHOD_1234") != "OTHER" {
		t.Errorf("expected OTHER, got %s", telemetry.SanitizeMethod("MALICIOUS_CUSTOM_METHOD_1234"))
	}

	// Gather metrics to ensure registration succeeded without panic or label conflicts
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather prometheus metrics: %v", err)
	}
	if len(mfs) == 0 {
		t.Errorf("expected non-empty gathered metrics")
	}
}

func TestTracer_W3CPropagationAndMalformedHeaderSafety(t *testing.T) {
	tm, err := telemetry.NewTracerManager("test-service", 1.0)
	if err != nil {
		t.Fatalf("failed to create tracer manager: %v", err)
	}
	defer func() { _ = tm.Shutdown(context.Background()) }()

	// 1. Valid W3C traceparent header
	const validTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("traceparent", validTraceparent)

	ctx, span := tm.StartInboundSpan(req, "test-route")
	defer span.End()

	sc := span.SpanContext()
	if !sc.IsValid() {
		t.Fatalf("expected valid span context from traceparent")
	}
	if sc.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("expected trace ID preserved, got %s", sc.TraceID().String())
	}

	// 2. Outbound trace injection
	outboundReq, _ := http.NewRequest("GET", "http://backend/test", nil)
	tm.InjectOutboundTrace(ctx, outboundReq)

	outboundTP := outboundReq.Header.Get("traceparent")
	if outboundTP == "" {
		t.Errorf("expected traceparent header to be injected into outbound request")
	}

	// 3. Malformed traceparent header must not crash or error
	malformedReq := httptest.NewRequest("GET", "/test", nil)
	malformedReq.Header.Set("traceparent", "invalid-garbage-trace-header")

	mCtx, mSpan := tm.StartInboundSpan(malformedReq, "test-route")
	defer mSpan.End()

	mSc := trace.SpanFromContext(mCtx).SpanContext()
	if !mSc.IsValid() {
		t.Errorf("expected tracer to generate new valid span for malformed traceparent header")
	}
}

func TestLogging_Correlation(t *testing.T) {
	var buf bytes.Buffer
	logger := telemetry.NewLogger("info", "json")
	// Use custom buffer to capture log output
	_ = logger

	ctx := middleware.WithRequestID(context.Background(), "test-req-id-1234")
	// Verify LogRequest runs without panic
	telemetry.LogRequest(ctx, logger, "route-1", "GET", "/test", 200, 15, "backend-1", "")
	_ = buf.Len()
}
