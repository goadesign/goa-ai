//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agents/interrupt"
	"goa.design/goa-ai/runtime/agents/planner"
	"goa.design/goa-ai/runtime/agents/policy"
	"goa.design/goa-ai/runtime/agents/run"
)

func TestRunLoopPauseResumeEmitsEvents_Barriered(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{Bus: recorder, toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
		return planner.ToolResult{
			Name: call.Name,
		}, nil
	}}}}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, barrier: make(chan struct{}, 1)}
	go func() {
		// enqueue pause/resume before allowing async completion
		wfCtx.SignalChannel(interrupt.SignalPause).(*testSignalChannel).ch <- interrupt.PauseRequest{RunID: "run-1", Reason: "human"}
		wfCtx.SignalChannel(interrupt.SignalResume).(*testSignalChannel).ch <- interrupt.ResumeRequest{RunID: "run-1", Notes: "resume"}
		time.Sleep(5 * time.Millisecond)
		wfCtx.barrier <- struct{}{}
	}()
	wfCtx.hasPlanResult = true
	wfCtx.planResult = planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "ok"}}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "svc.ts.tool"}}}
	ctrl := interrupt.NewController(wfCtx)
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, 2, &turnSequencer{turnID: "turn-1"}, nil, ctrl)
	require.NoError(t, err)
}
