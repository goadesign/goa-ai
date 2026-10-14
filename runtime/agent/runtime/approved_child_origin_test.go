package runtime

// Approved child calls retain their original permission identity when an answer
// or cancellation starts another child workflow, while results keep the run
// that scheduled the call after approval.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestApprovedChildContinuationKeepsOriginalInvocation(t *testing.T) {
	for _, cancelChild := range []bool{false, true} {
		name := "answer"
		if cancelChild {
			name = "session_cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			generated := genpictures.SpecView()
			tool := tools.ToolSpec{Name: "delegate.inspect", IsAgentTool: true, AgentID: admissionChildAgentID,
				Payload: generated.Payload, Result: generated.Result,
				ExecutionPayloadSchema: generated.ExecutionPayloadSchema, ExecutionPayloadCodec: generated.ExecutionPayloadCodec}
			rt := New(newTestStore(), WithEngine(engineinmem.New()), WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{tool.Name: {
				Prompt:       func(context.Context, *ToolCall) (string, error) { return "Inspect item?", nil },
				DeniedResult: func(context.Context, *ToolCall) (any, error) { return &genpictures.ViewResult{ID: "declined"}, nil },
			}}}))
			childDefinition := testAgentDefinition(admissionChildAgentID, "child.workflow", "child.queue", []tools.ToolSpec{tool}, nil)
			parentDefinition := NewAgentDefinition(AgentRoute{ID: parentAgentID, WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"}, []tools.ToolSpec{tool}, nil, nil, []tools.Ident{tool.Name}, []AgentDefinition{childDefinition}, nil)
			var childResumes atomic.Int32
			require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: childDefinition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "child.plan", ResumeActivityName: "child.resume", ExecuteToolActivity: "child.execute",
				Planner: &stubPlanner{
					start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
						return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "question", Question: "Which item?"}))}, nil
					},
					resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
						childResumes.Add(1)
						result, err := genpictures.MarshalViewResult(&genpictures.ViewResult{ID: "A"})
						if err != nil {
							return nil, err
						}
						return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: result}}, nil
					},
				},
			}))
			registration := NewAgentToolsetRegistration(AgentToolConfig{Definition: childDefinition, Name: "delegate"})
			registration.Specs = []tools.ToolSpec{tool}
			require.NoError(t, rt.RegisterToolset(registration))
			payload, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "A"})
			require.NoError(t, err)
			require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: parentDefinition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "parent.plan", ResumeActivityName: "parent.resume", ExecuteToolActivity: "parent.execute",
				Planner: &stubPlanner{
					start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
						return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tool.Name, Payload: payload}}}, nil
					},
					resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
						require.Len(t, in.ToolOutputs, 1)
						return finalPlannerResult("complete"), nil
					},
				},
			}))
			_, err = createSessionForTest(ctx, rt.Store, "session")
			require.NoError(t, err)
			client := rt.MustClient(parentAgentID)
			first, err := client.Run(ctx, "session", nil, WithRunID("original"))
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			originalRecords, err := rt.Store.ListRunRecords(ctx, "original", "", 100)
			require.NoError(t, err)
			prepared, err := client.PrepareContinuation(ctx, "session", "original", "approved", "approval", &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: first.Suspension.Pending[0].Confirmation.ID, Approved: true, RequestedBy: "operator"}}, WorkflowOptions{})
			require.NoError(t, err)
			handle, err := client.StartPrepared(ctx, prepared)
			require.NoError(t, err)
			approved, err := handle.Wait(ctx)
			require.NoError(t, err)
			require.NotNil(t, approved.Suspension)
			checkpoint, err := decodeWorkflowCheckpoint(approved.Suspension, parentDefinition)
			require.NoError(t, err)
			require.Equal(t, "original", checkpoint.Batch.Records[0].Call.RunID)
			require.Equal(t, "approved", checkpoint.Batch.Records[0].CallRunID)
			childCheckpoint, err := decodeWorkflowCheckpointState(checkpoint.Pending[0].Child.Suspension)
			require.NoError(t, err)
			approvedRecords, err := rt.Store.ListRunRecords(ctx, "approved", "", 100)
			require.NoError(t, err)
			if cancelChild {
				_, err = rt.Store.(interface {
					EndSession(context.Context, string, time.Time) (session.Session, error)
				}).EndSession(ctx, "session", time.Now())
				require.NoError(t, err)
			}
			prepared, err = client.PrepareContinuation(ctx, "session", "approved", "continued", "answer", &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "A"}}, WorkflowOptions{})
			require.NoError(t, err)
			handle, err = client.StartPrepared(ctx, prepared)
			require.NoError(t, err)
			completed, err := handle.Wait(ctx)
			if cancelChild {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
				require.Equal(t, "complete", completed.Final.Text())
			}
			originalAfter, err := rt.Store.ListRunRecords(ctx, "original", "", 100)
			require.NoError(t, err)
			require.Equal(t, originalRecords, originalAfter)
			approvedAfter, err := rt.Store.ListRunRecords(ctx, "approved", "", 100)
			require.NoError(t, err)
			require.Equal(t, approvedRecords, approvedAfter)
			oldChild, err := rt.Store.LoadRun(ctx, childCheckpoint.PreviousRunID)
			require.NoError(t, err)
			require.NotEmpty(t, oldChild.SuccessorRunID)
			newChild, err := rt.Store.LoadRun(ctx, oldChild.SuccessorRunID)
			require.NoError(t, err)
			require.Equal(t, "continued", newChild.ParentRunID)
			if cancelChild {
				require.Equal(t, session.RunStatusCanceled, newChild.Status)
				require.Zero(t, childResumes.Load())
			} else {
				require.Equal(t, session.RunStatusCompleted, newChild.Status)
				require.EqualValues(t, 1, childResumes.Load())
				outputs, err := rt.loadPlannerToolOutputs(ctx, []*api.ToolOutputRef{{CallRunID: "approved", ResultRunID: "continued", ToolCallID: checkpoint.Batch.Records[0].Call.ToolCallID}})
				require.NoError(t, err)
				require.Len(t, outputs, 1)
			}
		})
	}
}
