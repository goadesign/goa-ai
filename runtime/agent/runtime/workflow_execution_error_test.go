package runtime

// workflow_execution_error_test.go verifies transcript completion when an
// execution-layer failure occurs after the assistant tool-use turn is committed.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	failingRecordWorkflowContext struct {
		*testWorkflowContext
		state *failingRecordState
	}

	failingRecordState struct {
		err      error
		failAt   int
		failType string
		calls    int
		failed   bool
	}
)

func (w *failingRecordWorkflowContext) Context() context.Context {
	return engine.WithWorkflowContext(w.ctx, w)
}

func (w *failingRecordWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	ctx, cancel := context.WithCancel(w.ctx)
	child := *w.testWorkflowContext
	child.ctx = ctx
	child.parent = w.root()
	return &failingRecordWorkflowContext{
		testWorkflowContext: &child,
		state:               w.state,
	}, cancel
}

func (w *failingRecordWorkflowContext) PublishRecords(call engine.RecordActivityCall) error {
	for _, record := range call.Input.Records {
		w.state.calls++
		failAtCall := w.state.failAt > 0 && w.state.calls == w.state.failAt
		failAtType := w.state.failType != "" &&
			string(record.Type) == w.state.failType &&
			!w.state.failed
		if failAtCall || failAtType {
			w.state.failed = true
			return w.state.err
		}
	}
	return w.testWorkflowContext.PublishRecords(call)
}

func TestRunLoopRecordsPartialInlineResultsBeforeExecutionError(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	first := newAnyJSONSpec(tools.Ident("svc.first"))
	second := newAnyJSONSpec(tools.Ident("svc.second"))
	third := newAnyJSONSpec(tools.Ident("svc.third"))
	executed := make([]tools.Ident, 0, 3)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name:   "svc",
		Inline: true,
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			executed = append(executed, call.Name)
			if call.Name == second.Name {
				return nil, errors.New("second tool failed")
			}
			return Executed(&planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"ok": true},
			}), nil
		},
		Specs: []tools.ToolSpec{first, second, third},
	}))
	wfCtx := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
	}}
	initial := &PlanResult{ToolCalls: []ToolCall{
		{ToolCallID: "first-call", Name: first.Name, Payload: rawjson.Message(`{}`)},
		{ToolCallID: "second-call", Name: second.Name, Payload: rawjson.Message(`{}`)},
		{ToolCallID: "third-call", Name: third.Name, Payload: rawjson.Message(`{}`)},
	}}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, `inline tool "svc.second" failed`)
	require.Equal(t, []tools.Ident{first.Name, second.Name, third.Name}, executed)
	require.NoError(t, transcript.ValidatePlannerTranscript(base.Messages))
	require.GreaterOrEqual(t, len(base.Messages), 2)
	require.Len(t, base.Messages[1].Parts, 3)
	firstResult, ok := base.Messages[1].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.False(t, firstResult.IsError)
	secondResult, ok := base.Messages[1].Parts[1].(model.ToolResultPart)
	require.True(t, ok)
	require.True(t, secondResult.IsError)
	thirdResult, ok := base.Messages[1].Parts[2].(model.ToolResultPart)
	require.True(t, ok)
	require.False(t, thirdResult.IsError)
}

func TestRunLoopPreservesConcreteResultAndContinuesBookkeepingAfterHookError(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	budgeted := newAnyJSONSpec(tools.Ident("svc.lookup"))
	bookkeeping := newAnyJSONSpec(tools.Ident("svc.record"))
	bookkeeping.Bookkeeping = true
	executed := make([]tools.Ident, 0, 2)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name:   "svc",
		Inline: true,
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			executed = append(executed, call.Name)
			return Executed(&planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"ok": true},
			}), nil
		},
		Specs: []tools.ToolSpec{budgeted, bookkeeping},
	}))
	require.True(t, rt.isBookkeeping(bookkeeping.Name))
	wfCtx := &failingRecordWorkflowContext{
		testWorkflowContext: &testWorkflowContext{ctx: context.Background(), runtime: rt},
		state: &failingRecordState{
			err:      errors.New("result publication failed"),
			failType: "tool_result_received",
		},
	}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
	}}
	initial := &PlanResult{ToolCalls: []ToolCall{
		{ToolCallID: "budgeted-call", Name: budgeted.Name, Payload: rawjson.Message(`{}`)},
		{ToolCallID: "bookkeeping-call", Name: bookkeeping.Name, Payload: rawjson.Message(`{}`)},
	}}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, "result publication failed")
	require.Equal(t, []tools.Ident{budgeted.Name, bookkeeping.Name}, executed)
	require.NoError(t, transcript.ValidatePlannerTranscript(base.Messages))
	require.GreaterOrEqual(t, len(base.Messages), 2)
	require.Len(t, base.Messages[1].Parts, 2)
	for _, part := range base.Messages[1].Parts {
		result, ok := part.(model.ToolResultPart)
		require.True(t, ok)
		require.False(t, result.IsError)
	}
}

func TestRunLoopRecordsCompleteCapDenialBeforePublicationError(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	first := newAnyJSONSpec(tools.Ident("svc.first"))
	second := newAnyJSONSpec(tools.Ident("svc.second"))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{first, second},
	}))
	wfCtx := &failingRecordWorkflowContext{
		testWorkflowContext: &testWorkflowContext{ctx: context.Background(), runtime: rt},
		state: &failingRecordState{
			err:    errors.New("record publication failed"),
			failAt: 2,
		},
	}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
	}}
	initial := &PlanResult{ToolCalls: []ToolCall{
		{ToolCallID: "first-call", Name: first.Name, Payload: rawjson.Message(`{}`)},
		{ToolCallID: "second-call", Name: second.Name, Payload: rawjson.Message(`{}`)},
	}}
	caps := initialCaps(RunPolicy{MaxToolCalls: 4})
	caps.RemainingToolCalls = 0

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		caps,
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, "record publication failed")
	require.NoError(t, transcript.ValidatePlannerTranscript(base.Messages))
	require.GreaterOrEqual(t, len(base.Messages), 2)
	require.Len(t, base.Messages[1].Parts, 2)
}
