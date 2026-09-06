package temporal

// Registered activity wrappers must offer the original error to the configured
// tracer before Temporal receives a bounded, framework-owned failure.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	diagnosticWorker struct {
		fakeWorker
		activities map[string]any
	}

	activityDiagnosticTracer struct {
		telemetry.NoopTracer
		span activityDiagnosticSpan
	}

	activityDiagnosticSpan struct {
		errors []error
		code   codes.Code
	}
)

func TestActivityWrappersOfferOriginalCustomError(t *testing.T) {
	for _, name := range []string{"storage", "planner", "tool", "child"} {
		t.Run(name, func(t *testing.T) {
			original := temporal.NewNonRetryableApplicationError("dependency rejected field", "client.type",
				errors.New("root detail"), "custom diagnostic detail")
			tracer := &activityDiagnosticTracer{}
			eng := newTestEngine(t)
			eng.tracer = tracer
			captured := &diagnosticWorker{activities: make(map[string]any)}
			eng.workerFactory = func(client.Client, string, worker.Options) worker.Worker { return captured }
			var input any
			switch name {
			case "storage":
				input = &api.StorageActivityCommand{}
				require.NoError(t, eng.RegisterStorageActivity(t.Context(), name, engine.ActivityOptions{},
					func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
						return nil, original
					}))
			case "planner":
				input = &api.PlanActivityInput{}
				require.NoError(t, eng.RegisterPlannerActivity(t.Context(), name, engine.ActivityOptions{},
					func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) { return nil, original }))
			case "tool":
				input = &api.ToolInput{}
				require.NoError(t, eng.RegisterExecuteToolActivity(t.Context(), name, engine.ActivityOptions{},
					func(context.Context, *api.ToolInput) (*api.ToolOutput, error) { return nil, original }))
			case "child":
				input = &api.AgentChildActivityInput{}
				require.NoError(t, eng.RegisterAgentChildActivity(t.Context(), name, engine.ActivityOptions{},
					func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
						return nil, original
					}))
			}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			env.RegisterActivityWithOptions(captured.activities[name], activity.RegisterOptions{Name: name})
			_, err := env.ExecuteActivity(name, input)
			require.Error(t, err)
			require.Len(t, tracer.span.errors, 1)
			assert.Same(t, original, tracer.span.errors[0])
			assert.Equal(t, codes.Error, tracer.span.code)
			var custom string
			var capturedError *temporal.ApplicationError
			require.ErrorAs(t, tracer.span.errors[0], &capturedError)
			require.NoError(t, capturedError.Details(&custom))
			assert.Equal(t, "custom diagnostic detail", custom)
			assert.EqualError(t, errors.Unwrap(tracer.span.errors[0]), "root detail")
		})
	}
}

func TestActivityDiagnosticCapturePreservesCancellationPolicy(t *testing.T) {
	tracer := &activityDiagnosticTracer{}
	eng := &Engine{tracer: tracer}
	eng.recordActivityError(t.Context(), nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	eng.recordActivityError(ctx, context.Canceled)
	assert.Empty(t, tracer.span.errors)
}

func (w *diagnosticWorker) RegisterActivityWithOptions(fn any, opts activity.RegisterOptions) {
	w.activities[opts.Name] = fn
}

func (t *activityDiagnosticTracer) Span(context.Context) telemetry.Span {
	return &t.span
}

func (s *activityDiagnosticSpan) RecordError(err error, _ ...trace.EventOption) {
	s.errors = append(s.errors, err)
}

func (s *activityDiagnosticSpan) SetStatus(code codes.Code, _ string) {
	s.code = code
}

func (*activityDiagnosticSpan) End(...trace.SpanEndOption) {}

func (*activityDiagnosticSpan) AddEvent(string, ...any) {}

func (*activityDiagnosticSpan) SetAttributes(...attribute.KeyValue) {}
