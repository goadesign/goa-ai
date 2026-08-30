package runtime

// workflow_deadline_test.go verifies that the runtime owns Budget and Hard
// deadlines while engine adapters preserve the cause of planner timeouts.

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
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestDerivedWorkflowContextsPreserveWorkflowTime(t *testing.T) {
	current := time.Unix(100, 0)
	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		now: func() time.Time { return current },
	}

	require.Equal(t, current, wfCtx.Detached().Now())
	child, cancel := wfCtx.WithCancel()
	defer cancel()
	require.Equal(t, current, child.Now())
}

func TestAdvanceStepBudgetDeadline(t *testing.T) {
	plannerErr := errors.New("history compression failed")
	tests := []struct {
		name              string
		remaining         time.Duration
		resumeErr         error
		advanceToBudget   bool
		wantCalls         int
		wantFinalizations int
		wantErr           error
	}{
		{
			name:              "activity deadline finalizes without clock inference",
			remaining:         4 * time.Second,
			resumeErr:         engine.ErrPlannerActivityDeadlineExceeded,
			wantCalls:         2,
			wantFinalizations: 1,
		},
		{
			name:            "provider timeout after budget fails",
			remaining:       4 * time.Second,
			resumeErr:       context.DeadlineExceeded,
			advanceToBudget: true,
			wantCalls:       1,
			wantErr:         context.DeadlineExceeded,
		},
		{
			name:            "non-timeout after budget fails",
			remaining:       4 * time.Second,
			resumeErr:       plannerErr,
			advanceToBudget: true,
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
					if test.advanceToBudget {
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

func TestAdvanceStepUsesAgentToolResultContractWhenNoChildrenRan(t *testing.T) {
	agentTool := newAnyJSONSpec("specialist.inspect")
	agentTool.IsAgentTool = true
	nativeTool := newAnyJSONSpec("catalog.lookup")

	tests := []struct {
		name            string
		records         []stepToolRecord
		wantRecoveryIDs []string
	}{
		{
			name: "correctable agent failure with successful sibling",
			records: []stepToolRecord{
				{
					call: ToolCall{Name: agentTool.Name, ToolCallID: "agent-call"},
					result: &planner.ToolResult{
						Name:       agentTool.Name,
						ToolCallID: "agent-call",
						Failure: testToolFailure(
							planner.FailureInvalidCall,
							planner.RecoveryCorrectCall,
							"correct the agent call",
						),
					},
				},
				{
					call: ToolCall{Name: nativeTool.Name, ToolCallID: "native-call"},
					result: &planner.ToolResult{
						Name:       nativeTool.Name,
						ToolCallID: "native-call",
						Result:     map[string]any{"ok": true},
					},
				},
			},
			wantRecoveryIDs: []string{"agent-call"},
		},
		{
			name:            "successful agent result",
			wantRecoveryIDs: []string{},
			records: []stepToolRecord{{
				call: ToolCall{Name: agentTool.Name, ToolCallID: "agent-call"},
				result: &planner.ToolResult{
					Name:       agentTool.Name,
					ToolCallID: "agent-call",
					Result:     map[string]any{"ok": true},
				},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var resumes int
			loop, _ := newResumeDeadlineTestLoop(
				t,
				func() time.Time { return time.Unix(100, 0) },
				runDeadlines{},
				func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
					resumes++
					require.Nil(t, input.Finalize)
					require.Equal(t, test.wantRecoveryIDs, input.RecoveryToolCallIDs)
					return deadlineTestFinalOutput(), nil
				},
			)
			seedTestToolSpecs(loop.r, agentTool, nativeTool)
			batch := deadlineTestResumeBatch()
			batch.records = test.records
			batch.recorded = len(test.records)

			out, err := loop.advanceStep(batch)

			require.NoError(t, err)
			require.Nil(t, out)
			require.Equal(t, 1, resumes)
		})
	}
}

func TestExecuteWorkflowFinalizesPlanStartAtBudget(t *testing.T) {
	const (
		timeBudget     = 20 * time.Second
		finalizerGrace = 30 * time.Second
	)
	current := time.Unix(100, 0)
	var planOutput *PlanActivityOutput
	planErr := engine.ErrPlannerActivityDeadlineExceeded
	finalOutput := deadlineTestFinalOutput()
	var finalErr error
	rt := New(newTestStore())
	rt.agents["agent-1"] = AgentRegistration{Definition: testRegistrationDefinition("agent-1", engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, PlanActivityName: "plan",
		ResumeActivityName: "resume",
		ResumeActivityOptions: engine.ActivityOptions{
			StartToCloseTimeout: time.Minute,
		},
		Policy: RunPolicy{
			TimeBudget:     timeBudget,
			FinalizerGrace: finalizerGrace,
		},
	}
	var wfCtx *routeWorkflowContext
	wfCtx = &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		now:   func() time.Time { return current },
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				require.Equal(t, timeBudget, wfCtx.lastPlannerCall.Options.ScheduleToCloseTimeout)
				current = current.Add(timeBudget)
				return planOutput, planErr
			},
			"resume": func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
				require.NotNil(t, input.Finalize)
				require.Equal(t, planner.TerminationReasonTimeBudget, input.Finalize.Reason)
				require.Equal(t, finalizerGrace, wfCtx.lastPlannerCall.Options.ScheduleToCloseTimeout)
				return finalOutput, finalErr
			},
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, finalOutput.Result.FinalResponse.Message, out.Final)
}

func TestExecuteWorkflowKeepsPlanStartResultAtBudget(t *testing.T) {
	const timeBudget = 20 * time.Second
	current := time.Unix(100, 0)
	planOutput := deadlineTestFinalOutput()
	var planErr error
	finalOutput := deadlineTestFinalOutput()
	var finalErr error
	resumeCalls := 0
	rt := New(newTestStore())
	rt.agents["agent-1"] = AgentRegistration{Definition: testRegistrationDefinition("agent-1", engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, PlanActivityName: "plan",
		ResumeActivityName: "resume",
		Policy: RunPolicy{
			TimeBudget: timeBudget,
		},
	}
	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		now:   func() time.Time { return current },
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				current = current.Add(timeBudget)
				return planOutput, planErr
			},
			"resume": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				resumeCalls++
				return finalOutput, finalErr
			},
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	})

	require.NoError(t, err)
	require.Equal(t, planOutput.Result.FinalResponse.Message, out.Final)
	require.Zero(t, resumeCalls)
}

func TestWorkflowLoopKeepsPlanResumeResultAtBudget(t *testing.T) {
	current := time.Unix(100, 0)
	budget := current.Add(20 * time.Second)
	finalOutput := deadlineTestFinalOutput()
	var finalErr error
	finalizerCalls := 0
	loop, _ := newResumeDeadlineTestLoop(
		t,
		func() time.Time { return current },
		runDeadlines{
			Budget: budget,
			Hard:   budget.Add(30 * time.Second),
		},
		func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
			finalizerCalls++
			return finalOutput, finalErr
		},
	)
	loop.st.Result = deadlineTestFinalOutput().Result
	current = budget

	out, err := loop.run()

	require.NoError(t, err)
	require.Equal(t, loop.st.Result.FinalResponse.Message, out.Final)
	require.Zero(t, finalizerCalls)
}

func TestWorkflowLoopDoesNotCommitToolPlanRejectedAtBudget(t *testing.T) {
	current := time.Unix(100, 0)
	budget := current.Add(20 * time.Second)
	finalOutput := deadlineTestFinalOutput()
	var finalErr error
	loop, _ := newResumeDeadlineTestLoop(
		t,
		func() time.Time { return current },
		runDeadlines{
			Budget: budget,
			Hard:   budget.Add(30 * time.Second),
		},
		func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
			return finalOutput, finalErr
		},
	)
	loop.st.Result = &PlanResult{
		ToolCalls: []ToolCall{{
			Name:       "service.tool",
			ToolCallID: "call-1",
			Payload:    rawjson.Message(`{}`),
		}},
	}
	current = budget

	out, err := loop.run()

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NoError(t, transcript.ValidatePlannerTranscript(loop.base.Messages))
}

func TestExecuteWorkflowPreservesPlanStartProviderTimeout(t *testing.T) {
	current := time.Unix(100, 0)
	var planOutput *PlanActivityOutput
	planErr := context.DeadlineExceeded
	finalOutput := deadlineTestFinalOutput()
	var finalErr error
	rt := New(newTestStore())
	rt.agents["agent-1"] = AgentRegistration{Definition: testRegistrationDefinition("agent-1", engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, PlanActivityName: "plan",
		ResumeActivityName: "resume",
		Policy: RunPolicy{
			TimeBudget: 20 * time.Second,
		},
	}
	resumeCalls := 0
	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		now:   func() time.Time { return current },
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				current = current.Add(20 * time.Second)
				return planOutput, planErr
			},
			"resume": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				resumeCalls++
				return finalOutput, finalErr
			},
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	})

	require.Nil(t, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, engine.ErrPlannerActivityDeadlineExceeded)
	require.Zero(t, resumeCalls)
}

func TestExecuteWorkflowClassifiesExpiredPlanStartFinalizer(t *testing.T) {
	const (
		timeBudget     = 20 * time.Second
		finalizerGrace = 30 * time.Second
	)
	current := time.Unix(100, 0)
	var planOutput *PlanActivityOutput
	planErr := engine.ErrPlannerActivityDeadlineExceeded
	finalOutput := deadlineTestFinalOutput()
	var finalErr error
	rt := New(newTestStore())
	rt.agents["agent-1"] = AgentRegistration{Definition: testRegistrationDefinition("agent-1", engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, PlanActivityName: "plan",
		ResumeActivityName: "resume",
		ResumeActivityOptions: engine.ActivityOptions{
			StartToCloseTimeout: finalizerGrace,
		},
		Policy: RunPolicy{
			TimeBudget:     timeBudget,
			FinalizerGrace: finalizerGrace,
		},
	}
	resumeCalls := 0
	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		now:   func() time.Time { return current },
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				current = current.Add(timeBudget + finalizerGrace)
				return planOutput, planErr
			},
			"resume": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				resumeCalls++
				return finalOutput, finalErr
			},
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	})

	require.Nil(t, out)
	require.ErrorIs(t, err, engine.ErrPlannerActivityDeadlineExceeded)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, resumeCalls)
}

func TestMissingFieldsFinalizationUsesHardDeadline(t *testing.T) {
	current := time.Unix(100, 0)
	hard := current.Add(30 * time.Second)
	finalOutput := deadlineTestFinalOutput()
	var finalErr error
	loop, wfCtx := newResumeDeadlineTestLoop(
		t,
		func() time.Time { return current },
		runDeadlines{
			Budget: current.Add(20 * time.Second),
			Hard:   hard,
		},
		func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
			require.NotNil(t, input.Finalize)
			require.Equal(t, planner.TerminationReasonToolFailure, input.Finalize.Reason)
			require.Len(t, input.ToolOutputs, 1)
			require.Equal(t, "call-1", input.ToolOutputs[0].ToolCallID)
			return finalOutput, finalErr
		},
	)
	loop.reg.Policy.OnMissingFields = MissingFieldsFinalize
	loop.input.Policy = &PolicyOverrides{
		LimitTerminalPlans: testLimitTerminalPlans("service.tools.complete"),
	}
	loop.st.ToolOutputs = []*planner.ToolOutput{{
		CallRunID:   "run-1",
		ResultRunID: "run-1",
		Name:        "tool",
		ToolCallID:  "call-1",
		Payload:     rawjson.Message(`{}`),
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			planner.RecoveryCorrectCall,
			"missing field",
		),
	}}
	batch := deadlineTestResumeBatch()
	batch.recorded = 1
	batch.records = []stepToolRecord{{
		result: &planner.ToolResult{
			Name: "tool",
			Failure: &planner.ToolFailure{
				Recovery: planner.RecoveryDirective{
					Action: planner.RecoveryCorrectCall,
					Issues: []*tools.FieldIssue{{
						Field:      "field",
						Constraint: "missing_field",
					}},
				},
			},
		},
	}}

	out, err := loop.advanceStep(batch)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, hard.Sub(current), wfCtx.lastPlannerCall.Options.ScheduleToCloseTimeout)
}

func TestMissingFieldsFinalizationCannotBypassCompletionTool(t *testing.T) {
	loop, _ := newResumeDeadlineTestLoop(
		t,
		func() time.Time { return time.Unix(100, 0) },
		runDeadlines{},
		func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
			require.FailNow(t, "completion run must not enter planner finalization")
			return nil, nil
		},
	)
	loop.reg.Policy.OnMissingFields = MissingFieldsFinalize
	loop.input.Policy = &PolicyOverrides{CompletionTool: "service.tools.persist"}
	batch := deadlineTestResumeBatch()
	batch.recorded = 1
	batch.records = []stepToolRecord{{
		result: &planner.ToolResult{
			Name: "service.tools.persist",
			Failure: &planner.ToolFailure{
				Recovery: planner.RecoveryDirective{
					Action: planner.RecoveryCorrectCall,
					Issues: []*tools.FieldIssue{{
						Field:      "field",
						Constraint: "missing_field",
					}},
				},
			},
		},
	}}

	out, err := loop.advanceStep(batch)

	require.Nil(t, out)
	require.ErrorContains(t, err, `completion tool "service.tools.persist" did not succeed`)
	require.ErrorContains(t, err, string(planner.TerminationReasonToolFailure))
}

func newResumeDeadlineTestLoop(
	t *testing.T,
	now func() time.Time,
	deadlines runDeadlines,
	resume func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error),
) (*workflowLoop, *routeWorkflowContext) {
	t.Helper()

	rt := New(newTestStore())
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
		&PlanResult{},
		nil,
		model.TokenUsage{},
		initialCaps(RunPolicy{}),
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
		deadlines,
		activityOptions,
		activityOptions,
	), wfCtx
}

func deadlineTestResumeBatch() stepBatch {
	return stepBatch{
		program: stepProgram{
			result: &PlanResult{},
			kind:   stepKindTools,
		},
	}
}

func deadlineTestFinalOutput() *PlanActivityOutput {
	return &PlanActivityOutput{
		PublicationBatchID: testPublicationBatchID,
		Result: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "final"}},
				},
			},
		},
	}
}
