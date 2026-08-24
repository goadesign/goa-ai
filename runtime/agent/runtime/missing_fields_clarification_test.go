package runtime

// This file verifies that missing generated arguments become an explicit
// clarification request instead of being repaired or guessed by the runtime.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func seedRunMeta(t *testing.T, rt *Runtime, input *RunInput) {
	t.Helper()
	now := time.Now().UTC()
	_, err := rt.SessionStore.CreateSession(context.Background(), input.SessionID, now)
	require.NoError(t, err)
	require.NoError(t, rt.SessionStore.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   string(input.AgentID),
		RunID:     input.RunID,
		SessionID: input.SessionID,
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	}))
}

func TestMissingFieldsClarificationReturnsTypedAwait(t *testing.T) {
	rt := &Runtime{
		RunEventStore: runloginmem.New(),
		SessionStore:  sessioninmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
	}
	seedTestToolSpecs(rt, tools.ToolSpec{
		Name: tools.Ident("tool"),
		Payload: tools.TypeSpec{
			FieldDescriptions: map[string]string{
				"field": "The facility detail needed to continue.",
			},
		},
	})

	baseCtx := &testWorkflowContext{
		ctx:           context.Background(),
		hookRuntime:   rt,
		hasPlanResult: true,
		planResult: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}},
			},
		},
	}
	wfCtx := baseCtx

	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", SessionID: "sess-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     input.RunID,
			SessionID: input.SessionID,
			TurnID:    "turn-1",
		},
		Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID}),
	}
	results := []*planner.ToolResult{{
		Name:       tools.Ident("tool"),
		ToolCallID: "tool-1",
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInvalidCall,
			Error: planner.NewToolError("missing field"),
			Recovery: planner.RecoveryDirective{
				Action:     planner.RecoveryCorrectCall,
				PriorInput: rawjson.Message(`{}`),
				Issues: []*tools.FieldIssue{{
					Field:      "field",
					Constraint: "missing_field",
				}},
			},
		},
	}}
	nextAttempt := 2
	deadline := wfCtx.Now().Add(1 * time.Hour)
	out, await, err := rt.applyMissingFieldsPolicy(
		wfCtx,
		AgentRegistration{
			ID:                 input.AgentID,
			ResumeActivityName: "resume",
			Policy:             RunPolicy{OnMissingFields: MissingFieldsAwaitClarification},
		},
		input,
		base,
		results,
		results,
		nil,
		model.TokenUsage{},
		&nextAttempt,
		"turn-1",
		&runDeadlines{Budget: deadline, Hard: deadline},
	)
	require.NoError(t, err)
	require.Nil(t, out)
	require.NotNil(t, await)
	require.Equal(t, planner.AwaitItemKindClarification, await.Kind)
	require.Contains(t, await.Clarification.Question, "The facility detail needed to continue.")
	require.Equal(t, []string{"field"}, await.Clarification.MissingFields)
	require.Equal(t, tools.Ident("tool"), await.Clarification.RestrictToTool)
	require.Empty(t, base.Messages)
}

func TestMissingFieldsClarificationResumesAfterAccountedFailure(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	completion.Payload.FieldDescriptions = map[string]string{
		"title": "The title to save.",
	}
	executions := 0
	resumes := 0
	h := newRecoveryHarness(
		t,
		"missing-fields-continuation",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			if string(call.Payload) == `{"title":"corrected"}` {
				return successfulToolResult(call), nil
			}
			result := invalidCallResult(call)
			result.Failure.Recovery.Issues = []*tools.FieldIssue{{
				Field:      "title",
				Constraint: "missing_field",
			}}
			return result, nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    completion.Name,
				Payload: rawjson.Message(`{"title":"corrected"}`),
			}}}, nil
		},
	)
	h.registration.Policy = RunPolicy{OnMissingFields: MissingFieldsAwaitClarification}
	h.registration.Specs = []tools.ToolSpec{completion}
	h.runtime.agents[h.input.AgentID] = h.registration
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	first, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: completion.Name, Payload: rawjson.Message(`{}`), ToolCallID: "persist-initial",
	}}}, policy.CapsState{
		MaxToolCalls:                        3,
		RemainingToolCalls:                  3,
		MaxConsecutiveFailedToolCalls:       2,
		RemainingConsecutiveFailedToolCalls: 2,
	})
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.Len(t, first.Suspension.Pending, 1)

	checkpoint, err := h.runtime.decodeWorkflowCheckpoint(first.Suspension)
	require.NoError(t, err)
	require.True(t, checkpoint.Batch.ResumePlannerAfterPending)
	require.Equal(t, 2, checkpoint.State.Caps.RemainingToolCalls)
	require.Equal(t, 1, checkpoint.State.Caps.RemainingConsecutiveFailedToolCalls)

	nextInput := &RunInput{
		AgentID:   h.input.AgentID,
		RunID:     "run-missing-fields-continuation-2",
		SessionID: h.input.SessionID,
		TurnID:    "turn-missing-fields-continuation-2",
		Continuation: &api.RunContinuationInput{
			Suspension: first.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID:     first.Suspension.Pending[0].Await.Clarification.ID,
				Answer: "Use the corrected title.",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(nextInput, checkpoint))
	seedRunMeta(t, h.runtime, nextInput)
	nextWorkflow := &routeWorkflowContext{
		ctx:           t.Context(),
		runID:         nextInput.RunID,
		hookRuntime:   h.runtime,
		plannerRoutes: h.workflow.plannerRoutes,
		toolRoutes:    h.workflow.toolRoutes,
	}

	out, err := h.runtime.resumeSuspendedWorkflow(
		nextWorkflow,
		h.registration,
		nextInput,
		checkpoint,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Suspension)
	require.Nil(t, out.Final)
	require.Equal(t, 2, executions)
	require.Equal(t, 1, resumes)
}

