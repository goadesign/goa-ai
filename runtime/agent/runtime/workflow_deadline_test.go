package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestAdvanceStepBudgetDeadline(t *testing.T) {
	plannerErr := errors.New("history compression failed")
	tests := []struct {
		name              string
		remaining         time.Duration
		resumeErr         error
		exhaustOnResume   bool
		wantCalls         int
		wantFinalizations int
		wantErr           error
	}{
		{
			name:              "near budget finalizes without normal resume",
			remaining:         minActivityTimeout,
			wantCalls:         1,
			wantFinalizations: 1,
		},
		{
			name:              "timeout at exhausted budget finalizes",
			remaining:         4 * time.Second,
			resumeErr:         context.DeadlineExceeded,
			exhaustOnResume:   true,
			wantCalls:         2,
			wantFinalizations: 1,
		},
		{
			name:      "timeout while budget remains fails",
			remaining: 4 * time.Second,
			resumeErr: context.DeadlineExceeded,
			wantCalls: 1,
			wantErr:   context.DeadlineExceeded,
		},
		{
			name:            "non-timeout at exhausted budget fails",
			remaining:       4 * time.Second,
			resumeErr:       plannerErr,
			exhaustOnResume: true,
			wantCalls:       1,
			wantErr:         plannerErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := time.Unix(100, 0)
			budget := current.Add(test.remaining)
			hard := budget.Add(30 * time.Second)
			var calls, finalizations int
			loop, wfCtx := newResumeDeadlineTestLoop(
				t,
				func() time.Time { return current },
				runDeadlines{Budget: budget, Hard: hard},
				func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
					calls++
					if input.Finalize != nil {
						finalizations++
						require.Equal(t, planner.TerminationReasonTimeBudget, input.Finalize.Reason)
						return deadlineTestFinalOutput(), nil
					}
					if test.exhaustOnResume {
						current = budget
					}
					return nil, test.resumeErr
				},
			)

			out, err := loop.advanceStep(deadlineTestResumeBatch())

			if test.wantErr != nil {
				require.Nil(t, out)
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, out)
			}
			require.Equal(t, test.wantCalls, calls)
			require.Equal(t, test.wantFinalizations, finalizations)
			if finalizations > 0 {
				require.Equal(t, hard.Sub(current), wfCtx.lastPlannerCall.Options.ScheduleToCloseTimeout)
			}
		})
	}
}

func newResumeDeadlineTestLoop(
	t *testing.T,
	now func() time.Time,
	deadlines runDeadlines,
	resume func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error),
) (*workflowLoop, *routeWorkflowContext) {
	t.Helper()

	rt := New()
	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		now:   now,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": resume,
		},
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "session-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	}
	state := newRunLoopState(
		&planner.PlanResult{},
		nil,
		model.TokenUsage{},
		policy.CapsState{},
		2,
	)
	activityOptions := engine.ActivityOptions{
		RetryPolicy: engine.RetryPolicy{
			MaxAttempts:        3,
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
		},
	}
	return newWorkflowLoop(
		rt,
		wfCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		input,
		base,
		state,
		"turn-1",
		nil,
		nil,
		deadlines,
		activityOptions,
		activityOptions,
	), wfCtx
}

func deadlineTestResumeBatch() stepBatch {
	return stepBatch{
		program: stepProgram{
			result: &planner.PlanResult{},
			kind:   stepKindTools,
		},
	}
}

func deadlineTestFinalOutput() *PlanActivityOutput {
	return &PlanActivityOutput{
		Result: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "final"}},
				},
			},
		},
	}
}
