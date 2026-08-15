// Package telemetry provides OpenTelemetry tracing and metrics
// instrumentation for the Agent Platform. It wraps the OTel SDK with
// platform-specific span attributes and convenience helpers.
//
// Usage:
//
//	// Initialize once at startup.
//	shutdown, err := telemetry.Init(ctx, telemetry.Config{
//	    ServiceName: "agent-platform",
//	    Exporter:    telemetry.ExporterOTLPHTTP,
//	    Endpoint:    "localhost:4318",
//	})
//	defer shutdown(ctx)
//
//	// Create spans in business logic.
//	ctx, span := telemetry.StartSpan(ctx, "Worker.RunAgent",
//	    telemetry.AttrTenantID(tenantID),
//	    telemetry.AttrSessionID(sessionID),
//	)
//	defer span.End()
package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ExporterType selects the OTLP exporter protocol.
type ExporterType string

const (
	// ExporterNone disables tracing (uses no-op tracer).
	ExporterNone ExporterType = "none"

	// ExporterOTLPHTTP sends spans over HTTP/Protobuf to an OTLP
	// collector (default endpoint: localhost:4318).
	ExporterOTLPHTTP ExporterType = "otlp-http"

	// ExporterOTLPGRPC sends spans over gRPC to an OTLP collector
	// (default endpoint: localhost:4317).
	ExporterOTLPGRPC ExporterType = "otlp-grpc"

	// ExporterStdout prints spans to stdout (for development).
	ExporterStdout ExporterType = "stdout"
)

// Config holds the telemetry initialization configuration.
type Config struct {
	// ServiceName is the OTel service.name resource attribute.
	ServiceName string

	// Exporter selects the span exporter type.
	Exporter ExporterType

	// Endpoint is the OTLP collector endpoint (host:port).
	// Defaults to localhost:4318 (HTTP) or localhost:4317 (gRPC).
	Endpoint string

	// SampleRate controls trace sampling (0.0 = none, 1.0 = all).
	// Defaults to 1.0 in development, 0.1 in production.
	SampleRate float64

	// BatchTimeout is the maximum time between span exports.
	// Defaults to 5s.
	BatchTimeout time.Duration

	// Attributes are additional static resource attributes.
	Attributes []attribute.KeyValue
}

// DefaultConfig returns a development-oriented configuration.
func DefaultConfig() Config {
	return Config{
		ServiceName:  "agent-platform",
		Exporter:     ExporterNone,
		Endpoint:     "localhost:4318",
		SampleRate:   1.0,
		BatchTimeout: 5 * time.Second,
	}
}

// Init initializes the global OpenTelemetry tracer provider. It returns
// a shutdown function that should be deferred by the caller.
func Init(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = "agent-platform"
	}

	if cfg.Exporter == ExporterNone || cfg.Exporter == "" {
		// Use no-op tracer — all spans are dropped.
		tp := noop.NewTracerProvider()
		otel.SetTracerProvider(tp)
		return func(_ context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion("1.0.0"),
		),
		resource.WithAttributes(cfg.Attributes...),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create resource: %w", err)
	}

	var exporter sdktrace.SpanExporter

	switch cfg.Exporter {
	case ExporterOTLPHTTP:
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Endpoint == "" || cfg.Endpoint == "localhost:4318" {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		exporter, err = otlptracehttp.New(ctx, opts...)
	case ExporterOTLPGRPC:
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Endpoint == "" || cfg.Endpoint == "localhost:4317" {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exporter, err = otlptracegrpc.New(ctx, opts...)
	default:
		return nil, fmt.Errorf("telemetry: unknown exporter type %q", cfg.Exporter)
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: create exporter: %w", err)
	}

	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 1.0
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(cfg.BatchTimeout),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// ─── Span helpers ────────────────────────────────────────────────────

// TracerName is the OTel instrumentation scope name used by all platform
// spans.
const TracerName = "agent-platform"

// StartSpan creates a new span with the given name and optional
// attributes. It extracts the parent span from ctx.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(TracerName).Start(ctx, name, trace.WithAttributes(attrs...))
}

// SpanFromContext returns the current span from context, or a no-op span.
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// AddEvent records a timestamped event on the current span.
func AddEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	span.AddEvent(name, trace.WithAttributes(attrs...))
}

// RecordError records an error on the current span.
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}

// SetStatus sets the span status based on an error.
func SetStatus(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	if err != nil {
		span.SetAttributes(attribute.String("error.message", err.Error()))
	}
}

// ─── Standard attribute constructors ─────────────────────────────────

// AttrTenantID returns the standard tenant_id span attribute.
func AttrTenantID(id string) attribute.KeyValue {
	return attribute.String("tenant_id", id)
}

// AttrSessionID returns the standard session_id span attribute.
func AttrSessionID(id string) attribute.KeyValue {
	return attribute.String("session_id", id)
}

// AttrAgentName returns the standard agent_name span attribute.
func AttrAgentName(name string) attribute.KeyValue {
	return attribute.String("agent_name", name)
}

// AttrModelName returns the standard model_name span attribute.
func AttrModelName(name string) attribute.KeyValue {
	return attribute.String("model_name", name)
}

// AttrToolName returns the standard tool_name span attribute.
func AttrToolName(name string) attribute.KeyValue {
	return attribute.String("tool_name", name)
}

// AttrChannel returns the standard channel span attribute.
func AttrChannel(channel string) attribute.KeyValue {
	return attribute.String("channel", channel)
}

// AttrBackendType returns the standard backend_type span attribute.
func AttrBackendType(t string) attribute.KeyValue {
	return attribute.String("backend_type", t)
}

// AttrTokenUsage sets token usage attributes on the current span.
func AttrTokenUsage(input, output, total int) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int("token_usage.input", input),
		attribute.Int("token_usage.output", output),
		attribute.Int("token_usage.total", total),
	}
}

// SetTokenUsage is a convenience helper that sets token usage
// attributes on the current span in ctx.
func SetTokenUsage(ctx context.Context, input, output int) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.Int("token_usage.input", input),
		attribute.Int("token_usage.output", output),
		attribute.Int("token_usage.total", input+output),
	)
}

// AttrLatencyMs sets a latency_ms attribute.
func AttrLatencyMs(ms int64) attribute.KeyValue {
	return attribute.Int64("latency_ms", ms)
}

// AttrErrorType sets an error_type attribute.
func AttrErrorType(t string) attribute.KeyValue {
	return attribute.String("error_type", t)
}

// ─── Convenience span creators for common operations ─────────────────

// SpanGateway creates a span for the Gateway webhook handler.
func SpanGateway(ctx context.Context, channel, tenantID string) (context.Context, trace.Span) {
	return StartSpan(ctx, "Gateway.HandleWebhook",
		AttrChannel(channel),
		AttrTenantID(tenantID),
	)
}

// SpanWorker creates a span for a Worker agent run.
func SpanWorker(ctx context.Context, tenantID, sessionID, agentName string) (context.Context, trace.Span) {
	return StartSpan(ctx, "Worker.RunAgent",
		AttrTenantID(tenantID),
		AttrSessionID(sessionID),
		AttrAgentName(agentName),
	)
}

// SpanLLM creates a span for an LLM call.
func SpanLLM(ctx context.Context, modelName string) (context.Context, trace.Span) {
	return StartSpan(ctx, "LLM.GenerateContent",
		AttrModelName(modelName),
	)
}

// SpanTool creates a span for a tool execution.
func SpanTool(ctx context.Context, toolName string) (context.Context, trace.Span) {
	return StartSpan(ctx, "Tool.Execute",
		AttrToolName(toolName),
	)
}

// SpanSession creates a span for a SessionService operation.
func SpanSession(ctx context.Context, op, backendType string) (context.Context, trace.Span) {
	return StartSpan(ctx, "SessionService."+op,
		AttrBackendType(backendType),
	)
}

// SpanMemory creates a span for a MemoryService operation.
func SpanMemory(ctx context.Context, op, backendType string) (context.Context, trace.Span) {
	return StartSpan(ctx, "MemoryService."+op,
		AttrBackendType(backendType),
	)
}

// SpanChannel creates a span for an IM channel operation.
func SpanChannel(ctx context.Context, channel, op string) (context.Context, trace.Span) {
	return StartSpan(ctx, "IM."+op,
		AttrChannel(channel),
	)
}

// SpanAudit creates a span for an audit log write.
func SpanAudit(ctx context.Context) (context.Context, trace.Span) {
	return StartSpan(ctx, "Audit.Write")
}
