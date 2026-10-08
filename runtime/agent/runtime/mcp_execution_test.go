// This file verifies that a remote input request survives worker replacement
// without publishing a final result or charging another model tool call.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"

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

func TestMCPInputSurvivesSuccessorRuns(t *testing.T) {
	for _, mode := range []string{"form", "state", "identical_state", "clarification", "empty_id"} {
		t.Run(mode, func(t *testing.T) {
			stateOnly := mode == "state" || mode == "identical_state"
			requestID := "same-request-id"
			if mode == "empty_id" {
				requestID = ""
			}
			spec := newAnyJSONSpec("remote.tools.lookup")
			definition := testAgentDefinition("test.agent", "test.workflow", "test.queue", []tools.ToolSpec{spec}, nil)
			registration := AgentRegistration{Definition: definition, ExecuteToolActivity: "execute", ResumeActivityName: "resume"}
			rt := New(newTestStore())
			round := uint64(0)
			install := func(rt *Runtime) {
				rt.agents["test.agent"] = registration
				seedTestToolset(rt, "remote.tools", spec)
				binding := rt.toolsets["remote.tools"]
				binding.Execute = func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					round++
					assert.Equal(t, round-1, call.ExecutionSequence)
					assert.JSONEq(t, `{"query":"original"}`, string(call.Payload))
					if round > 1 {
						require.NotNil(t, call.ExecutionContinuation)
						continuation, ok := call.ExecutionContinuation.AsInput()
						require.True(t, ok)
						expectedState := fmt.Sprintf("opaque state %d", round-1)
						if mode == "identical_state" {
							expectedState = ""
						}
						assert.Equal(t, expectedState, *continuation.RequestState)
						if !stateOnly {
							assert.JSONEq(t, `{"action":"accept","content":{"choice":"yes"}}`, string(continuation.InputResponses[requestID]))
						}
					}
					if round == 3 {
						result := Executed(&planner.ToolResult{Name: call.Name, Result: struct {
							Answer int `json:"answer"`
						}{Answer: 42}})
						if mode == "clarification" {
							result.Clarification = &ToolClarification{ID: "after-result", Question: "Show the answer?"}
						}
						return result, nil
					}
					state := fmt.Sprintf("opaque state %d", round)
					if mode == "identical_state" {
						state = ""
					}
					input := &mcp.InputRequired{RequestState: &state}
					if !stateOnly {
						input.Requests = map[string]mcp.InputRequest{requestID: {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{"choice":{"type":"string"}},"required":["choice"]}}`)}}
					}
					return AwaitMCPInput(input)
				}
				rt.toolsets["remote.tools"] = binding
			}
			install(rt)
			firstInput := &RunInput{AgentID: "test.agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
			seedRunMeta(t, rt, firstInput)
			wfCtx := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, runtime: rt}
			first, err := rt.runLoop(wfCtx, registration, firstInput, &workflowConversation{RunContext: run.Context{RunID: "run-1", SessionID: "session-1", TurnID: "turn-1", Attempt: 1}}, &PlanResult{ToolCalls: []ToolCall{{Name: spec.Name, ToolCallID: "original-call", Payload: rawjson.Message(`{"query":"original"}`)}}}, initialCaps(RunPolicy{MaxToolCalls: 1}), time.Time{}, time.Time{}, "turn-1", nil)
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			events, err := rt.ListRunEvents(t.Context(), "run-1", "", 100)
			require.NoError(t, err)
			assert.Equal(t, 0, countRunEventsByType(events, hooks.ToolResultReceived))
			assert.Equal(t, 1, countRunEventsByType(events, hooks.AwaitMCPInput))
			suspension := first.Suspension
			for successor := 2; successor <= 3; successor++ {
				rt = New(rt.Store)
				install(rt)
				checkpoint, err := decodeWorkflowCheckpoint(suspension, definition)
				require.NoError(t, err)
				answers := map[string]json.RawMessage(nil)
				if !stateOnly {
					answers = map[string]json.RawMessage{requestID: json.RawMessage(`{"action":"accept","content":{"choice":"yes"}}`)}
				}
				input := &RunInput{AgentID: "test.agent", RunID: fmt.Sprintf("run-%d", successor), SessionID: "session-1", TurnID: fmt.Sprintf("turn-%d", successor), Continuation: &api.RunContinuationInput{Suspension: suspension, Response: &api.PendingInputResponse{MCP: &api.MCPInputResponse{ToolCallID: suspension.Pending[0].MCP.ToolCallID, Responses: answers}}}}
				require.NoError(t, restoreContinuationRunInput(input, checkpoint))
				seedRunMeta(t, rt, input)
				wfCtx = &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, runtime: rt, plannerOutput: &PlanActivityOutput{PublicationBatchID: testPublicationBatchID, Result: &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}}}}
				endID := seedTestContinuationHistory(t, rt, input, checkpoint)
				out, err := rt.resumeSuspendedWorkflow(wfCtx, registration, input, checkpoint, endID)
				require.NoError(t, err)
				require.NotNil(t, out)
				if successor == 2 {
					require.EqualValues(t, successor, round)
					require.NotNil(t, out.Suspension)
					suspension = out.Suspension
				} else {
					if mode == "clarification" {
						require.NotNil(t, out.Suspension)
						require.Len(t, out.Suspension.Pending, 1)
						assert.Equal(t, "after-result", out.Suspension.Pending[0].Await.Clarification.ID)
						next := &RunInput{AgentID: input.AgentID, RunID: "run-4", SessionID: input.SessionID, TurnID: "turn-4", Continuation: &api.RunContinuationInput{Suspension: out.Suspension, Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "after-result", Answer: "yes"}}}}
						rt = New(rt.Store)
						install(rt)
						checkpoint, err := prepareContinuation(next, definition)
						require.NoError(t, err)
						require.NoError(t, restoreContinuationRunInput(next, checkpoint))
						seedRunMeta(t, rt, next)
						wfCtx = &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, runtime: rt, plannerOutput: wfCtx.plannerOutput}
						out, err = rt.resumeSuspendedWorkflow(wfCtx, registration, next, checkpoint, seedTestContinuationHistory(t, rt, next, checkpoint))
						require.NoError(t, err)
					}
					assert.Nil(t, out.Suspension)
					assert.Equal(t, "done", out.Final.Text())
					require.Len(t, wfCtx.lastPlannerCall.Input.ToolOutputs, 1)
					assert.Equal(t, "run-1", wfCtx.lastPlannerCall.Input.ToolOutputs[0].CallRunID)
					assert.Equal(t, "run-3", wfCtx.lastPlannerCall.Input.ToolOutputs[0].ResultRunID)
					outputs, err := rt.loadPlannerToolOutputs(t.Context(), wfCtx.lastPlannerCall.Input.ToolOutputs)
					require.NoError(t, err)
					require.Len(t, outputs, 1)
					assert.Nil(t, outputs[0].Failure)
					assert.JSONEq(t, `{"answer":42}`, string(outputs[0].Result))
				}
			}
			assert.Equal(t, uint64(3), round)
		})
	}
}

func TestMCPBindingRetryPolicyIsOwned(t *testing.T) {
	rt := New(newTestStore())
	policy := &engine.RetryPolicy{MaxAttempts: 1}
	reg := ToolsetRegistration{Name: "remote", Specs: []tools.ToolSpec{newAnyJSONSpec("remote.lookup")}, ActivityRetryPolicy: policy, Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) { return nil, nil }}
	require.NoError(t, rt.RegisterToolset(reg))
	policy.MaxAttempts = 10
	assert.Equal(t, 1, rt.toolsets["remote"].ActivityRetryPolicy.MaxAttempts)
}

// A remote binding owns one attempt even if the agent's normal tools allow retries.
func TestMCPActivityRetryDoesNotChangeLocalTools(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote=%t", remote), func(t *testing.T) {
			spec := newAnyJSONSpec("lookup")
			rt := New(newTestStore())
			seedTestToolset(rt, "lookup", spec)
			binding := rt.toolsets["lookup"]
			if remote {
				binding.ActivityRetryPolicy = &engine.RetryPolicy{MaxAttempts: 1}
			}
			rt.toolsets["lookup"] = binding
			ctx := run.Context{RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
			wf := &testWorkflowContext{ctx: t.Context(), asyncResult: ToolOutput{Payload: []byte(`{}`)}}
			_, _, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{MaxAttempts: 5}}, "test.agent", &ctx, testToolHistory(t, rt, "test.agent", ctx, nil), []ToolCall{{Name: spec.Name, ToolCallID: "call-1", Payload: []byte(`{}`)}}, 0, nil, time.Time{}, nil)
			require.NoError(t, err)
			want := 5
			if remote {
				want = 1
			}
			assert.Equal(t, want, wf.lastToolCall.Options.RetryPolicy.MaxAttempts)
		})
	}
}

// Parallel remote calls may use the same server input ID. The host response and
// opaque server state remain bound to the runtime's original tool-call ID.
func TestMCPCheckpointCorrelatesParallelCalls(t *testing.T) {
	requests := map[string]mcp.InputRequest{"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}}`)}}
	firstState, secondState := "first-state", "second-state"
	first := ToolCall{Name: "remote.lookup", ToolCallID: "call-1", Payload: []byte(`{"key":"first"}`)}
	second := ToolCall{Name: first.Name, ToolCallID: "call-2", Payload: []byte(`{"key":"second"}`)}
	inputs := []*mcp.InputRequired{{RequestState: &firstState, Requests: requests}, {RequestState: &secondState, Requests: requests}}
	checkpoint := &workflowCheckpoint{Batch: checkpointStepBatch{Calls: []ToolCall{first, second}, Records: []checkpointToolRecord{{Call: first, MCPPending: mustPendingInput(t, inputs[0])}, {Call: second, MCPPending: mustPendingInput(t, inputs[1])}}}, Pending: []checkpointPendingInput{{MCP: pendingMCPInput(first, mustPendingInput(t, inputs[0]))}, {MCP: pendingMCPInput(second, mustPendingInput(t, inputs[1]))}}}
	require.NoError(t, validateCheckpointMCPInputs(checkpoint))
	for _, mutate := range []func(*workflowCheckpoint){
		func(c *workflowCheckpoint) { c.Pending[0].MCP.ToolCallID = "call-2" },
		func(c *workflowCheckpoint) { c.Batch.Records[0].Call.Payload = []byte(`{"key":"changed"}`) },
		func(c *workflowCheckpoint) { c.Pending = c.Pending[:1] },
	} {
		raw, err := json.Marshal(checkpoint)
		require.NoError(t, err)
		var changed workflowCheckpoint
		require.NoError(t, json.Unmarshal(raw, &changed))
		mutate(&changed)
		require.Error(t, validateCheckpointMCPInputs(&changed))
	}
}

// TestTextOnlyMCPInputBoundaries proves external and custom executor input cannot
// create a suspension in a restricted run. Ordinary runs keep all three modes.
func TestTextOnlyMCPInputBoundaries(t *testing.T) {
	state := "saved"
	for _, test := range []struct {
		name  string
		input *mcp.InputRequired
	}{
		{"state", &mcp.InputRequired{RequestState: &state}},
		{"form", &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"form": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{"value":{"type":"string"}}}}`)}}}},
		{"url", &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"url": {Method: "elicitation/create", Params: json.RawMessage(`{"mode":"url","message":"Continue outside the client","url":"https://example.test/consent"}`)}}}},
	} {
		input := test.input
		for _, restricted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restricted=%t", test.name, restricted), func(t *testing.T) {
				rt := New(newTestStore())
				spec := genrecords.SpecRead()
				calls := 0
				require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec}, Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
					calls++
					return AwaitMCPInput(input)
				}}))
				output, err := rt.ExecuteToolActivity(t.Context(), &ToolInput{ToolName: genrecords.Read, ToolsetName: "records", RunID: "run", ToolCallID: "read", Payload: []byte(`{"query":"active"}`), TextOnly: restricted})
				assert.Equal(t, 1, calls)
				if restricted {
					assert.Nil(t, output)
					require.ErrorContains(t, err, "text-only tools cannot request MCP host input")
					assert.True(t, engine.IsActivityErrorNonRetryable(err))
				} else {
					require.NoError(t, err)
					assert.Equal(t, input, pendingHostInput(output.PendingExecution))
				}
				call := ToolCall{Name: genrecords.Read, ToolCallID: "read", TextOnly: restricted}
				execution := &toolBatchExec{r: rt}
				outcome, err := execution.executionFromActivityOutput(t.Context(), futureInfo{call: call}, &ToolOutput{PendingExecution: mustPendingInput(t, input)}, 0)
				if restricted {
					assert.Nil(t, outcome)
					require.ErrorContains(t, err, "text-only tools cannot request MCP host input")
				} else {
					require.NoError(t, err)
					assert.Equal(t, input, pendingHostInput(outcome.mcpPending))
					assert.Equal(t, "read", outcome.mcpToolCallID)
				}
			})
		}
	}
}

func TestTextOnlyExecutionContinuationCannotDispatch(t *testing.T) {
	rt := New(newTestStore())
	spec := genrecords.SpecRead()
	calls := 0
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec}, Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
		calls++
		return Executed(&planner.ToolResult{Name: genrecords.Read, Result: &genrecords.ReadResult{Count: 1}}), nil
	}}))
	continuation, err := tooloperation.NewInput(&mcp.CallContinuation{})
	require.NoError(t, err)
	output, err := rt.ExecuteToolActivity(t.Context(), &ToolInput{ToolName: genrecords.Read, ToolsetName: "records", Payload: []byte(`{"query":"active"}`), TextOnly: true, ExecutionSequence: 1, ExecutionContinuation: continuation})
	assert.Nil(t, output)
	require.ErrorContains(t, err, "text-only calls cannot carry host input")
	assert.True(t, engine.IsActivityErrorNonRetryable(err))
	assert.Zero(t, calls)
}

func TestTextOnlyCheckpointCannotRetainMCPInput(t *testing.T) {
	state := "saved"
	input := &mcp.InputRequired{RequestState: &state}
	for _, restriction := range []string{"call", "context", "policy"} {
		t.Run(restriction, func(t *testing.T) {
			call := ToolCall{Name: "remote.lookup", ToolCallID: "call", Payload: []byte(`{}`)}
			checkpoint := &workflowCheckpoint{}
			switch restriction {
			case "call":
				call.TextOnly = true
			case "context":
				checkpoint.Context.TextOnly = true
			case "policy":
				checkpoint.Policy = &PolicyOverrides{TextOnly: true}
			}
			checkpoint.Batch = checkpointStepBatch{Calls: []ToolCall{call}, Records: []checkpointToolRecord{{Call: call, MCPPending: mustPendingInput(t, input)}}}
			checkpoint.Pending = []checkpointPendingInput{{MCP: pendingMCPInput(call, mustPendingInput(t, input))}}
			require.ErrorContains(t, validateCheckpointMCPInputs(checkpoint), "text-only checkpoint cannot contain MCP host input")
			checkpoint.Batch.Records[0].MCPPending = nil
			checkpoint.Pending = nil
			require.NoError(t, validateCheckpointMCPInputs(checkpoint))
		})
	}
}

// mustPendingInput constructs a validated saved value for runtime boundary tests.
func mustPendingInput(t *testing.T, input *mcp.InputRequired) *api.PendingExecution {
	t.Helper()
	pending, err := tooloperation.NewPendingInput(input)
	require.NoError(t, err)
	return pending
}
