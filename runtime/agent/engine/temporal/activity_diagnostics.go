package temporal

// Activity registration offers the original returned error to application
// instrumentation before converting it into a bounded Temporal failure. The
// worker interceptor already owns the activity span and its lifetime.

import (
	"context"

	"go.opentelemetry.io/otel/codes"

	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// recordActivityError records an original activity failure on the current span.
// Application tracers may inspect its typed causes and custom Temporal details;
// the serialized failure deliberately cannot preserve arbitrary Go values.
// Workflow handlers never call this method because workflow execution replays.
func (e *Engine) recordActivityError(ctx context.Context, err error) {
	if telemetry.ShouldRecordSpanError(ctx, err) {
		span := e.tracer.Span(ctx)
		span.RecordError(err)
		span.SetStatus(codes.Error, errorevidence.Text(err))
	}
}
