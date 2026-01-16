package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// timeoutReceiver is a test receiver that deterministically times out without blocking.
type timeoutReceiver[T any] struct{}

func (timeoutReceiver[T]) Receive(ctx context.Context) (T, error) {
	var zero T
	<-ctx.Done()
	return zero, ctx.Err()
}

func (timeoutReceiver[T]) ReceiveWithTimeout(ctx context.Context, timeout time.Duration) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if timeout <= 0 {
		return zero, context.DeadlineExceeded
	}
	return zero, context.DeadlineExceeded
}

func (timeoutReceiver[T]) ReceiveAsync() (T, bool) {
	var zero T
	return zero, false
}

type confirmationTimeoutWorkflowContext struct{ *testWorkflowContext }

func (w *confirmationTimeoutWorkflowContext) ConfirmationDecisions() engine.Receiver[api.ConfirmationDecision] {
	return timeoutReceiver[api.ConfirmationDecision]{}
}

type clarificationTimeoutWorkflowContext struct{ *testWorkflowContext }

func (w *clarificationTimeoutWorkflowContext) ClarificationAnswers() engine.Receiver[api.ClarificationAnswer] {
	return timeoutReceiver[api.ClarificationAnswer]{}
}

type resumeTimeoutWorkflowContext struct{ *testWorkflowContext }

func (w *resumeTimeoutWorkflowContext) ResumeRequests() engine.Receiver[api.ResumeRequest] {
	return timeoutReceiver[api.ResumeRequest]{}
}

func pauseResumeSequence(evts []hooks.Event) []string {
	seq := make([]string, 0, 8)
	for _, evt := range evts {
		switch e := evt.(type) {
		case *hooks.RunPausedEvent:
			seq = append(seq, "pause:"+e.Reason)
		case *hooks.RunResumedEvent:
			seq = append(seq, "resume:"+e.Notes)
		}
	}
	return seq
}

func TestRunLoopConfirmationTimeoutBalancesPauseResume(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		RunEventStore: runloginmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("tool"): func() tools.ToolSpec {
				spec := newAnyJSONSpec("tool", "svc.ts")
				spec.Confirmation = &tools.ConfirmationSpec{
					Title:                "Confirm tool",
					PromptTemplate:       "ok",
					DeniedResultTemplate: "null",
				}
				return spec
			}(),
		},
	}

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
	baseCtx.ensureSignals()
	wfCtx := &confirmationTimeoutWorkflowContext{testWorkflowContext: baseCtx}

	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", SessionID: "sess-1"}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     input.RunID,
			SessionID: input.SessionID,
			TurnID:    "turn-1",
		},
		Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID}),
	}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:       tools.Ident("tool"),
		ToolCallID: "tool-1",
		Payload:    []byte(`{}`),
	}}}
	ctrl := interrupt.NewController(wfCtx)

	deadline := wfCtx.Now().Add(1 * time.Hour)
	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ID: input.AgentID, ResumeActivityName: "resume"},
		input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1},
		deadline,
		2,
		"turn-1",
		nil,
		ctrl,
		0,
	)
	require.NoError(t, err)
	require.NotNil(t, out)

	require.Equal(t, []string{
		"pause:await_confirmation",
		"resume:confirmation_timeout",
		"pause:finalize",
		"resume:finalize",
	}, pauseResumeSequence(recorder.events))
}

func TestMissingFieldsClarificationTimeoutBalancesPauseResume(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		RunEventStore: runloginmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
	}

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
	baseCtx.ensureSignals()
	wfCtx := &clarificationTimeoutWorkflowContext{testWorkflowContext: baseCtx}
	ctrl := interrupt.NewController(wfCtx)

	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", SessionID: "sess-1"}
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
		RetryHint: &planner.RetryHint{
			Reason:             planner.RetryReasonMissingFields,
			Tool:               tools.Ident("tool"),
			MissingFields:      []string{"field"},
			ClarifyingQuestion: "provide field",
		},
	}}

	nextAttempt := 2
	deadline := wfCtx.Now().Add(1 * time.Hour)
	out, err := rt.handleMissingFieldsPolicy(
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
		model.TokenUsage{},
		&nextAttempt,
		"turn-1",
		ctrl,
		deadline,
	)
	require.NoError(t, err)
	require.NotNil(t, out)

	require.Equal(t, []string{
		"pause:await_clarification",
		"resume:clarification_timeout",
		"pause:finalize",
		"resume:finalize",
	}, pauseResumeSequence(recorder.events))
}

func TestHandleInterruptsTimeoutBalancesPauseResume(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		RunEventStore: runloginmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
	}

	baseCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
	}
	baseCtx.ensureSignals()
	baseCtx.pauseCh <- api.PauseRequest{RunID: "run-1", Reason: "human", RequestedBy: "user"}

	wfCtx := &resumeTimeoutWorkflowContext{testWorkflowContext: baseCtx}
	ctrl := interrupt.NewController(wfCtx)

	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", SessionID: "sess-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID, SessionID: input.SessionID, TurnID: "turn-1"}}
	nextAttempt := 2
	deadline := wfCtx.Now().Add(1 * time.Hour)

	err := rt.handleInterrupts(wfCtx, input, base, "turn-1", ctrl, &nextAttempt, deadline)
	require.NoError(t, err)

	require.Equal(t, []string{
		"pause:human",
		"resume:deadline_exceeded",
	}, pauseResumeSequence(recorder.events))
}
