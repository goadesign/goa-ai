package runtime

// These tests trace the existing saved-history and planner preparation owners.
// Synthetic message and result text must stay out of measurement attributes;
// failures keep the runtime's existing error and cancellation reporting policy.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	// preparationTraceStore shows that downstream reads receive the new child
	// context and can return an unchanged synthetic storage failure.
	preparationTraceStore struct {
		storage.Store
		tracer telemetry.Tracer
		err    error
	}

	// preparationTraceMemory returns the same failure from planner memory
	// preparation without changing the runtime's memory interface.
	preparationTraceMemory struct {
		memory.Store
		err error
	}
)

func TestPlannerPreparationSpansAreSequentialActivityChildren(t *testing.T) {
	recorder := preparationSpanRecorder(t)
	original := []*model.Message{userMsg("synthetic private message")}
	rt := newTestRuntimeWithPlanner("test.agent", &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		assert.Equal(t, original, input.Messages)
		return finalPlannerResult("synthetic private answer"), nil
	}})
	rt.tracer = telemetry.NewClueTracer()
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "test.agent", RunID: "test-run", RunContext: run.Context{RunID: "test-run"}}, original)
	rt.Store = &preparationTraceStore{Store: rt.Store, tracer: rt.tracer}
	ctx, parent := rt.tracer.Start(t.Context(), "synthetic.parent")
	output, err := rt.PlanResumeActivity(ctx, input)
	parent.End()
	require.NoError(t, err)
	require.NotNil(t, output.Result.FinalResponse)
	spans := recorder.Ended()
	activity := preparationSpan(t, spans, "planner.plan_resume")
	parentSpan := preparationSpan(t, spans, "synthetic.parent")
	assert.Equal(t, parentSpan.SpanContext().SpanID(), activity.Parent().SpanID())
	names := []string{"planner.resolve_activity_input", "planner.load_tool_outputs", "planner.prepare_continuations", "planner.prepare_invocation"}
	var previous sdktrace.ReadOnlySpan
	for _, name := range names {
		span := preparationSpan(t, spans, name)
		assert.Equal(t, activity.SpanContext().SpanID(), span.Parent().SpanID())
		assert.Equal(t, codes.Ok, span.Status().Code)
		assert.Empty(t, span.Events())
		assert.False(t, span.EndTime().Before(span.StartTime()))
		if previous != nil {
			assert.False(t, span.StartTime().Before(previous.EndTime()))
		}
		previous = span
		for _, attr := range span.Attributes() {
			assert.Equal(t, attribute.INT64, attr.Value.Type())
			assert.NotContains(t, attr.Value.String(), "synthetic private")
		}
	}
	assert.Equal(t, int64(1), attrsByKey(preparationSpan(t, spans, names[0]).Attributes())["message.count"].AsInt64())
	assert.Equal(t, int64(0), attrsByKey(preparationSpan(t, spans, names[1]).Attributes())["tool_output.ref_count"].AsInt64())
	assert.Equal(t, int64(0), attrsByKey(preparationSpan(t, spans, names[2]).Attributes())["continuation.count"].AsInt64())
	attrs := attrsByKey(preparationSpan(t, spans, names[3]).Attributes())
	assert.Equal(t, int64(1), attrs["message.count"].AsInt64())
	assert.Equal(t, int64(0), attrs["tool_spec.count"].AsInt64())
	storeReads := 0
	for _, span := range spans {
		if span.Name() == "synthetic.store.read" {
			storeReads++
			owner := preparationSpanByID(t, spans, span.Parent().SpanID().String())
			assert.Contains(t, []string{names[0], names[2]}, owner.Name())
		}
	}
	assert.Positive(t, storeReads, "downstream reads must receive the measured child context")
}

func TestPlannerToolOutputSpanCountsCrossRunSourcesWithoutContents(t *testing.T) {
	recorder := preparationSpanRecorder(t)
	store := newTestStore()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}
	appendHistoricalHookEvent(t, store, hooks.NewToolCallScheduledEvent("call-run", subject, "session", "test.lookup", "call", rawjson.Message(`{"query":"synthetic private argument"}`), "", "", 0), "schedule", 1)
	appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent("result-run", subject, "session", "call-run", "test.lookup", "call", "", rawjson.Message(`{"answer":"synthetic private result"}`), nil, nil, "", nil, time.Second, nil, nil), "result", 2)
	rt := New(store, WithTracer(telemetry.NewClueTracer()))
	seedTestToolSpecs(rt, newAnyJSONSpec("test.lookup"))
	outputs, err := rt.loadPlannerToolOutputs(t.Context(), []*api.ToolOutputRef{{CallRunID: "call-run", ResultRunID: "result-run", ToolCallID: "call"}})
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	assert.Equal(t, rawjson.Message(`{"answer":"synthetic private result"}`), outputs[0].Result)
	span := preparationSpan(t, recorder.Ended(), "planner.load_tool_outputs")
	attrs := attrsByKey(span.Attributes())
	assert.Equal(t, int64(1), attrs["tool_output.ref_count"].AsInt64())
	assert.Equal(t, int64(1), attrs["tool_output.count"].AsInt64())
	assert.Equal(t, int64(2), attrs["source_run.count"].AsInt64())
	assert.Len(t, attrs, 3)
	assert.Empty(t, span.Events())
}

func TestPlannerPreparationSpansPreserveErrorsAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, phase := range []string{"planner.resolve_activity_input", "planner.load_tool_outputs", "planner.prepare_continuations", "planner.prepare_invocation"} {
			t.Run(phase+map[bool]string{false: "/failure", true: "/canceled"}[canceled], func(t *testing.T) {
				recorder := preparationSpanRecorder(t)
				rt := newTestRuntimeWithPlanner("test.agent", &stubPlanner{})
				rt.tracer = telemetry.NewClueTracer()
				wire := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "test.agent", RunID: "test-run", RunContext: run.Context{RunID: "test-run"}}, nil)
				input := &resolvedPlanActivityInput{PlanActivityInput: wire}
				cause := errors.New("synthetic storage failure")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if canceled {
					cancel()
					cause = context.Canceled
				}
				rt.Store = &preparationTraceStore{Store: rt.Store, tracer: rt.tracer, err: cause}
				var err error
				switch phase {
				case "planner.resolve_activity_input":
					_, err = rt.resolvePlanActivityInput(ctx, wire)
				case "planner.load_tool_outputs":
					_, err = rt.loadPlannerToolOutputs(ctx, []*api.ToolOutputRef{{CallRunID: "test-run", ResultRunID: "test-run", ToolCallID: "call"}})
				case "planner.prepare_continuations":
					_, err = rt.continuationActionsForHistory(ctx, input, rt.newRegistryCatalog(rt.agents["test.agent"].Definition), nil)
				case "planner.prepare_invocation":
					rt.Memory = preparationTraceMemory{err: cause}
					_, err = rt.preparePlannerActivity(ctx, input, rt.newRegistryCatalog(rt.agents["test.agent"].Definition), nil, nil, nil)
				}
				require.ErrorIs(t, err, cause)
				if phase != "planner.load_tool_outputs" {
					assert.Same(t, cause, err)
				}
				span := preparationSpan(t, recorder.Ended(), phase)
				if canceled {
					assert.Equal(t, codes.Unset, span.Status().Code)
					assert.Empty(t, span.Events())
				} else {
					assert.Equal(t, codes.Error, span.Status().Code)
					require.Len(t, span.Events(), 1)
					attrs := attrsByKey(span.Events()[0].Attributes)
					assert.Equal(t, err.Error(), attrs["exception.message"].AsString())
				}
			})
		}
	}
}

// preparationSpanRecorder installs an in-memory provider for this non-parallel
// test, then restores the previous provider before other tests may use it.
func preparationSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return recorder
}

func preparationSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var matches []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			matches = append(matches, span)
		}
	}
	require.Len(t, matches, 1, "span %s must end exactly once", name)
	return matches[0]
}

func preparationSpanByID(t *testing.T, spans []sdktrace.ReadOnlySpan, id string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.SpanContext().SpanID().String() == id {
			return span
		}
	}
	t.Fatalf("missing parent span %s", id)
	return nil
}

func (s *preparationTraceStore) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	_, span := s.tracer.Start(ctx, "synthetic.store.read")
	defer span.End()
	if s.err != nil {
		return session.RunMeta{}, s.err
	}
	return s.Store.LoadRun(ctx, runID)
}

func (s *preparationTraceStore) ListRunRecords(ctx context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	if s.err != nil {
		return runlog.Page{}, s.err
	}
	return s.Store.ListRunRecords(ctx, runID, cursor, limit)
}

func (s preparationTraceMemory) LoadRun(context.Context, string, string) (memory.Snapshot, error) {
	return memory.Snapshot{}, s.err
}
