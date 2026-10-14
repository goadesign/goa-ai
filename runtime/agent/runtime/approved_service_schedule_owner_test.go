package runtime

// These tests follow scheduled service calls through approval and later input.
// The provider keeps the original invocation while history names the run that
// actually scheduled the call, including when a deadline stops a continuation.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/tooloperation"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

func TestApprovedServiceRetainsScheduleOwner(t *testing.T) {
	for _, mode := range []string{"same_run", "approved", "approved_then_input", "approved_then_expired_input"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			spec := newAnyJSONSpec("records.change")
			inputAgain := mode == "approved_then_input" || mode == "approved_then_expired_input"
			options := []RuntimeOption{WithEngine(engineinmem.New())}
			if mode != "same_run" {
				options = append(options, WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{spec.Name: {
					Prompt:       func(context.Context, *ToolCall) (string, error) { return "Apply change?", nil },
					DeniedResult: func(context.Context, *ToolCall) (any, error) { return map[string]any{"changed": false}, nil },
				}}}))
			}
			rt := New(newTestStore(), options...)
			invocations := make(chan string, 3)
			executions := 0
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec},
				Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					executions++
					invocations <- call.RunID
					if inputAgain && executions == 1 {
						state := "saved-input"
						return AwaitMCPInput(&mcp.InputRequired{RequestState: &state})
					}
					return Executed(&planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"changed": true}}), nil
				},
			}))
			definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, []tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, nil)
			resumed := make(chan *planner.PlanResumeInput, 1)
			require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute",
				Planner: &stubPlanner{
					start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
						return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: spec.Name, Payload: rawjson.Message(`{}`)}}}, nil
					},
					resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
						resumed <- in
						return finalPlannerResult("complete"), nil
					},
				},
			}))
			_, err := createSessionForTest(ctx, rt.Store, "session")
			require.NoError(t, err)
			out, err := rt.MustClient("records.agent").Run(ctx, "session", []*model.Message{userMsg("Apply change")}, WithRunID("original"))
			require.NoError(t, err)
			scheduleRun, resultRun := "original", "original"
			if mode != "same_run" {
				require.NotNil(t, out.Suspension)
				before, err := rt.Store.ListRunRecords(ctx, "original", "", 100)
				require.NoError(t, err)
				prepared, err := rt.MustClient("records.agent").PrepareContinuation(ctx, "session", "original", "approved", "approval-turn", &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: out.Suspension.Pending[0].Confirmation.ID, Approved: true, RequestedBy: "operator"}}, WorkflowOptions{})
				require.NoError(t, err)
				handle, err := rt.MustClient("records.agent").StartPrepared(ctx, prepared)
				require.NoError(t, err)
				out, err = handle.Wait(ctx)
				require.NoError(t, err)
				after, err := rt.Store.ListRunRecords(ctx, "original", "", 100)
				require.NoError(t, err)
				require.Equal(t, before, after)
				scheduleRun, resultRun = "approved", "approved"
			}
			require.Equal(t, "original", <-invocations)
			if inputAgain {
				require.NotNil(t, out.Suspension)
				checkpoint, err := decodeWorkflowCheckpoint(out.Suspension, definition)
				require.NoError(t, err)
				require.Equal(t, "approved", checkpoint.Batch.Records[0].CallRunID)
				response := &api.PendingInputResponse{MCP: &api.MCPInputResponse{ToolCallID: out.Suspension.Pending[0].MCP.ToolCallID}}
				if mode == "approved_then_expired_input" {
					input := &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("records.agent"))}, RunID: "continued", SessionID: "session", TurnID: "input-turn", Continuation: &api.RunContinuationInput{Suspension: out.Suspension, Response: response}}
					require.NoError(t, restoreContinuationRunInput(input, checkpoint))
					prepared, err := rt.MustClient("records.agent").PrepareContinuation(ctx, "session", "approved", "continued", "input-turn", response, WorkflowOptions{})
					require.NoError(t, err)
					input.SeedEndID = prepared.reference.HistoryEndID
					// The real workflow already stored its suspension. Accept the
					// prepared successor without writing that terminal event again.
					admitContinuedRunForTest(t, rt.Store, session.RunMeta{
						Subject: input.Subject, RunID: input.RunID, SessionID: input.SessionID,
						SeedEndID: input.SeedEndID, Status: session.RunStatusRunning,
					}, "approved")
					now := time.Now()
					wf := &testWorkflowContext{ctx: ctx, runtime: rt, hookRuntime: rt, now: func() time.Time { return now }}
					owner := &workflowLoop{unfinishedBatch: &stepBatch{records: []stepToolRecord{{call: checkpoint.Batch.Records[0].Call, callRunID: checkpoint.Batch.Records[0].CallRunID}}}}
					call := checkpoint.Batch.Records[0].Call
					call.ExecutionContinuation, err = tooloperation.NewInput(&mcp.CallContinuation{RequestState: pendingHostInput(checkpoint.Batch.Records[0].MCPPending).RequestState})
					require.NoError(t, err)
					runContext := run.Context{RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID}
					results, timedOut, err := rt.executeToolCalls(owner, wf, "records.execute", engine.ActivityOptions{}, definition.route.ID, &runContext, input.SeedEndID, []ToolCall{call}, 0, nil, now.Add(-time.Second), nil)
					require.NoError(t, err)
					require.True(t, timedOut)
					require.Len(t, results, 1)
					records, err := stepToolRecordsAfterExecution([]ToolCall{call}, results, nil)
					require.NoError(t, err)
					owner.finishToolBatches(records)
					require.Equal(t, "approved", records[0].callRunID)
				} else {
					prepared, err := rt.MustClient("records.agent").PrepareContinuation(ctx, "session", "approved", "continued", "input-turn", response, WorkflowOptions{})
					require.NoError(t, err)
					handle, err := rt.MustClient("records.agent").StartPrepared(ctx, prepared)
					require.NoError(t, err)
					out, err = handle.Wait(ctx)
					require.NoError(t, err)
					require.Equal(t, "original", <-invocations)
				}
				resultRun = "continued"
			}
			page, err := rt.Store.ListRunRecords(ctx, resultRun, "", 100)
			require.NoError(t, err)
			var result *hooks.ToolResultReceivedEvent
			schedules := 0
			for _, event := range page.Events {
				if event.Type == hooks.ToolCallScheduled {
					schedules++
				}
				if event.Type == hooks.ToolResultReceived {
					result, err = decodeToolResultRunlogEvent(event)
					require.NoError(t, err)
				}
			}
			require.NotNil(t, result)
			require.Equal(t, scheduleRun, result.CallRunID)
			if resultRun != scheduleRun {
				require.Zero(t, schedules)
			} else {
				require.Equal(t, 1, schedules)
			}
			outputs, err := rt.loadPlannerToolOutputs(ctx, []*api.ToolOutputRef{{CallRunID: scheduleRun, ResultRunID: resultRun, ToolCallID: result.ToolCallID}})
			require.NoError(t, err)
			require.Len(t, outputs, 1)
			if mode == "approved_then_expired_input" {
				require.NotNil(t, outputs[0].Failure)
			} else {
				require.Equal(t, "complete", out.Final.Text())
				require.Len(t, (<-resumed).ToolOutputs, 1)
			}
		})
	}
}
