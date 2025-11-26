package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestPolicyAllowlistTrimsToolExecution(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:     recorder,
		Policy:  &stubPolicyEngine{decision: policy.Decision{AllowedTools: []tools.Ident{tools.Ident("allowed")}}},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	rt.toolsets = map[string]ToolsetRegistration{"svc.tools": {
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:   call.Name,
				Result: map[string]any{"ok": true},
			}, nil
		}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{"allowed": newAnyJSONSpec("allowed", "svc.tools"), "blocked": newAnyJSONSpec("blocked", "svc.tools")}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, planResult: &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "done"}}}}}, hasPlanResult: true}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("allowed")}, {Name: tools.Ident("blocked")}}}
	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5}, time.Time{}, 2, &turnSequencer{turnID: "turn-1"}, nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, tools.Ident("allowed"), out.ToolEvents[0].Name)
	var scheduled []tools.Ident
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolCallScheduledEvent); ok {
			scheduled = append(scheduled, e.ToolName)
		}
	}
	require.Equal(t, []tools.Ident{tools.Ident("allowed")}, scheduled)
}
