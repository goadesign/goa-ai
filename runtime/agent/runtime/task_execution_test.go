// These tests exercise Task observation through the ordinary workflow activity
// route, including host answers across replacement runtimes and exact timer
// guidance. A completed Task contributes one final result and one tool budget.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	// taskTimerWorkflow records timer segments without spending wall-clock time.
	taskTimerWorkflow struct {
		engine.WorkflowContext
		durations []time.Duration
	}

	// taskActivityFailureWorkflow rejects one read before the activity starts.
	taskActivityFailureWorkflow struct {
		engine.WorkflowContext
	}

	// taskCancellationWorkflow stops the run only after its Task poll is saved.
	taskCancellationWorkflow struct {
		engine.WorkflowContext
		cancel context.CancelFunc
	}
)

func TestTaskInputSurvivesSuccessorRuns(t *testing.T) {
	spec := newAnyJSONSpec("remote.tools.lookup")
	definition := testAgentDefinition("test.agent", "test.workflow", "test.queue", []tools.ToolSpec{spec}, nil)
	registration := AgentRegistration{Definition: definition, ExecuteToolActivity: "execute", ResumeActivityName: "resume"}
	rt := New(newTestStore())
	var seen []uint64
	zero := int64(0)
	questions := func(keys ...string) *mcp.InputRequired {
		input := &mcp.InputRequired{Requests: make(map[string]mcp.InputRequest, len(keys))}
		for _, key := range keys {
			input.Requests[key] = mcp.InputRequest{Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}
		}
		return input
	}
	install := func(rt *Runtime) {
		rt.agents["test.agent"] = registration
		seedTestToolset(rt, "remote.tools", spec)
		binding := rt.toolsets["remote.tools"]
		binding.Execute = func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			assert.Equal(t, uint64(len(seen)), call.ExecutionSequence)
			seen = append(seen, call.ExecutionSequence)
			assert.JSONEq(t, `{"query":"original"}`, string(call.Payload))
			switch call.ExecutionSequence {
			case 0:
				assert.Nil(t, call.ExecutionContinuation)
				pending, err := tooloperation.NewPendingTaskWait("", &zero)
				require.NoError(t, err)
				return Unfinished(pending)
			case 2, 5:
				id, answers, ok := call.ExecutionContinuation.AsTaskUpdate()
				require.True(t, ok)
				assert.Empty(t, id)
				key := ""
				if call.ExecutionSequence == 5 {
					key = "next"
				}
				assert.Len(t, answers, 1)
				assert.JSONEq(t, `{"action":"accept","content":{}}`, string(answers[key]))
				pending, err := tooloperation.NewPendingTaskWait(id, nil)
				require.NoError(t, err)
				return Unfinished(pending)
			default:
				id, ok := call.ExecutionContinuation.AsTaskGet()
				require.True(t, ok)
				assert.Empty(t, id)
				if call.ExecutionSequence == 7 {
					return Executed(&planner.ToolResult{Name: call.Name, Result: struct {
						Answer int `json:"answer"`
					}{Answer: 42}}), nil
				}
				keys := []string{""}
				if call.ExecutionSequence >= 4 {
					keys = append(keys, "next")
				}
				pending, err := tooloperation.NewPendingTaskInput(id, &zero, questions(keys...))
				require.NoError(t, err)
				return Unfinished(pending)
			}
		}
		rt.toolsets["remote.tools"] = binding
	}
	install(rt)
	input := &RunInput{AgentID: "test.agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	wf := &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt}
	out, err := rt.runLoop(wf, registration, input, &workflowConversation{RunContext: run.Context{RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1}}, &PlanResult{ToolCalls: []ToolCall{{Name: spec.Name, ToolCallID: "original-call", Payload: rawjson.Message(`{"query":"original"}`)}}}, initialCaps(RunPolicy{MaxToolCalls: 1}), time.Time{}, time.Time{}, input.TurnID, nil)
	require.NoError(t, err)
	require.NotNil(t, out.Suspension)
	for successor := 2; successor <= 3; successor++ {
		key := ""
		if successor == 3 {
			key = "next"
		}
		require.Len(t, out.Suspension.Pending, 1)
		assert.Len(t, out.Suspension.Pending[0].MCP.Requests, 1)
		assert.Contains(t, out.Suspension.Pending[0].MCP.Requests, key)
		rt = New(rt.Store)
		install(rt)
		input = &RunInput{AgentID: input.AgentID, RunID: fmt.Sprintf("run-%d", successor), SessionID: input.SessionID, TurnID: fmt.Sprintf("turn-%d", successor), Continuation: &api.RunContinuationInput{
			Suspension: out.Suspension, Response: &api.PendingInputResponse{MCP: &api.MCPInputResponse{
				ToolCallID: "original-call", Responses: map[string]json.RawMessage{key: json.RawMessage(`{"action":"accept","content":{}}`)},
			}},
		}}
		checkpoint, err := prepareContinuation(input, definition)
		require.NoError(t, err)
		require.NoError(t, restoreContinuationRunInput(input, checkpoint))
		seedRunMeta(t, rt, input)
		wf = &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt, plannerOutput: &PlanActivityOutput{PublicationBatchID: testPublicationBatchID, Result: &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}}}}
		out, err = rt.resumeSuspendedWorkflow(wf, registration, input, checkpoint, seedTestContinuationHistory(t, rt, input, checkpoint))
		require.NoError(t, err)
	}
	assert.Nil(t, out.Suspension)
	assert.Equal(t, "done", out.Final.Text())
	assert.Equal(t, []uint64{0, 1, 2, 3, 4, 5, 6, 7}, seen)
	require.Len(t, wf.lastPlannerCall.Input.ToolOutputs, 1)
	assert.Equal(t, "run-1", wf.lastPlannerCall.Input.ToolOutputs[0].CallRunID)
	assert.Equal(t, "run-3", wf.lastPlannerCall.Input.ToolOutputs[0].ResultRunID)
	outputs, err := rt.loadPlannerToolOutputs(t.Context(), wf.lastPlannerCall.Input.ToolOutputs)
	require.NoError(t, err)
	assert.JSONEq(t, `{"answer":42}`, string(outputs[0].Result))
	page, err := rt.ListRunEvents(t.Context(), "run-2", "", 100)
	require.NoError(t, err)
	assert.Zero(t, countRunEventsByType(page, hooks.ToolResultReceived))
	assert.Equal(t, 1, countRunEventsByType(page, hooks.AwaitMCPInput))
}

func TestTaskPollHintKeepsCompleteWait(t *testing.T) {
	segment := int64(math.MaxInt64) / int64(time.Millisecond)
	for _, millis := range []int64{-1, 0, 1, segment, segment + 1, math.MaxInt64} {
		t.Run(fmt.Sprint(millis), func(t *testing.T) {
			wf := &taskTimerWorkflow{WorkflowContext: &testWorkflowContext{ctx: t.Context()}}
			info := &futureInfo{}
			require.NoError(t, startTaskPollTimer(wf, info, millis))
			assert.Equal(t, time.Duration(min(max(millis, 0), segment))*time.Millisecond, wf.durations[0])
			assert.Equal(t, max(millis, 0)-min(max(millis, 0), segment), info.pollRemainingMs)
			if millis > segment {
				exec := &toolBatchExec{}
				require.NoError(t, exec.continueTaskPoll(wf, info))
				assert.Len(t, wf.durations, 2)
				assert.Equal(t, max(millis, 0)-int64(wf.durations[0]/time.Millisecond)-int64(wf.durations[1]/time.Millisecond), info.pollRemainingMs)
			}
		})
	}
}

func (w *taskTimerWorkflow) NewTimer(_ context.Context, duration time.Duration) (engine.Future[time.Time], error) {
	w.durations = append(w.durations, duration)
	ready := make(chan struct{})
	close(ready)
	return &controlledTimeFuture{ready: ready}, nil
}

func TestTaskCheckpointRejectsMisboundOwnership(t *testing.T) {
	zero := int64(0)
	question := &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
		"next": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
	}}
	pending, err := tooloperation.NewPendingTaskInput("", &zero, question)
	require.NoError(t, err)
	call := ToolCall{Name: "remote.lookup", ToolCallID: "task-call", Payload: []byte(`{}`)}
	makeCheckpoint := func() *workflowCheckpoint {
		return &workflowCheckpoint{Batch: checkpointStepBatch{
			Calls: []ToolCall{call}, Records: []checkpointToolRecord{{Call: call, MCPPending: pending, Task: &taskExecution{TaskID: "", PollIntervalMs: &zero, AnsweredRequests: []string{"previous"}}}},
		}, Pending: []checkpointPendingInput{{MCP: pendingMCPInput(call, pending)}}}
	}
	require.NoError(t, validateCheckpointMCPInputs(makeCheckpoint()))
	for _, test := range []struct {
		name   string
		change func(*checkpointToolRecord)
	}{
		{"missing owner", func(record *checkpointToolRecord) { record.Task = nil }},
		{"wrong owner", func(record *checkpointToolRecord) { record.Task.TaskID = "another" }},
		{"different hint presence", func(record *checkpointToolRecord) { record.Task.PollIntervalMs = nil }},
		{"duplicate answered key", func(record *checkpointToolRecord) { record.Task.AnsweredRequests = []string{"previous", "previous"} }},
		{"unordered answered keys", func(record *checkpointToolRecord) { record.Task.AnsweredRequests = []string{"z", "a"} }},
		{"answered question still displayed", func(record *checkpointToolRecord) { record.Task.AnsweredRequests = []string{"next"} }},
		{"completed record retains owner", func(record *checkpointToolRecord) { record.MCPPending = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkpoint := makeCheckpoint()
			test.change(&checkpoint.Batch.Records[0])
			require.Error(t, validateCheckpointMCPInputs(checkpoint))
		})
	}
}

func TestTaskCleanupBeforeActiveRunSettles(t *testing.T) {
	for _, mode := range []string{"deadline", "cancellation", "cancel failure"} {
		t.Run(mode, func(t *testing.T) {
			spec := newAnyJSONSpec("remote.tools.lookup")
			rt := New(newTestStore())
			seedTestToolset(rt, "remote.tools", spec)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			var operations []string
			longHint := int64(time.Minute / time.Millisecond)
			root := &routeWorkflowContext{ctx: ctx, now: time.Now, hookRuntime: rt, toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){}}
			root.toolRoutes["execute"] = func(activityCtx context.Context, input *ToolInput) (*ToolOutput, error) {
				assert.JSONEq(t, `{"query":"original"}`, string(input.Payload))
				if input.ExecutionContinuation == nil {
					operations = append(operations, "create")
					pending, err := tooloperation.NewPendingTaskWait("", &longHint)
					require.NoError(t, err)
					return &ToolOutput{PendingExecution: pending}, nil
				}
				id, cancelling := input.ExecutionContinuation.AsTaskCancel()
				require.True(t, cancelling, "deadline or cancellation must not poll again")
				assert.Empty(t, id)
				assert.EqualValues(t, 1, input.ExecutionSequence)
				require.NoError(t, activityCtx.Err(), "cleanup must outlive the cancelled run context")
				operations = append(operations, "cancel")
				if mode == "cancel failure" {
					return nil, errors.New("cancellation delivery unavailable")
				}
				pending, err := tooloperation.NewPendingTaskWait(id, nil)
				require.NoError(t, err)
				return &ToolOutput{PendingExecution: pending}, nil
			}
			var wf engine.WorkflowContext = root
			var deadline time.Time
			if mode == "cancellation" {
				wf = &taskCancellationWorkflow{WorkflowContext: root, cancel: cancel}
			} else {
				deadline = time.Now().Add(10 * time.Millisecond)
			}
			runCtx := run.Context{RunID: "run-task", SessionID: "session-task", TurnID: "turn-task"}
			results, timedOut, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{}, "test.agent", &runCtx, testToolHistory(t, rt, "test.agent", runCtx, nil), []ToolCall{{Name: spec.Name, ToolCallID: "task-call", Payload: []byte(`{"query":"original"}`)}}, 0, nil, deadline, nil)
			switch mode {
			case "deadline":
				require.NoError(t, err)
				assert.True(t, timedOut)
				require.Len(t, results, 1)
				assert.Equal(t, planner.FailureTimeout, results[0].ToolResult.Failure.Kind)
			case "cancellation":
				require.ErrorIs(t, err, context.Canceled)
			case "cancel failure":
				require.ErrorContains(t, err, "cancellation delivery unavailable")
			}
			assert.Equal(t, []string{"create", "cancel"}, operations)
		})
	}
}

func TestTaskReadFailureCancelsWithANewOperation(t *testing.T) {
	for _, mode := range []string{"schedule", "activity"} {
		t.Run(mode, func(t *testing.T) {
			spec := newAnyJSONSpec("remote.tools.lookup")
			rt := New(newTestStore())
			seedTestToolset(rt, "remote.tools", spec)
			zero := int64(0)
			var cancelled int
			root := &routeWorkflowContext{ctx: t.Context(), hookRuntime: rt, toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){}}
			root.toolRoutes["execute"] = func(_ context.Context, input *ToolInput) (*ToolOutput, error) {
				if operation := input.ExecutionContinuation; operation != nil {
					if _, reading := operation.AsTaskGet(); reading {
						assert.EqualValues(t, 1, input.ExecutionSequence)
						return nil, errors.New("read activity unavailable")
					}
					id, cancelling := operation.AsTaskCancel()
					require.True(t, cancelling)
					assert.Empty(t, id)
					assert.EqualValues(t, 2, input.ExecutionSequence, "cancellation cannot reuse the failed read's identity")
					cancelled++
				}
				pending, err := tooloperation.NewPendingTaskWait("", &zero)
				require.NoError(t, err)
				return &ToolOutput{PendingExecution: pending}, nil
			}
			var wf engine.WorkflowContext = root
			if mode == "schedule" {
				wf = &taskActivityFailureWorkflow{WorkflowContext: root}
			}
			runCtx := run.Context{RunID: "read-failure", SessionID: "session-task", TurnID: "turn-task"}
			results, _, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{}, "test.agent", &runCtx, testToolHistory(t, rt, "test.agent", runCtx, nil), []ToolCall{{Name: spec.Name, ToolCallID: "task-call", Payload: []byte(`{}`)}}, 0, nil, time.Time{}, nil)
			if mode == "schedule" {
				require.ErrorContains(t, err, "read scheduling unavailable")
			} else {
				require.NoError(t, err)
				require.Len(t, results, 1)
				assert.Equal(t, planner.FailureInternal, results[0].ToolResult.Failure.Kind)
			}
			assert.Equal(t, 1, cancelled)
		})
	}
}

func (w *taskActivityFailureWorkflow) ExecuteToolActivityAsync(call engine.ToolActivityCall) (engine.Future[*ToolOutput], error) {
	if operation := call.Input.ExecutionContinuation; operation != nil {
		if _, reading := operation.AsTaskGet(); reading {
			return nil, errors.New("read scheduling unavailable")
		}
	}
	return w.WorkflowContext.ExecuteToolActivityAsync(call)
}

func (w *taskCancellationWorkflow) NewTimer(ctx context.Context, duration time.Duration) (engine.Future[time.Time], error) {
	timer, err := w.WorkflowContext.NewTimer(ctx, duration)
	if duration == time.Minute {
		w.cancel()
	}
	return timer, err
}
