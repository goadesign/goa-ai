package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	// NoopLogger is a no-op implementation of Logger that discards all log messages.
	NoopLogger struct{}

	// NoopMetrics is a no-op implementation of Metrics that discards all metrics.
	NoopMetrics struct{}

	// NoopTracer is a no-op implementation of Tracer that creates no-op spans.
	NoopTracer struct{}

	// noopSpan is a no-op implementation of Span.
	noopSpan struct{}
)

// NewNoopLogger constructs a Logger that discards all log messages.
// Use this for testing or when logging is not required.
func NewNoopLogger() Logger {
	return NoopLogger{}
}

// NewNoopMetrics constructs a Metrics recorder that discards all metrics.
// Use this for testing or when metrics are not required.
func NewNoopMetrics() Metrics {
	return NoopMetrics{}
}

// NewNoopTracer constructs a Tracer that creates no-op spans.
// Use this for testing or when tracing is not required.
func NewNoopTracer() Tracer {
	return NoopTracer{}
}

// Debug discards the log message.
func (NoopLogger) Debug(context.Context, string, ...any) {}

// Info discards the log message.
func (NoopLogger) Info(context.Context, string, ...any) {}

// Warn discards the log message.
func (NoopLogger) Warn(context.Context, string, ...any) {}

// Error discards the log message.
func (NoopLogger) Error(context.Context, string, ...any) {}

// IncCounter discards the counter metric.
func (NoopMetrics) IncCounter(string, float64, ...string) {}

// RecordTimer discards the timer metric.
func (NoopMetrics) RecordTimer(string, time.Duration, ...string) {}

// RecordGauge discards the gauge metric.
func (NoopMetrics) RecordGauge(string, float64, ...string) {}

// Start returns a no-op span without modifying the context.
func (NoopTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Span) {
	return ctx, noopSpan{}
}

// Span returns a no-op span.
func (NoopTracer) Span(context.Context) Span {
	return noopSpan{}
}

// End is a no-op.
func (noopSpan) End(...trace.SpanEndOption) {}

// AddEvent is a no-op.
func (noopSpan) AddEvent(string, ...any) {}

// SetStatus is a no-op.
func (noopSpan) SetStatus(codes.Code, string) {}

// RecordError is a no-op.
func (noopSpan) RecordError(error, ...trace.EventOption) {}
