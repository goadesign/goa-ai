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
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type timeoutClarificationReceiver struct{}

func (timeoutClarificationReceiver) Receive(ctx context.Context) (*api.ClarificationAnswer, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (timeoutClarificationReceiver) ReceiveWithTimeout(ctx context.Context, timeout time.Duration) (*api.ClarificationAnswer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	return nil, context.DeadlineExceeded
}

func (timeoutClarificationReceiver) ReceiveAsync() (*api.ClarificationAnswer, bool) {
	return nil, false
}

type timeoutResumeReceiver struct{}

func (timeoutResumeReceiver) Receive(ctx context.Context) (*api.ResumeRequest, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (timeoutResumeReceiver) ReceiveWithTimeout(ctx context.Context, timeout time.Duration) (*api.ResumeRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	return nil, context.DeadlineExceeded
}

func (timeoutResumeReceiver) ReceiveAsync() (*api.ResumeRequest, bool) {
	return nil, false
}

type clarificationTimeoutWorkflowContext struct{ *testWorkflowContext }

func (w *clarificationTimeoutWorkflowContext) ClarificationAnswers() engine.Receiver[*api.ClarificationAnswer] {
	return timeoutClarificationReceiver{}
}

type resumeTimeoutWorkflowContext struct{ *testWorkflowContext }

func (w *resumeTimeoutWorkflowContext) ResumeRequests() engine.Receiver[*api.ResumeRequest] {
	return timeoutResumeReceiver{}
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

func TestMissingFieldsClarificationTimeoutBalancesPauseResume(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		RunEventStore: runloginmem.New(),
		SessionStore:  sessioninmem.New(),
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
		SessionStore:  sessioninmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
	}

	baseCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
	}
	baseCtx.ensureSignals()
	baseCtx.pauseCh <- &api.PauseRequest{RunID: "run-1", Reason: "human", RequestedBy: "user"}

	wfCtx := &resumeTimeoutWorkflowContext{testWorkflowContext: baseCtx}
	ctrl := interrupt.NewController(wfCtx)

	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", SessionID: "sess-1"}
	seedRunMeta(t, rt, input)
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
