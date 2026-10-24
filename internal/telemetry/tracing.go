package telemetry

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TracerManager manages OpenTelemetry TracerProvider and W3C context propagation.
type TracerManager struct {
	tp     *sdktrace.TracerProvider
	tracer trace.Tracer
}

// NewTracerManager configures the OpenTelemetry Go SDK with W3C Trace Context propagation.
func NewTracerManager(serviceName string, samplingRatio float64) (*TracerManager, error) {
	if serviceName == "" {
		serviceName = "sentinelproxy"
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(samplingRatio))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)
	// Set standard W3C Trace Context propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	tracer := tp.Tracer("github.com/sentinelproxy/sentinelproxy")

	return &TracerManager{
		tp:     tp,
		tracer: tracer,
	}, nil
}

// StartInboundSpan extracts incoming W3C trace context and starts an inbound gateway server span.
func (tm *TracerManager) StartInboundSpan(r *http.Request, routeID string) (context.Context, trace.Span) {
	if tm == nil || tm.tracer == nil {
		return r.Context(), trace.SpanFromContext(r.Context())
	}

	propagator := otel.GetTextMapPropagator()
	extractedCtx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

	ctx, span := tm.tracer.Start(
		extractedCtx,
		"gateway.request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("route_id", routeID),
			semconv.HTTPRequestMethodKey.String(r.Method),
		),
	)

	return ctx, span
}

// InjectOutboundTrace injects W3C Trace Context headers into an outgoing HTTP request.
func (tm *TracerManager) InjectOutboundTrace(ctx context.Context, req *http.Request) {
	if tm == nil {
		return
	}
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(ctx, propagation.HeaderCarrier(req.Header))
}

// Shutdown flushes all pending spans and stops the TracerProvider.
func (tm *TracerManager) Shutdown(ctx context.Context) error {
	if tm == nil || tm.tp == nil {
		return nil
	}
	return tm.tp.Shutdown(ctx)
}
