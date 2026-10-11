// These tests retain returned Task input before a continuation propagates an
// execution error. Cleanup must use the returned operation sequence once.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	cancellationUpdateStore struct {
		*testStore
		err error
	}
)

func TestTaskContinuationRetainsInputBeforeExecutionError(t *testing.T) {
	for _, mode := range []string{"returned_input", "canceled_update"} {
		t.Run(mode, func(t *testing.T) { testTaskContinuationOwnership(t, mode) })
	}
}

func testTaskContinuationOwnership(t *testing.T, mode string) {
	prior := errors.New("child progress persistence unavailable")
	store := &cancellationUpdateStore{testStore: newTestStore(), err: prior}
	if mode == "canceled_update" {
		store.err = nil
	}
	rt := New(store)
	spec := newAnyJSONSpec("remote.tools.pending")
	definition := testAgentDefinition("child.agent", "child.workflow", "child.queue", []tools.ToolSpec{spec}, nil)
	reg := AgentRegistration{Definition: definition, ExecuteToolActivity: "execute"}
	rt.agents["child.agent"] = reg
	seedTestToolset(rt, "remote.tools", spec)
	inputFor := func(key string) *mcp.InputRequired {
		return &mcp.InputRequired{Requests: map[string]mcp.InputRequest{key: {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}}}
	}
	initial, err := tooloperation.NewPendingTaskInput("owned-task", nil, inputFor("first"))
	require.NoError(t, err)
	call := ToolCall{Name: spec.Name, ToolCallID: "task-call", RunID: "child-run", AgentID: "child.agent", SessionID: "session", TurnID: "turn", Payload: rawjson.Message(`{}`)}
	batch := &stepBatch{records: []stepToolRecord{{call: call, mcpPending: initial, task: &taskExecution{TaskID: "owned-task"}}}}
	updates, cancels := 0, 0
	binding := rt.toolsets["remote.tools"]
	binding.DecodeInExecutor = true
	binding.Execute = func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
		if id, answers, updating := call.ExecutionContinuation.AsTaskUpdate(); updating {
			updates++
			assert.Equal(t, "owned-task", id)
			assert.Contains(t, answers, "first")
			assert.EqualValues(t, 1, call.ExecutionSequence)
			if mode == "canceled_update" {
				return nil, context.Canceled
			}
			pending, pendingErr := tooloperation.NewPendingTaskInput(id, nil, inputFor("second"))
			if pendingErr != nil {
				return nil, pendingErr
			}
			return Unfinished(pending)
		}
		id, cancel := call.ExecutionContinuation.AsTaskCancel()
		assert.True(t, cancel)
		assert.Equal(t, "owned-task", id)
		assert.EqualValues(t, 2, call.ExecutionSequence)
		cancels++
		pending, pendingErr := tooloperation.NewPendingTaskWait(id, nil)
		if pendingErr != nil {
			return nil, pendingErr
		}
		return Unfinished(pending)
	}
	rt.toolsets["remote.tools"] = binding
	wf := &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt}
	runCtx := run.Context{RunID: "child-run", SessionID: "session", TurnID: "turn", ParentRunID: "parent-run", ParentAgentID: "parent.agent", ParentToolCallID: "parent-call"}
	base := &workflowConversation{RunContext: runCtx, HistoryEndID: testToolHistory(t, rt, "child.agent", runCtx, nil)}
	loop := &workflowLoop{r: rt, wfCtx: wf, reg: reg, input: &RunInput{AgentID: "child.agent", RunID: runCtx.RunID, SessionID: runCtx.SessionID}, base: base, turnID: runCtx.TurnID, parentTracker: newChildTracker("parent-call"), unfinishedBatch: batch}
	_, executionErr := loop.applyExecutionContinuation(batch, pendingMCPInput(call, initial), &api.MCPInputResponse{ToolCallID: call.ToolCallID, Responses: map[string]json.RawMessage{"first": json.RawMessage(`{"action":"accept","content":{}}`)}})
	assert.Equal(t, 1, updates)
	if mode == "canceled_update" {
		require.ErrorIs(t, executionErr, context.Canceled)
		assert.Equal(t, 1, cancels, "active update remains batch-owned")
		assert.Nil(t, batch.records[0].task)
	} else {
		require.ErrorIs(t, executionErr, prior)
		assert.Zero(t, cancels, "returned Task input transfers cleanup to its workflow record")
		require.NotNil(t, batch.records[0].task)
		assert.Contains(t, pendingHostInput(batch.records[0].mcpPending).Requests, "second")
		assert.EqualValues(t, 1, batch.records[0].call.ExecutionSequence)
	}
	require.NoError(t, loop.cancelSavedWork(api.CancellationRequest{RunID: runCtx.RunID, Reason: run.CancellationReasonEngineCanceled}))
	assert.Equal(t, 1, cancels)
}

// AppendRunRecord fails only the parent progress event; Task operations and all
// other records use the real store so the caller sees the original write error.
func (s *cancellationUpdateStore) AppendRunRecord(ctx context.Context, event *runlog.Event) (storage.AppendResult, error) {
	if event.Type == hooks.ToolCallUpdated && s.err != nil {
		return storage.AppendResult{}, s.err
	}
	return s.testStore.AppendRunRecord(ctx, event)
}
