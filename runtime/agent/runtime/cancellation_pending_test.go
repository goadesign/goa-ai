// These tests cancel a batch after an earlier tool asks for input. The workflow
// must close cancellation without treating pending work as a completed result.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

func TestWorkflowCancellationAfterTaskInput(t *testing.T) {
	testWorkflowCancellationAfterTaskInput(t, "same_batch")
}

func TestWorkflowCancellationAfterTaskInputAcrossTimeoutGroups(t *testing.T) {
	testWorkflowCancellationAfterTaskInput(t, "timeout_groups")
}

func TestWorkflowCancellationAfterMixedTaskOutcomes(t *testing.T) {
	for _, mode := range []string{"completed_sibling", "cleanup_failure", "waiting_sibling", "budget_classes"} {
		t.Run(mode, func(t *testing.T) { testWorkflowCancellationAfterTaskInput(t, mode) })
	}
}

func testWorkflowCancellationAfterTaskInput(t *testing.T, mode string) {
	pendingSpec := newAnyJSONSpec("remote.tools.pending")
	cancelSpec := newAnyJSONSpec("remote.tools.cancel")
	completedSpec := newAnyJSONSpec("remote.tools.completed")
	waitingSpec := newAnyJSONSpec("remote.tools.waiting")
	if mode == "budget_classes" {
		cancelSpec.Bookkeeping = true
	}
	specs := []tools.ToolSpec{pendingSpec, cancelSpec, completedSpec, waitingSpec}
	requests := []planner.ToolRequest{{Name: pendingSpec.Name, Payload: rawjson.Message(`{}`)}, {Name: cancelSpec.Name, Payload: rawjson.Message(`{}`)}}
	if mode == "completed_sibling" {
		requests = []planner.ToolRequest{{Name: completedSpec.Name, Payload: rawjson.Message(`{}`)}, requests[1], requests[0]}
	}
	if mode == "waiting_sibling" {
		requests = []planner.ToolRequest{{Name: waitingSpec.Name, Payload: rawjson.Message(`{}`)}, requests[1], requests[0]}
	}
	cleanupFailure := errors.New("Task cancellation delivery failed")
	store := &cancellationScheduleStore{testStore: newTestStore()}
	pl := &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		return &planner.PlanResult{ToolCalls: requests}, nil
	}}
	rt := newTestRuntimeWithPlanner("synthetic.agent", pl)
	rt.Store = store
	reg := rt.agents["synthetic.agent"]
	reg.Definition = testRegistrationDefinition("synthetic.agent", engine.WorkflowDefinition{}, specs)
	reg.PlanActivityName, reg.ResumeActivityName, reg.ExecuteToolActivity = "plan", "resume", "execute"
	rt.agents["synthetic.agent"] = reg
	creates, cancels := 0, 0
	canceledTasks := make(map[string]int)
	rt.toolsets["remote.tools"] = ToolsetRegistration{
		DecodeInExecutor: true,
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			if operation := call.ExecutionContinuation; operation != nil {
				id, cancel := operation.AsTaskCancel()
				assert.True(t, cancel)
				assert.Contains(t, []string{"accepted-task", "waiting-task"}, id)
				cancels++
				canceledTasks[id]++
				if mode == "cleanup_failure" {
					return nil, engine.MarkActivityErrorNonRetryable(cleanupFailure)
				}
				pending, err := tooloperation.NewPendingTaskWait(id, nil)
				if err != nil {
					return nil, err
				}
				return Unfinished(pending)
			}
			if call.Name == cancelSpec.Name {
				return nil, context.Canceled
			}
			if call.Name == completedSpec.Name {
				return Executed(successfulToolResult(call)), nil
			}
			creates++
			if call.Name == waitingSpec.Name {
				hint := int64(time.Minute / time.Millisecond)
				pending, err := tooloperation.NewPendingTaskWait("waiting-task", &hint)
				if err != nil {
					return nil, err
				}
				return Unfinished(pending)
			}
			pending, err := tooloperation.NewPendingTaskInput("accepted-task", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
				"input": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
			}})
			if err != nil {
				return nil, err
			}
			return Unfinished(pending)
		},
	}
	seedTestToolset(rt, "remote.tools", specs...)
	seedTestToolDefinitions(rt, specs...)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"synthetic.agent": specs}
	wf := &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt}
	input := &RunInput{AgentID: "synthetic.agent", RunID: "run-pending", SessionID: "session-pending"}
	if mode == "timeout_groups" {
		input.Policy = &PolicyOverrides{PerToolTimeout: map[tools.Ident]time.Duration{pendingSpec.Name: time.Second, cancelSpec.Name: 2 * time.Second}}
	}
	publishTestRunInput(t, rt, input, nil)
	out, workflowErr := rt.ExecuteWorkflow(wf, input)
	assert.Nil(t, out)
	require.ErrorIs(t, workflowErr, context.Canceled)
	expectedTasks := 1
	if mode == "waiting_sibling" {
		expectedTasks = 2
	}
	assert.Equal(t, expectedTasks, creates)
	assert.Equal(t, expectedTasks, cancels)
	assert.Equal(t, 1, canceledTasks["accepted-task"])
	if mode == "waiting_sibling" {
		assert.Equal(t, 1, canceledTasks["waiting-task"])
	}
	assert.Len(t, store.schedules, len(requests))
	page, err := store.ListRunRecords(t.Context(), input.RunID, "", 100)
	require.NoError(t, err)
	expectedResults := 0
	if mode == "completed_sibling" {
		expectedResults = 1
	}
	assert.Equal(t, expectedResults, countRunEventsByType(page, hooks.ToolResultReceived))
	assert.Zero(t, countRunEventsByType(page, hooks.AwaitMCPInput))
	assert.Equal(t, 1, countRunEventsByType(page, hooks.RunCompleted))
	meta, err := store.LoadRun(t.Context(), input.RunID)
	require.NoError(t, err)
	if mode == "cleanup_failure" {
		require.ErrorIs(t, workflowErr, cleanupFailure)
		assert.Equal(t, session.RunStatusFailed, meta.Status)
	} else {
		assert.Equal(t, session.RunStatusCanceled, meta.Status)
	}
	t.Logf("workflow error: %v; creates=%d cancels=%d status=%s", workflowErr, creates, cancels, meta.Status)
}

func TestWorkflowCancellationAfterChildTaskInput(t *testing.T) {
	pendingSpec := newAnyJSONSpec("remote.tools.pending")
	first := newAnyJSONSpec("first.agent.run")
	first.IsAgentTool, first.AgentID = true, "first.agent"
	second := newAnyJSONSpec("second.agent.run")
	second.IsAgentTool, second.AgentID = true, "second.agent"
	childDefinition := testAgentDefinition("first.agent", "first.workflow", "first.queue", []tools.ToolSpec{pendingSpec}, nil)
	cancelDefinition := testAgentDefinition("second.agent", "second.workflow", "second.queue", nil, nil)
	parentDefinition := testAgentDefinitionWithChildren("parent.agent", "parent.workflow", "parent.queue", []tools.ToolSpec{first, second}, nil, []AgentDefinition{childDefinition, cancelDefinition})
	store := &cancellationScheduleStore{testStore: newTestStore()}
	rt := New(store)
	rt.agents["parent.agent"] = AgentRegistration{Definition: parentDefinition, PlanActivityName: "plan", ResumeActivityName: "resume", ExecuteToolActivity: "execute",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: first.Name, Payload: rawjson.Message(`{}`)}, {Name: second.Name, Payload: rawjson.Message(`{}`)}}}, nil
		}},
	}
	rt.agents["first.agent"] = AgentRegistration{Definition: childDefinition, PlanActivityName: "plan", ResumeActivityName: "resume", ExecuteToolActivity: "execute",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: pendingSpec.Name, Payload: rawjson.Message(`{}`)}}}, nil
		}},
	}
	rt.agents["second.agent"] = AgentRegistration{Definition: cancelDefinition, PlanActivityName: "plan", ResumeActivityName: "resume", ExecuteToolActivity: "execute",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) { return nil, context.Canceled }},
	}
	for _, entry := range []struct {
		name       string
		definition AgentDefinition
		spec       tools.ToolSpec
	}{{"first.agent", childDefinition, first}, {"second.agent", cancelDefinition, second}} {
		registration := NewAgentToolsetRegistration(AgentToolConfig{Definition: entry.definition, Name: entry.name, AgentToolContent: AgentToolContent{Prompt: func(tools.Ident, any) string { return "work" }}})
		rt.toolsets[registration.Name] = registration
		seedTestToolset(rt, registration.Name, entry.spec)
	}
	creates, cancels := 0, 0
	rt.toolsets["remote.tools"] = ToolsetRegistration{DecodeInExecutor: true, Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
		if operation := call.ExecutionContinuation; operation != nil {
			id, cancel := operation.AsTaskCancel()
			assert.True(t, cancel)
			assert.Equal(t, "child-task", id)
			cancels++
			pending, err := tooloperation.NewPendingTaskWait(id, nil)
			if err != nil {
				return nil, err
			}
			return Unfinished(pending)
		}
		creates++
		pending, err := tooloperation.NewPendingTaskInput("child-task", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
			"input": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
		}})
		if err != nil {
			return nil, err
		}
		return Unfinished(pending)
	}}
	seedTestToolset(rt, "remote.tools", pendingSpec)
	seedTestToolDefinitions(rt, first, second, pendingSpec)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"parent.agent": {first, second}, "first.agent": {pendingSpec}}
	wf := &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt}
	input := &RunInput{AgentID: "parent.agent", RunID: "parent-run", SessionID: "child-session"}
	publishTestRunInput(t, rt, input, nil)
	out, workflowErr := rt.ExecuteWorkflow(wf, input)
	assert.Nil(t, out)
	require.ErrorIs(t, workflowErr, context.Canceled)
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, cancels, "suspended child's accepted Task must receive cancellation")
	assert.Len(t, wf.childRequests, 3, "two initial children and one saved cancellation continuation")
	page, err := store.ListRunRecords(t.Context(), input.RunID, "", 100)
	require.NoError(t, err)
	assert.Equal(t, 2, countRunEventsByType(page, hooks.ToolCallScheduled))
	assert.Zero(t, countRunEventsByType(page, hooks.ToolResultReceived))
	assert.Zero(t, countRunEventsByType(page, hooks.AwaitMCPInput))
	assert.Equal(t, 1, countRunEventsByType(page, hooks.RunCompleted))
	meta, err := store.LoadRun(t.Context(), input.RunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, meta.Status)
	t.Logf("workflow error: %v; creates=%d cancels=%d children=%d status=%s", workflowErr, creates, cancels, len(wf.childRequests), meta.Status)
}
