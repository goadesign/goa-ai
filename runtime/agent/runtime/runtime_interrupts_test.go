package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRunLoopPauseResumeEmitsEvents_Barriered(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:     recorder,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		toolsets: map[string]ToolsetRegistration{"svc.ts": {
			Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
				return &planner.ToolResult{
					Name: call.Name,
				}, nil
			}}},
	}
	// Strong contract: codecs must be present. Provide a minimal spec for the tool.
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newAnyJSONSpec("tool", "svc.ts"),
	}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, barrier: make(chan struct{}, 1)}
	go func() {
		// enqueue pause/resume before allowing async completion
		wfCtx.SignalChannel(interrupt.SignalPause).(*testSignalChannel).ch <- interrupt.PauseRequest{RunID: "run-1", Reason: "human"}
		wfCtx.SignalChannel(interrupt.SignalResume).(*testSignalChannel).ch <- interrupt.ResumeRequest{RunID: "run-1", Notes: "resume"}
		time.Sleep(5 * time.Millisecond)
		wfCtx.barrier <- struct{}{}
	}()
	wfCtx.hasPlanResult = true
	wfCtx.planResult = &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "tool"}}}
	ctrl := interrupt.NewController(wfCtx)
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, 2, "turn-1", nil, ctrl, 0)
	require.NoError(t, err)
}
