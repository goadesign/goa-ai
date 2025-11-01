package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
	"goa.design/clue/log"
)

// MergeContext injects logging, tracing, and baggage metadata carried by base
// into ctx. It is used by workflow adapters to rehydrate the caller context
// (Clue logger + OTEL span) inside workflow/activity handlers so downstream
// code inherits the same observability state even when the workflow engine
// creates a fresh context. When base is nil the original ctx is returned.
func MergeContext(ctx, base context.Context) context.Context {
	if base == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = log.WithContext(ctx, base)
	if bag := baggage.FromContext(base); bag.Len() > 0 {
		ctx = baggage.ContextWithBaggage(ctx, bag)
	}
	if spanCtx := trace.SpanContextFromContext(base); spanCtx.IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, spanCtx)
	}
	return ctx
}
