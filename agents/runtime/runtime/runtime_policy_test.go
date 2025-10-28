//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/agents/runtime/hooks"
	"goa.design/goa-ai/agents/runtime/planner"
	"goa.design/goa-ai/agents/runtime/policy"
	"goa.design/goa-ai/agents/runtime/run"
	"goa.design/goa-ai/agents/runtime/tools"
)

func TestPolicyAllowlistTrimsToolExecution(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{Bus: recorder, Policy: &stubPolicyEngine{decision: policy.Decision{AllowedTools: []policy.ToolHandle{{ID: "svc.tools.allowed"}}}}}
	rt.toolsets = map[string]ToolsetRegistration{"svc.tools": {Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
		return planner.ToolResult{
			Name:    call.Name,
			Payload: map[string]any{"ok": true},
		}, nil
	}}}
	rt.toolSpecs = map[string]tools.ToolSpec{"svc.tools.allowed": newAnyJSONSpec("svc.tools.allowed"), "svc.tools.blocked": newAnyJSONSpec("svc.tools.blocked")}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, planResult: planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "done"}}}, hasPlanResult: true}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := planner.PlanResult{ToolCalls: []planner.ToolCallRequest{{Name: "svc.tools.allowed"}, {Name: "svc.tools.blocked"}}}
	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5}, time.Time{}, 2, &turnSequencer{turnID: "turn-1"}, nil, nil)
	require.NoError(t, err)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, "svc.tools.allowed", out.ToolEvents[0].Name)
	var scheduled []string
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolCallScheduledEvent); ok {
			scheduled = append(scheduled, e.ToolName)
		}
	}
	require.Equal(t, []string{"svc.tools.allowed"}, scheduled)
}
