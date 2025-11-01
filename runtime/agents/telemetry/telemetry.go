// Package telemetry integrates runtime events with Clue tracing and metrics.
package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Logger captures structured logging used throughout the runtime. Implementations
// typically delegate to Clue but the interface is intentionally small so tests can
// provide lightweight stubs.
type Logger interface {
	Debug(ctx context.Context, msg string, keyvals ...any)
	Info(ctx context.Context, msg string, keyvals ...any)
	Warn(ctx context.Context, msg string, keyvals ...any)
	Error(ctx context.Context, msg string, keyvals ...any)
}

// Metrics exposes counter and histogram helpers for runtime instrumentation.
type Metrics interface {
	IncCounter(name string, value float64, tags ...string)
	RecordTimer(name string, duration time.Duration, tags ...string)
	RecordGauge(name string, value float64, tags ...string)
}

// Tracer abstracts span creation so runtime code can remain agnostic of the
// underlying OpenTelemetry provider. Uses OTEL option types for type safety.
type Tracer interface {
	Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Span)
	Span(ctx context.Context) Span
}

// Span represents an in-flight tracing span. Uses OTEL option types for type safety.
//
// Example usage:
//
//	ctx, span := tracer.Start(ctx, "operation", trace.WithSpanKind(trace.SpanKindClient))
//	defer span.End()
//	span.SetStatus(codes.Ok, "completed successfully")
type Span interface {
	End(opts ...trace.SpanEndOption)
	AddEvent(name string, attrs ...any)
	SetStatus(code codes.Code, description string)
	RecordError(err error, opts ...trace.EventOption)
}

// ToolTelemetry captures observability metadata collected during tool execution.
// Common fields provide type safety for standard metrics. The Extra map holds
// tool-specific data (e.g., API response headers, cache keys, provider details).
type ToolTelemetry struct {
	// DurationMs is the wall-clock execution time in milliseconds.
	DurationMs int64
	// TokensUsed tracks the total tokens consumed by LLM calls.
	TokensUsed int
	// Model identifies which LLM model was used (e.g., "gpt-4", "claude-3-opus").
	Model string
	// Extra holds tool-specific metadata not captured by common fields.
	Extra map[string]any
}
