package runtime

// These tests protect application access to the original tool error. A custom
// tracer decides which messages and causes to retain; the runtime must not
// replace the error before calling that tracer.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	toolDiagnosticTracer struct {
		recordingTelemetryTracer
		startedAt time.Time
		endedAt   time.Time
	}

	toolDiagnosticSpan struct {
		*recordingTelemetrySpan
		owner *toolDiagnosticTracer
	}
)

func TestToolSpanPreservesApplicationDiagnosticChoice(t *testing.T) {
	kinds := []planner.FailureKind{
		planner.FailureInvalidCall, planner.FailureDomainRejection,
		planner.FailureUnavailable, planner.FailureRateLimited,
		planner.FailureTimeout, planner.FailureMalformedResult,
		planner.FailureInternal, "",
	}
	for _, kind := range kinds {
		name := string(kind)
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			tracer := &toolDiagnosticTracer{}
			runtime := New(newTestStore(), WithTracer(tracer))
			var failure *planner.ToolFailure
			if kind != "" {
				failure = &planner.ToolFailure{
					Kind: kind,
					Error: planner.NewToolErrorWithCause("operation failed",
						planner.NewToolErrorWithCause("upstream detail", planner.NewToolError("root detail"))),
					Recovery: planner.RecoveryDirective{Action: planner.RecoveryFinish},
				}
			}
			event := hooks.NewToolResultReceivedEvent("run", "service.agent", "session", "run",
				"service.read", "call", "", nil, nil, "", nil, 125*time.Millisecond, nil, failure)
			before := planner.CloneToolFailure(failure)

			require.NoError(t, runtime.recordGenAITelemetryEvent(t.Context(), event))

			require.Len(t, tracer.spans, 1)
			span := tracer.spans[0]
			assert.Equal(t, "execute_tool service.read", span.name)
			assert.True(t, span.ended)
			assert.Equal(t, time.UnixMilli(event.Timestamp()).UTC(), tracer.endedAt)
			assert.Equal(t, 125*time.Millisecond, tracer.endedAt.Sub(tracer.startedAt))
			attrs := attrsByKey(span.attrs)
			assert.Equal(t, "call", attrs[telemetry.AttrGenAIToolCallID].AsString())
			assert.Equal(t, "service.read", attrs[telemetry.AttrGenAIToolName].AsString())
			assert.Equal(t, before, event.Failure)
			if failure == nil {
				assert.Empty(t, span.errs)
				assert.Equal(t, codes.Ok, span.statusCode)
				assert.Equal(t, "ok", span.statusDesc)
				return
			}
			require.Len(t, span.errs, 1)
			assert.Same(t, event.Failure.Error, span.errs[0])
			assert.Equal(t, "root detail", failure.Error.Cause.Cause.Message)
			assert.Equal(t, codes.Error, span.statusCode)
			assert.Equal(t, failure.Error.Message, span.statusDesc)
		})
	}
}

func (t *toolDiagnosticTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	config := trace.NewSpanStartConfig(opts...)
	t.startedAt = config.Timestamp()
	ctx, span := t.recordingTelemetryTracer.Start(ctx, name, opts...)
	return ctx, &toolDiagnosticSpan{recordingTelemetrySpan: span.(*recordingTelemetrySpan), owner: t}
}

func (s *toolDiagnosticSpan) End(opts ...trace.SpanEndOption) {
	config := trace.NewSpanEndConfig(opts...)
	s.owner.endedAt = config.Timestamp()
	s.recordingTelemetrySpan.End(opts...)
}
