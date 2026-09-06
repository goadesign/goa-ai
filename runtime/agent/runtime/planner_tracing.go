package runtime

// Planner activity spans include result validation and transport preparation,
// not just the application planner call. Rejected results may cross an activity
// as successful values, so tracing uses the original failure retained locally.

import (
	"context"

	"go.opentelemetry.io/otel/codes"

	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// finishPlannerActivitySpan offers the original failure to the application
// tracer exactly once, before the activity returns to its workflow engine.
// It never runs in replaying workflow code or changes the returned result.
func finishPlannerActivitySpan(ctx context.Context, span telemetry.Span, invocation *plannerActivityInvocation, err error) {
	defer span.End()
	if invocation != nil && invocation.originalFailure != nil {
		err = invocation.originalFailure
	}
	if telemetry.ShouldRecordSpanError(ctx, err) {
		span.RecordError(err)
		span.SetStatus(codes.Error, errorevidence.Text(err))
	} else if err == nil {
		span.SetStatus(codes.Ok, "ok")
	}
}
