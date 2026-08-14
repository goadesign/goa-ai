package runtime

// This file verifies that missing generated arguments become an explicit
// clarification request instead of being repaired or guessed by the runtime.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
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
		planResult: &planner.PlanResult{
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
