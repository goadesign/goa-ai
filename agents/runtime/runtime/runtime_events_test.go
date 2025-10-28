//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/agents/runtime/planner"
	"goa.design/goa-ai/agents/runtime/policy"
	"goa.design/goa-ai/agents/runtime/run"
	"goa.design/goa-ai/agents/runtime/tools"
)

func TestEventSequencingMonotonic(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{Bus: recorder, toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
		return planner.ToolResult{
			Name: call.Name,
		}, nil
	}}}}
	rt.toolSpecs = map[string]tools.ToolSpec{"svc.ts.tool": newAnyJSONSpec("svc.ts.tool")}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, planResult: planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "ok"}}}, hasPlanResult: true}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", TurnID: "turn-1"}
	base := planner.PlanInput{RunContext: run.Context{RunID: input.RunID, TurnID: input.TurnID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := planner.PlanResult{ToolCalls: []planner.ToolCallRequest{{Name: "svc.ts.tool"}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, 2, &turnSequencer{turnID: input.TurnID}, nil, nil)
	require.NoError(t, err)
	seqs := make([]int, 0, len(recorder.events))
	for _, evt := range recorder.events {
		seqs = append(seqs, evt.SeqInTurn())
	}
	for i := 1; i < len(seqs); i++ {
		require.Greater(t, seqs[i], seqs[i-1])
	}
}
