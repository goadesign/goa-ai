package runtime

// workflow_continuation_queue_test.go proves that an ordered input barrier
// consumes one answer per workflow and resumes planning only after the complete
// barrier has been satisfied.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestContinuationConsumesOneOrderedPendingInputPerWorkflow(t *testing.T) {
	runtime := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	registration := AgentRegistration{ResumeActivityName: "resume", ExecuteToolActivity: "execute"}
	firstInput := &RunInput{
		AgentID: "agent-1", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1",
	}
	seedRunMeta(t, runtime, firstInput)
	firstContext := &testWorkflowContext{ctx: t.Context(), runtime: runtime}
	first, err := runtime.runLoop(
		firstContext,
		registration,
		firstInput,
		&planner.PlanInput{RunContext: run.Context{
			RunID: "run-1", SessionID: "session-1", TurnID: "turn-1", Attempt: 1,
		}},
		&PlanResult{Await: planner.NewAwait(
			planner.AwaitClarificationItem(&planner.AwaitClarification{
				ID: "facility", Question: "Which facility?",
			}),
			planner.AwaitClarificationItem(&planner.AwaitClarification{
				ID: "unit", Question: "Which unit?",
			}),
		)},
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.Len(t, first.Suspension.Pending, 2)

	firstCheckpoint, err := runtime.decodeWorkflowCheckpoint(first.Suspension)
	require.NoError(t, err)
	secondInput := &RunInput{
		AgentID: "agent-1", RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: first.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID: "facility", Answer: "Building A",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(secondInput, firstCheckpoint))
	seedRunMeta(t, runtime, secondInput)
	second, err := runtime.resumeSuspendedWorkflow(
		&testWorkflowContext{ctx: t.Context(), runtime: runtime},
		registration,
		secondInput,
		firstCheckpoint,
	)
	require.NoError(t, err)
	require.Len(t, second.Suspension.Pending, 1)
	require.Equal(t, "unit", second.Suspension.Pending[0].Await.Clarification.ID)
	secondEvents, err := runtime.ListRunEvents(t.Context(), "run-2", "", 100)
	require.NoError(t, err)
	require.Equal(t, 1, countRunEventsByType(secondEvents, hooks.AwaitClarification))

	secondCheckpoint, err := runtime.decodeWorkflowCheckpoint(second.Suspension)
	require.NoError(t, err)
	thirdInput := &RunInput{
		AgentID: "agent-1", RunID: "run-3", SessionID: "session-1", TurnID: "turn-3",
		Continuation: &api.RunContinuationInput{
			Suspension: second.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID: "unit", Answer: "Unit 7",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(thirdInput, secondCheckpoint))
	seedRunMeta(t, runtime, thirdInput)
	thirdContext := &testWorkflowContext{
		ctx: t.Context(), hasPlanResult: true,
		planResult: &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		}}},
	}
	third, err := runtime.resumeSuspendedWorkflow(
		thirdContext,
		registration,
		thirdInput,
		secondCheckpoint,
	)
	require.NoError(t, err)
	require.Nil(t, third.Suspension)
	require.Equal(t, "done", agentMessageText(third.Final))
	require.Len(t, thirdContext.lastPlannerCall.Input.Messages, 2)
}
