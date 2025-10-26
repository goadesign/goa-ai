package telemetry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"goa.design/goa-ai/agents/runtime/telemetry"
)

func TestNoopLogger(t *testing.T) {
	ctx := context.Background()
	logger := telemetry.NewNoopLogger()

	// These should not panic and should do nothing
	logger.Debug(ctx, "debug message", "key", "value")
	logger.Info(ctx, "info message", "key", "value")
	logger.Warn(ctx, "warn message", "key", "value")
	logger.Error(ctx, "error message", "key", "value")
}

func TestNoopMetrics(t *testing.T) {
	metrics := telemetry.NewNoopMetrics()

	// These should not panic and should do nothing
	metrics.IncCounter("test.counter", 1.0, "env", "test")
	metrics.RecordTimer("test.timer", 100*time.Millisecond, "env", "test")
	metrics.RecordGauge("test.gauge", 42.0, "env", "test")
}

func TestNoopTracer(t *testing.T) {
	ctx := context.Background()
	tracer := telemetry.NewNoopTracer()

	// Start should return the same context and a non-nil span
	newCtx, span := tracer.Start(ctx, "test.operation")
	if newCtx != ctx {
		t.Error("expected noop tracer to return same context")
	}
	if span == nil {
		t.Fatal("expected non-nil span")
	}

	// These should not panic and should do nothing
	span.AddEvent("test.event", "key", "value")
	span.SetStatus(codes.Ok, "completed")
	span.RecordError(errors.New("test error"))
	span.End()

	// Span should return a non-nil span
	span2 := tracer.Span(ctx)
	if span2 == nil {
		t.Fatal("expected non-nil span from Span()")
	}
}

func TestNoopImplementsInterfaces(t *testing.T) {
	// Compile-time verification that noop types implement the interfaces
	_ = telemetry.NewNoopLogger()
	_ = telemetry.NewNoopMetrics()
	_ = telemetry.NewNoopTracer()
}
