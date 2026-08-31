// client_continuation_test.go verifies continuation identity before a workflow
// or its pending run metadata can be created.
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestPrepareContinuationRejectsIdentityMismatchBeforeWorkflowStart(t *testing.T) {
	tests := []struct {
		name      string
		agentID   agent.Ident
		sessionID string
		wantError string
	}{
		{name: "agent", agentID: "svc.other", sessionID: "session-1", wantError: "agent mismatch"},
		{name: "session", agentID: "svc.agent", sessionID: "session-2", wantError: "session mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &stubEngine{}
			sessions := newTestStore()
			runtime := &Runtime{
				Engine:  eng,
				logger:  telemetry.NoopLogger{},
				metrics: telemetry.NoopMetrics{},
				tracer:  telemetry.NoopTracer{},
				Store:   sessions,
				agents: map[agent.Ident]AgentRegistration{
					"svc.agent": {Definition: testAgentDefinition("svc.agent", "agent.workflow", "q", nil, nil)},
					"svc.other": {Definition: testAgentDefinition("svc.other", "other.workflow", "q", nil, nil)},
				},
			}
			spec := newAnyJSONSpec("svc.lookup")
			seedTestToolSpecs(runtime, spec)
			_, err := createSessionForTest(context.Background(), runtime.Store, "session-1")
			require.NoError(t, err)
			_, err = createSessionForTest(context.Background(), runtime.Store, "session-2")
			require.NoError(t, err)
			suspension := suspensionContractFixtureWithContext(
				t, spec.Name, "svc.agent", "run-1", nil, nil,
			)
			now := time.Now().UTC()
			admitRunForTest(t, sessions, session.RunMeta{
				AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
				Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
			})
			data, err := json.Marshal(suspension)
			require.NoError(t, err)
			require.NoError(t, storeSuspensionForTest(context.Background(), sessions, "run-1", session.RunSuspension{
				ID: suspension.ID, Data: data,
			}))

			client := runtime.MustClient(tt.agentID)
			_, err = client.PrepareContinuation(
				context.Background(),
				tt.sessionID,
				"run-1",
				"run-2",
				"turn-2",
				&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
					ID: "clarification-1", Answer: "Building A",
				}},
				WorkflowOptions{},
			)
			require.ErrorContains(t, err, tt.wantError)
			require.ErrorIs(t, err, ErrContinuationRejected)
			require.Empty(t, eng.last.Workflow)
			_, err = sessions.LoadRun(context.Background(), "run-2")
			require.ErrorIs(t, err, session.ErrRunNotFound)
		})
	}
}

func TestPrepareContinuationRejectsWrongPendingResponseBeforeWorkflowStart(t *testing.T) {
	eng := &stubEngine{}
	sessions := newTestStore()
	runtime := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   sessions,
		agents: map[agent.Ident]AgentRegistration{
			"svc.agent": {
				Definition: testAgentDefinition("svc.agent", "agent.workflow", "q", nil, nil),
			},
		},
	}
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	_, err := createSessionForTest(context.Background(), runtime.Store, "session-1")
	require.NoError(t, err)
	suspension := suspensionContractFixtureWithContext(t, spec.Name, "svc.agent", "run-1", nil, nil)
	now := time.Now().UTC()
	admitRunForTest(t, sessions, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(context.Background(), sessions, "run-1", session.RunSuspension{
		ID: suspension.ID, Data: data,
	}))

	client := runtime.MustClient("svc.agent")
	_, err = client.PrepareContinuation(
		context.Background(),
		"session-1",
		"run-1",
		"run-2",
		"turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-other", Answer: "Building A",
		}},
		WorkflowOptions{},
	)
	require.ErrorContains(t, err, "does not match pending id")
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.Empty(t, eng.last.Workflow)
	_, err = sessions.LoadRun(context.Background(), "run-2")
	require.ErrorIs(t, err, session.ErrRunNotFound)

	directInput, err := buildContinuationRunInput(
		"svc.agent",
		"session-1",
		"run-3",
		"turn-3",
		suspension,
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-other",
		}},
	)
	require.NoError(t, err)
	err = runtime.buildAndSubmitWorkflowForTest(context.Background(), directInput, "agent.workflow", "q", true)
	require.ErrorContains(t, err, "does not match pending id")
	require.Empty(t, eng.last.Workflow)
	_, err = sessions.LoadRun(context.Background(), "run-3")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func TestPreparedRunOwnsExactContinuationStartInput(t *testing.T) {
	eng := &stubEngine{}
	sessions := newTestStore()
	runtime := &Runtime{
		Engine: eng, Store: sessions,
		logger: telemetry.NoopLogger{}, metrics: telemetry.NoopMetrics{}, tracer: telemetry.NoopTracer{},
	}
	spec := newProjectedResultSpec()
	spec.Name = "svc.lookup"
	definition := testAgentDefinition("svc.agent", "agent.workflow", "agent-q", []tools.ToolSpec{spec}, nil)
	client := runtime.MustClientFor(definition)
	_, err := createSessionForTest(t.Context(), sessions, "session-1")
	require.NoError(t, err)
	suspension := externalResultsSuspensionFixture(t, spec.Name)
	now := time.Now().UTC()
	admitRunForTest(t, sessions, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), sessions, "run-1", session.RunSuspension{
		ID: suspension.ID, Data: data,
	}))
	total := 2
	cursor := "next-original"
	response := &api.PendingInputResponse{ToolResults: &api.ToolResultsSet{
		ID: "external-1",
		Results: []*api.ProvidedToolResult{{
			Name: spec.Name, ToolCallID: "call-1",
			Success: &api.ProvidedToolSuccess{
				Result: rawjson.Message(`{"results":["original"]}`),
				Bounds: &agent.Bounds{
					Returned: 1, Total: &total, Truncated: true, NextCursor: &cursor,
				},
			},
		}},
	}}
	_, err = client.PrepareContinuation(
		t.Context(), "session-1", "run-1", "run-invalid", "turn-invalid", response,
		WorkflowOptions{Memo: map[string]any{"": "missing name"}},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.NotErrorIs(t, err, ErrPreparedRunRejected)
	require.Zero(t, eng.sealCalls)
	require.Zero(t, eng.startCalls)
	_, err = client.PrepareContinuation(
		t.Context(), "session-1", "run-1", "run-invalid", "turn-invalid", response,
		WorkflowOptions{SearchAttributes: map[string]any{"SessionID": "another-session"}},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, "does not match session id")
	require.Zero(t, eng.sealCalls)
	require.Zero(t, eng.startCalls)

	options := WorkflowOptions{
		TaskQueue: "override-q",
		Memo: map[string]any{
			"nested": map[string]any{"value": "original"},
		},
		SearchAttributes: map[string]any{"SessionID": "session-1"},
	}
	prepared, err := client.PrepareContinuation(
		t.Context(), "session-1", "run-1", "run-2", "turn-2", response, options,
	)
	require.NoError(t, err)

	const mutated = "mutated"
	response.ToolResults.Results[0].Success.Result[13] = 'X'
	*response.ToolResults.Results[0].Success.Bounds.Total = 99
	*response.ToolResults.Results[0].Success.Bounds.NextCursor = mutated
	options.Memo["nested"].(map[string]any)["value"] = mutated
	other := runtime.MustClientFor(testAgentDefinition(
		"svc.other", "other.workflow", "other-q", []tools.ToolSpec{spec}, nil))
	_, err = other.StartPrepared(t.Context(), prepared)
	require.ErrorIs(t, err, ErrPreparedRunRejected)
	require.Zero(t, eng.startCalls)

	_, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	first := eng.last.Input
	require.JSONEq(t, `{"results":["original"]}`, string(first.Continuation.Response.ToolResults.Results[0].Success.Result))
	require.Equal(t, 2, *first.Continuation.Response.ToolResults.Results[0].Success.Bounds.Total)
	require.Equal(t, "next-original", *first.Continuation.Response.ToolResults.Results[0].Success.Bounds.NextCursor)
	require.Equal(t, "original", decodePreparedMemo[map[string]any](t, eng.last.Memo, "nested")["value"])
	require.Equal(t, "override-q", eng.last.TaskQueue)

	first.Continuation.Response.ToolResults.Results[0].Success.Result[13] = 'Y'
	mutatedMemo := eng.last.Memo["nested"]
	mutatedMemo.Data[0] ^= 0xff
	_, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	retry := eng.last.Input
	require.JSONEq(t, `{"results":["original"]}`, string(retry.Continuation.Response.ToolResults.Results[0].Success.Result))
	require.Equal(t, "original", decodePreparedMemo[map[string]any](t, eng.last.Memo, "nested")["value"])
	require.Equal(t, 2, eng.startCalls)
}

func TestPrepareContinuationRejectsInvalidProvidedResultBeforeEngineStart(t *testing.T) {
	eng := &stubEngine{}
	sessions := newTestStore()
	runtime := &Runtime{
		Engine: eng, Store: sessions,
		logger: telemetry.NoopLogger{}, metrics: telemetry.NoopMetrics{}, tracer: telemetry.NoopTracer{},
	}
	spec := newProjectedResultSpec()
	spec.Name = "svc.lookup"
	client := runtime.MustClientFor(testAgentDefinition(
		"svc.agent", "agent.workflow", "agent-q", []tools.ToolSpec{spec}, nil))
	_, err := createSessionForTest(t.Context(), sessions, "session-1")
	require.NoError(t, err)
	suspension := externalResultsSuspensionFixture(t, spec.Name)
	now := time.Now().UTC()
	admitRunForTest(t, sessions, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), sessions, "run-1", session.RunSuspension{
		ID: suspension.ID, Data: data,
	}))

	total := 1
	_, err = client.PrepareContinuation(
		t.Context(), "session-1", "run-1", "run-2", "turn-2",
		&api.PendingInputResponse{ToolResults: &api.ToolResultsSet{
			ID: "external-1",
			Results: []*api.ProvidedToolResult{{
				Name: spec.Name, ToolCallID: "call-1",
				Success: &api.ProvidedToolSuccess{
					Result: rawjson.Message(`{"results":"wrong"}`),
					Bounds: &agent.Bounds{Returned: 1, Total: &total},
				},
			}},
		}},
		WorkflowOptions{},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, "generated contract")
	require.Zero(t, eng.startCalls)
}

func TestPrepareContinuationValidatesGrandchildDefinitionBeforeEngineStart(t *testing.T) {
	eng := &stubEngine{}
	sessions := newTestStore()
	runtime := &Runtime{
		Engine: eng, Store: sessions,
		logger: telemetry.NoopLogger{}, metrics: telemetry.NoopMetrics{}, tracer: telemetry.NoopTracer{},
	}
	leafTool := newAnyJSONSpec("leaf.lookup")
	middleTool := newAnyJSONSpec("middle.delegate")
	middleTool.IsAgentTool = true
	middleTool.AgentID = "leaf.agent"
	rootTool := newAnyJSONSpec("root.delegate")
	rootTool.IsAgentTool = true
	rootTool.AgentID = "middle.agent"
	leafSuspension := suspensionContractFixtureWithContext(
		t, leafTool.Name, "leaf.agent", "leaf-run", nil, nil,
	)
	middleSuspension := nestedChildSuspensionFixture(
		t, "middle.agent", "middle-run", middleTool, leafSuspension,
	)
	rootSuspension := nestedChildSuspensionFixture(
		t, "root.agent", "root-run", rootTool, middleSuspension,
	)
	_, err := createSessionForTest(t.Context(), sessions, "session-1")
	require.NoError(t, err)
	now := time.Now().UTC()
	admitRunForTest(t, sessions, session.RunMeta{
		AgentID: "root.agent", RunID: "root-run", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(rootSuspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), sessions, "root-run", session.RunSuspension{
		ID: rootSuspension.ID, Data: data,
	}))
	middleDefinition := testAgentDefinition(
		"middle.agent", "middle.workflow", "middle-q", []tools.ToolSpec{middleTool}, nil)

	removedLeafDefinition := testAgentDefinition(
		"leaf.agent", "leaf.workflow", "leaf-q", nil, nil)

	removedDefinition := testAgentDefinitionWithChildren(
		"root.agent", "root.workflow", "root-q", []tools.ToolSpec{rootTool}, nil,
		[]AgentDefinition{middleDefinition, removedLeafDefinition})

	response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
		ID: "clarification-1", Answer: "Unit 7",
	}}
	_, err = runtime.MustClientFor(removedDefinition).PrepareContinuation(
		t.Context(), "session-1", "root-run", "successor", "turn-2", response, WorkflowOptions{},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, `requires tool "leaf.lookup" removed`)
	require.Zero(t, eng.startCalls)

	leafDefinition := testAgentDefinition(
		"leaf.agent", "leaf.workflow", "leaf-q", []tools.ToolSpec{leafTool}, nil)

	currentDefinition := testAgentDefinitionWithChildren(
		"root.agent", "root.workflow", "root-q", []tools.ToolSpec{rootTool}, nil,
		[]AgentDefinition{middleDefinition, leafDefinition})

	prepared, err := runtime.MustClientFor(currentDefinition).PrepareContinuation(
		t.Context(), "session-1", "root-run", "successor", "turn-2", response, WorkflowOptions{},
	)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Zero(t, eng.startCalls)
}

func TestPrepareContinuationRejectsDuplicateSavedChildCallBeforeEngineStart(t *testing.T) {
	eng := &stubEngine{}
	sessions := newTestStore()
	runtime := &Runtime{
		Engine: eng, Store: sessions,
		logger: telemetry.NoopLogger{}, metrics: telemetry.NoopMetrics{}, tracer: telemetry.NoopTracer{},
	}
	childTool := newAnyJSONSpec("child.lookup")
	parentTool := newAnyJSONSpec("parent.delegate")
	parentTool.IsAgentTool = true
	parentTool.AgentID = "child.agent"
	childSuspension := suspensionContractFixtureWithContext(
		t, childTool.Name, "child.agent", "child-run", nil, nil,
	)
	parentSuspension := nestedChildSuspensionFixture(
		t, "parent.agent", "parent-run", parentTool, childSuspension,
	)
	rewriteSuspensionCheckpointAndPublic(t, parentSuspension, func(checkpoint *workflowCheckpoint) {
		call := checkpoint.Batch.Records[0].Call
		checkpoint.Batch.Records = append([]checkpointToolRecord{{
			Call: call,
			Result: &api.ToolEvent{
				Name: call.Name, Result: rawjson.Message(`{"completed":true}`), ToolCallID: call.ToolCallID,
			},
		}}, checkpoint.Batch.Records...)
	})
	_, err := createSessionForTest(t.Context(), sessions, "session-1")
	require.NoError(t, err)
	now := time.Now().UTC()
	admitRunForTest(t, sessions, session.RunMeta{
		AgentID: "parent.agent", RunID: "parent-run", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(parentSuspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), sessions, "parent-run", session.RunSuspension{
		ID: parentSuspension.ID, Data: data,
	}))
	definition := testAgentDefinitionWithChildren(
		"parent.agent", "parent.workflow", "parent-q", []tools.ToolSpec{parentTool}, nil,
		[]AgentDefinition{testAgentDefinition(
			"child.agent", "child.workflow", "child-q", []tools.ToolSpec{childTool}, nil)})

	_, err = runtime.MustClientFor(definition).PrepareContinuation(
		t.Context(), "session-1", "parent-run", "successor", "turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-1", Answer: "Unit 7",
		}},
		WorkflowOptions{},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, `duplicate saved tool call id "child-call"`)
	require.Zero(t, eng.startCalls)
}

func TestPrepareContinuationRejectsDuplicateModelToolCallIDBeforeEngineStart(t *testing.T) {
	eng := &stubEngine{}
	sessions := newTestStore()
	runtime := &Runtime{
		Engine: eng, Store: sessions,
		logger: telemetry.NoopLogger{}, metrics: telemetry.NoopMetrics{}, tracer: telemetry.NoopTracer{},
	}
	spec := newAnyJSONSpec("svc.lookup")
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpointAndPublic(t, suspension, func(checkpoint *workflowCheckpoint) {
		calls := []ToolCall{
			{
				Name: spec.Name, ToolCallID: "call-1", ModelToolCallID: "model-call",
				Payload: rawjson.Message(`{"query":"first"}`),
			},
			{
				Name: spec.Name, ToolCallID: "call-2", ModelToolCallID: "model-call",
				Payload: rawjson.Message(`{"query":"second"}`),
			},
		}
		checkpoint.Batch.Result.ToolCalls = calls
		checkpoint.Batch.Calls = calls
	})
	_, err := createSessionForTest(t.Context(), sessions, "session-1")
	require.NoError(t, err)
	now := time.Now().UTC()
	admitRunForTest(t, sessions, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), sessions, "run-1", session.RunSuspension{
		ID: suspension.ID, Data: data,
	}))
	definition := testAgentDefinition(
		"svc.agent", "agent.workflow", "agent-q", []tools.ToolSpec{spec}, nil)

	_, err = runtime.MustClientFor(definition).PrepareContinuation(
		t.Context(), "session-1", "run-1", "successor", "turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-1", Answer: "Unit 7",
		}},
		WorkflowOptions{},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, "duplicate tool_call_id model-call")
	require.Zero(t, eng.startCalls)
}

// externalResultsSuspensionFixture creates a valid checkpoint whose first
// pending request expects one externally supplied tool result.
func externalResultsSuspensionFixture(t *testing.T, tool tools.Ident) *api.RunSuspension {
	t.Helper()
	suspension := suspensionContractFixture(t, tool)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		await := planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
			ID: "external-1",
			Items: []planner.AwaitToolItem{{
				Name: tool, ToolCallID: "call-1", ModelToolCallID: "model-call-1",
				Payload: rawjson.Message(`{"query":"status"}`),
			}},
		})
		checkpoint.Batch = checkpointStepBatch{
			Result:     &PlanResult{Await: planner.NewAwait(await)},
			AwaitItems: []planner.AwaitItem{await},
			Kind:       stepKindAwait,
		}
		checkpoint.Pending = []checkpointPendingInput{{Await: &await, CallRunID: "run-1"}}
		checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
	})
	var checkpoint workflowCheckpoint
	require.NoError(t, json.Unmarshal(suspension.Checkpoint, &checkpoint))
	pending, err := publicPendingInputs(checkpoint.Pending)
	require.NoError(t, err)
	suspension.Pending = pending
	suspension.RequiredTools = append([]tools.Ident(nil), checkpoint.RequiredTools...)
	return suspension
}

// nestedChildSuspensionFixture creates a valid parent checkpoint whose first
// pending request belongs to one suspended child agent.
func nestedChildSuspensionFixture(
	t *testing.T,
	agentID, runID string,
	tool tools.ToolSpec,
	child *api.RunSuspension,
) *api.RunSuspension {
	t.Helper()
	suspension := suspensionContractFixtureWithContext(t, tool.Name, agentID, runID, nil, nil)
	rewriteSuspensionCheckpointAndPublic(t, suspension, func(checkpoint *workflowCheckpoint) {
		call := ToolCall{
			Name: tool.Name, ToolCallID: "child-call", ModelToolCallID: "model-child-call",
			Payload: rawjson.Message(`{}`), ModelPayload: rawjson.Message(`{}`),
		}
		checkpoint.Batch = checkpointStepBatch{
			Result: &PlanResult{ToolCalls: []ToolCall{call}},
			Calls:  []ToolCall{call},
			Kind:   stepKindTools,
			Records: []checkpointToolRecord{{
				Call: call, ChildSuspension: child,
			}},
		}
		checkpoint.Pending = []checkpointPendingInput{{Child: &checkpointChildContinuation{
			ToolCallID: call.ToolCallID, Suspension: child,
		}}}
	})
	return suspension
}

// rewriteSuspensionCheckpointAndPublic keeps the private checkpoint and public
// continuation envelope synchronized after one test mutation.
func rewriteSuspensionCheckpointAndPublic(
	t *testing.T,
	suspension *api.RunSuspension,
	mutate func(*workflowCheckpoint),
) {
	t.Helper()
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		mutate(checkpoint)
		checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
	})
	var checkpoint workflowCheckpoint
	require.NoError(t, json.Unmarshal(suspension.Checkpoint, &checkpoint))
	pending, err := publicPendingInputs(checkpoint.Pending)
	require.NoError(t, err)
	suspension.Pending = pending
	suspension.RequiredTools = append([]tools.Ident(nil), checkpoint.RequiredTools...)
}
