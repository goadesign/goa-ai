package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/memory"
	memoryinmem "goa.design/goa-ai/runtime/agent/memory/inmem"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestAttachContinuationKeepsProviderCursorInternal(t *testing.T) {
	cursor := "provider-cursor-with-large-opaque-state"
	result := &planner.ToolResult{Bounds: &agent.Bounds{NextCursor: &cursor}}
	call := planner.ToolRequest{
		Name: "svc.list", RunID: "run-1", SessionID: "session-1", ToolCallID: "call-1",
	}

	err := attachContinuation(call, result)

	require.NoError(t, err)
	require.NotNil(t, result.Bounds.Continuation)
	assert.Equal(t, cursor, *result.Bounds.NextCursor)
	assert.NotContains(t, *result.Bounds.Continuation, cursor)
	assert.Less(t, len(*result.Bounds.Continuation), len(cursor)+30)
	assert.NoError(t, validateContinuationScope(*result.Bounds.Continuation, "run-1", "session-1", "svc.list"))
}

func TestAttachContinuationRejectsEmptyProviderCursor(t *testing.T) {
	cursor := ""
	result := &planner.ToolResult{Bounds: &agent.Bounds{NextCursor: &cursor}}
	call := planner.ToolRequest{Name: "svc.list", RunID: "run-1", SessionID: "session-1", ToolCallID: "call-1"}

	err := attachContinuation(call, result)

	assert.ErrorContains(t, err, "empty provider cursor")
}

func TestResolvePlanContinuationsPreservesModelPayload(t *testing.T) {
	runtime, input, result, reference, cursor := continuationFixture(t)

	err := runtime.resolvePlanContinuations(context.Background(), input, result)

	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	assert.JSONEq(t, `{"cursor":"`+reference+`"}`, string(result.ToolCalls[0].ModelPayload))
	assert.JSONEq(t, `{"site":"reno","cursor":"`+cursor+`"}`, string(result.ToolCalls[0].Payload))
}

func TestResolvePlanContinuationsRejectsInvalidUse(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *Runtime, *PlanActivityInput, *planner.PlanResult, string, string)
		wantErr string
	}{
		{
			name: "cross run",
			mutate: func(_ *testing.T, _ *Runtime, input *PlanActivityInput, _ *planner.PlanResult, _, _ string) {
				input.RunID = "run-2"
			},
			wantErr: "belongs to another run",
		},
		{
			name: "cross session",
			mutate: func(_ *testing.T, _ *Runtime, input *PlanActivityInput, _ *planner.PlanResult, _, _ string) {
				input.RunContext.SessionID = "session-2"
			},
			wantErr: "belongs to another session",
		},
		{
			name: "cross tool",
			mutate: func(_ *testing.T, runtime *Runtime, _ *PlanActivityInput, result *planner.PlanResult, _, _ string) {
				result.ToolCalls[0].Name = "svc.other"
				runtime.toolSpecs["svc.other"] = continuationToolSpec("svc.other")
			},
			wantErr: "belongs to another tool",
		},
		{
			name: "extra argument",
			mutate: func(_ *testing.T, _ *Runtime, _ *PlanActivityInput, result *planner.PlanResult, reference, _ string) {
				result.ToolCalls[0].Payload = rawjson.Message(`{"site":"boise","cursor":"` + reference + `"}`)
			},
			wantErr: "must be the only argument",
		},
		{
			name: "stale reference",
			mutate: func(t *testing.T, runtime *Runtime, input *PlanActivityInput, _ *planner.PlanResult, _, cursor string) {
				t.Helper()
				event := memory.NewEvent(time.Unix(3, 0), memory.ToolCallData{
					ToolCallID: "call-2",
					ToolName:   "svc.list",
					PayloadJSON: rawjson.Message(
						`{"site":"reno","cursor":"` + cursor + `"}`,
					),
				}, nil)
				require.NoError(t, runtime.Memory.AppendEvents(
					context.Background(), string(input.AgentID), input.RunID, event,
				))
			},
			wantErr: "is stale",
		},
		{
			name: "non-string reference",
			mutate: func(_ *testing.T, _ *Runtime, _ *PlanActivityInput, result *planner.PlanResult, _, _ string) {
				result.ToolCalls[0].Payload = rawjson.Message(`{"cursor":42}`)
			},
			wantErr: "must be a non-empty string",
		},
		{
			name: "duplicate batch use",
			mutate: func(_ *testing.T, _ *Runtime, _ *PlanActivityInput, result *planner.PlanResult, _, _ string) {
				result.ToolCalls = append(result.ToolCalls, result.ToolCalls[0])
			},
			wantErr: "used more than once",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, input, result, reference, cursor := continuationFixture(t)
			test.mutate(t, runtime, input, result, reference, cursor)
			err := runtime.resolvePlanContinuations(context.Background(), input, result)
			require.NoError(t, err)
			failure := result.ToolCalls[0].PreflightFailure
			require.NotNil(t, failure)
			assert.Equal(t, planner.FailureInvalidCall, failure.Kind)
			assert.Equal(t, planner.RecoveryReplan, failure.Recovery.Action)
			require.ErrorContains(t, failure.Error, test.wantErr)
			if test.name == "duplicate batch use" {
				require.Len(t, result.ToolCalls, 2)
				require.NotNil(t, result.ToolCalls[1].PreflightFailure)
				assert.ErrorContains(t, result.ToolCalls[1].PreflightFailure.Error, test.wantErr)
			}
		})
	}
}

func TestResolvePlanContinuationsRejectsPlannerAuthoredPreflightFailure(t *testing.T) {
	runtime, input, result, _, _ := continuationFixture(t)
	result.ToolCalls[0].PreflightFailure = invalidContinuationFailure(assert.AnError)

	err := runtime.resolvePlanContinuations(context.Background(), input, result)

	assert.ErrorContains(t, err, "planner authored preflight failure")
}

func TestResolveContinuationFailsOnContradictoryDurableState(t *testing.T) {
	const cursor = "provider-cursor"
	reference := continuationReference("run-1", "session-1", "svc.list", "call-1", cursor)
	events := []memory.Event{memory.NewEvent(time.Unix(1, 0), memory.ToolResultData{
		ToolCallID: "call-1",
		ToolName:   "svc.list",
		Bounds: &agent.Bounds{
			Continuation: stringPointer(reference),
		},
	}, nil)}

	_, _, err := resolveContinuation(
		events,
		"svc.list",
		"cursor",
		reference,
		map[string]any{"cursor": reference},
	)

	require.Error(t, err)
	var useErr *continuationUseError
	assert.NotErrorAs(t, err, &useErr)
}

func TestResolvePlanContinuationsRequiresDurableHistory(t *testing.T) {
	runtime, input, result, _, _ := continuationFixture(t)
	runtime.Memory = nil

	err := runtime.resolvePlanContinuations(context.Background(), input, result)

	assert.ErrorContains(t, err, "requires durable memory")
}

func TestResolvePlanContinuationsRejectsCrossScopeBeforeReadingMemory(t *testing.T) {
	runtime, input, result, _, _ := continuationFixture(t)
	input.RunContext.SessionID = "session-2"
	runtime.Memory = nil

	err := runtime.resolvePlanContinuations(context.Background(), input, result)

	require.NoError(t, err)
	require.NotNil(t, result.ToolCalls[0].PreflightFailure)
	assert.ErrorContains(t, result.ToolCalls[0].PreflightFailure.Error, "belongs to another session")
}

func continuationFixture(t *testing.T) (*Runtime, *PlanActivityInput, *planner.PlanResult, string, string) {
	t.Helper()
	const (
		agentID  = "svc.agent"
		runID    = "run-1"
		session  = "session-1"
		toolName = tools.Ident("svc.list")
		callID   = "call-1"
		cursor   = "opaque-provider-cursor"
	)
	reference := continuationReference(runID, session, toolName, callID, cursor)
	store := memoryinmem.New()
	events := []memory.Event{
		memory.NewEvent(time.Unix(1, 0), memory.ToolCallData{
			ToolCallID: callID,
			ToolName:   toolName,
			PayloadJSON: rawjson.Message(
				`{"site":"reno"}`,
			),
		}, nil),
		memory.NewEvent(time.Unix(2, 0), memory.ToolResultData{
			ToolCallID: callID,
			ToolName:   toolName,
			Bounds: &agent.Bounds{
				Returned:     20,
				Truncated:    true,
				NextCursor:   stringPointer(cursor),
				Continuation: stringPointer(reference),
			},
		}, nil),
	}
	require.NoError(t, store.AppendEvents(context.Background(), agentID, runID, events...))
	runtime := &Runtime{
		Memory: store,
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			toolName: continuationToolSpec(toolName),
		},
	}
	input := &PlanActivityInput{
		AgentID: agentID,
		RunID:   runID,
		RunContext: run.Context{
			RunID:     runID,
			SessionID: session,
		},
	}
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    toolName,
		Payload: rawjson.Message(`{"cursor":"` + reference + `"}`),
	}}}
	return runtime, input, result, reference, cursor
}

func continuationToolSpec(name tools.Ident) tools.ToolSpec {
	return tools.ToolSpec{
		Name: name,
		Bounds: &tools.BoundsSpec{Paging: &tools.PagingSpec{
			CursorField: "cursor", NextCursorField: "next_cursor",
		}},
	}
}

func stringPointer(value string) *string {
	return &value
}
