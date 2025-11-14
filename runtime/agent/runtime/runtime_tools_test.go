package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestExecuteToolActivityReturnsErrorAndHint(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		require.Equal(t, "tool-1", call.ToolCallID)
		require.Equal(t, "parent-1", call.ParentToolCallID)
		require.Equal(t, "run", call.RunID)
		return &planner.ToolResult{
			Name:  call.Name,
			Error: planner.NewToolError("invalid payload"),
			RetryHint: &planner.RetryHint{
				Reason: planner.RetryReasonInvalidArguments,
				Tool:   call.Name,
			},
		}, nil
	}}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newAnyJSONSpec("tool", "svc.ts"),
	}
	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: "tool", ToolCallID: "tool-1", ParentToolCallID: "parent-1", Payload: []byte("null")}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "invalid payload", out.Error)
	require.Nil(t, out.Payload)
	require.NotNil(t, out.RetryHint)
	require.Equal(t, planner.RetryReasonInvalidArguments, out.RetryHint.Reason)
}

func TestToolsetTaskQueueOverrideUsed(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.export": {TaskQueue: "q1", Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		return &planner.ToolResult{
			Name: call.Name,
		}, nil
	}}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{"child": newAnyJSONSpec("child", "svc.export")}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, planResult: &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, hasPlanResult: true}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("child")}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, 2, nil, nil, nil, 0)
	require.NoError(t, err)
	require.Equal(t, "q1", wfCtx.lastRequest.Queue)
	ti, ok := wfCtx.lastRequest.Input.(ToolInput)
	require.True(t, ok, "expected ToolInput in lastRequest.Input")
	require.Equal(t, "svc.export", ti.ToolsetName)
}

func TestPreserveModelProvidedToolCallID(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		// Ensure the ID provided by the planner/model flows into the executor unchanged
		require.Equal(t, "model-123", call.ToolCallID)
		return &planner.ToolResult{Name: call.Name}, nil
	}}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{"tool": newAnyJSONSpec("tool", "svc.ts")}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, planResult: &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, hasPlanResult: true}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	// Planner supplies an explicit ToolCallID from the model
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("tool"), ToolCallID: "model-123"}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, 2, nil, nil, nil, 0)
	require.NoError(t, err)
	// Activity input should carry the same ID
	ti, ok := wfCtx.lastRequest.Input.(ToolInput)
	require.True(t, ok)
	require.Equal(t, "model-123", ti.ToolCallID)
}

func TestActivityToolExecutorExecute(t *testing.T) {
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("123")}}
	exec := &ActivityToolExecutor{activityName: "execute"}
	input := ToolInput{RunID: "r1"}
	out, err := exec.Execute(context.Background(), wfCtx, &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, []byte("123"), []byte(out.Payload))
}

func TestRunLoopPauseResumeEmitsEvents(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:     recorder,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name: call.Name,
			}, nil
		}}},
	}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{"tool": newAnyJSONSpec("tool", "svc.ts")}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, barrier: make(chan struct{}, 1)}
	// Allow tests to enqueue pause/resume before async completes
	go func() {
		time.Sleep(5 * time.Millisecond)
		wfCtx.SignalChannel(interrupt.SignalPause).(*testSignalChannel).ch <- interrupt.PauseRequest{RunID: "run-1", Reason: "human"}
		wfCtx.SignalChannel(interrupt.SignalResume).(*testSignalChannel).ch <- interrupt.ResumeRequest{RunID: "run-1", Notes: "resume"}
		wfCtx.barrier <- struct{}{}
	}()
	wfCtx.hasPlanResult = true
	wfCtx.planResult = &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("tool")}}}
	ctrl := interrupt.NewController(wfCtx)
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, 2, &turnSequencer{turnID: "turn-1"}, nil, ctrl, 0)
	require.NoError(t, err)
	var sawPause, sawResume bool
	for _, evt := range recorder.events {
		switch evt.(type) {
		case *hooks.RunPausedEvent:
			sawPause = true
		case *hooks.RunResumedEvent:
			sawResume = true
		}
	}
	require.True(t, sawPause)
	require.True(t, sawResume)
}
