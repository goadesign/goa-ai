package runtime

// workflow_tool_clarification_test.go verifies that free-text answers complete
// the exact model-authored tool call that initiated the external-input request.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestRunLoopToolClarificationPreservesCallAndReturnsAnswer(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	tool := newAnyJSONSpec(tools.Ident("chat.ask_clarification"), "chat")
	seedTestToolSpecs(rt, tool)

	wfCtx := &testWorkflowContext{ctx: t.Context()}

	base := &planner.PlanInput{RunContext: run.Context{
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Attempt:   1,
	}}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	initial := &planner.PlanResult{Await: planner.NewAwait(
		planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
			ID:         "clarification-await-1",
			ToolName:   tool.Name,
			ToolCallID: "clarification-call-1",
			Payload:    rawjson.Message(`{"question":"Which equipment and time window?"}`),
			Question:   "Which equipment and time window?",
		}),
	)}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
	require.Empty(t, wfCtx.lastPlannerCall.Name)
	require.Len(t, out.Suspension.Pending, 1)
	require.Equal(t, api.PendingInputKindClarification, out.Suspension.Pending[0].Kind)

	checkpoint, err := rt.decodeWorkflowCheckpoint(out.Suspension)
	require.NoError(t, err)
	continuedCtx := &testWorkflowContext{
		ctx:           t.Context(),
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}
	continuedInput := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-2",
		SessionID: "session-1",
		TurnID:    "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: out.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID:     "clarification-await-1",
				Answer: "Use compressor_1 over the past 24 hours.",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(continuedInput, checkpoint))
	out, err = rt.resumeSuspendedWorkflow(
		continuedCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		continuedInput,
		checkpoint,
	)
	require.NoError(t, err)
	require.Equal(t, "done", agentMessageText(out.Final))
	require.Equal(t, "resume", continuedCtx.lastPlannerCall.Name)
	require.NoError(t, transcript.ValidatePlannerTranscript(continuedCtx.lastPlannerCall.Input.Messages))
	require.Len(t, continuedCtx.lastPlannerCall.Input.Messages, 2)
	require.Len(t, continuedCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "run-1", continuedCtx.lastPlannerCall.Input.ToolOutputs[0].CallRunID)
	require.Equal(t, "run-2", continuedCtx.lastPlannerCall.Input.ToolOutputs[0].ResultRunID)

	assistant := continuedCtx.lastPlannerCall.Input.Messages[0]
	require.Equal(t, model.ConversationRoleAssistant, assistant.Role)
	toolUse, ok := assistant.Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "clarification-call-1", toolUse.ID)

	user := continuedCtx.lastPlannerCall.Input.Messages[1]
	require.Equal(t, model.ConversationRoleUser, user.Role)
	toolResult, ok := user.Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, toolUse.ID, toolResult.ToolUseID)
	require.Equal(t, map[string]any{
		"answer": "Use compressor_1 over the past 24 hours.",
	}, toolResult.Content)
}

func TestRunLoopSessionlessRunRejectsExternalInput(t *testing.T) {
	runtime := New(WithLogger(telemetry.NoopLogger{}))
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", TurnID: "turn-1"}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: "run-1", TurnID: "turn-1", Attempt: 1,
	}}
	result := &planner.PlanResult{Await: planner.NewAwait(
		planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "clarification-1", Question: "Which unit?",
		}),
	)}

	_, err := runtime.runLoop(
		&testWorkflowContext{ctx: t.Context()},
		AgentRegistration{},
		input,
		base,
		result,
		policy.CapsState{},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.ErrorContains(t, err, "sessionless run cannot request external input")
}
