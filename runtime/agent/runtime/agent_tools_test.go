package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// planner that captures messages passed to PlanStart and returns a final response
type capturePlanner struct{ msgs []planner.AgentMessage }

func (p *capturePlanner) PlanStart(ctx context.Context, in planner.PlanInput) (planner.PlanResult, error) {
	p.msgs = append([]planner.AgentMessage(nil), in.Messages...)
	return planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "ok"}}}, nil
}
func (p *capturePlanner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (planner.PlanResult, error) {
	return planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "done"}}}, nil
}

func TestAgentTool_DefaultsFromPayload(t *testing.T) {
	rt := &Runtime{agents: make(map[string]AgentRegistration), Engine: engineinmem.New()}
	const agentID = "svc.agent"
	pl := &capturePlanner{}
	// Register nested agent
	require.NoError(t, rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:                  agentID,
		Planner:             pl,
		Workflow:            engine.WorkflowDefinition{Name: "wf", Handler: func(engine.WorkflowContext, any) (any, error) { return &RunOutput{}, nil }},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	}))

	// Build registration with no per-tool content: default uses PayloadToString
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{AgentID: agentID})
	wf := &testWorkflowContext{ctx: context.Background()}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	// String payload path
	tr, err := reg.Execute(ctx, planner.ToolRequest{RunID: "r1", SessionID: "s1", Name: tools.Ident("svc.tools.do"), Payload: "hello"})
	require.NoError(t, err)
	require.Equal(t, tools.Ident("svc.tools.do"), tr.Name)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, "user", pl.msgs[0].Role)
	require.Equal(t, "hello", pl.msgs[0].Content)
}

func TestAgentTool_PromptBuilderOverrides(t *testing.T) {
	rt := &Runtime{agents: make(map[string]AgentRegistration), Engine: engineinmem.New()}
	const agentID = "svc.agent"
	pl := &capturePlanner{}
	require.NoError(t, rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:                  agentID,
		Planner:             pl,
		Workflow:            engine.WorkflowDefinition{Name: "wf", Handler: func(engine.WorkflowContext, any) (any, error) { return &RunOutput{}, nil }},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	}))
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{AgentID: agentID, Prompt: func(_ tools.Ident, payload any) string {
		return "PB:" + PayloadToString(payload)
	}})
	wf := &testWorkflowContext{ctx: context.Background()}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	tr, err := reg.Execute(ctx, planner.ToolRequest{RunID: "r1", SessionID: "s1", Name: tools.Ident("svc.tools.do"), Payload: "hello"})
	require.NoError(t, err)
	require.Equal(t, tools.Ident("svc.tools.do"), tr.Name)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, "PB:hello", pl.msgs[0].Content)
}

func TestAgentTool_SystemPromptPrepended(t *testing.T) {
	rt := &Runtime{agents: make(map[string]AgentRegistration), Engine: engineinmem.New()}
	const agentID = "svc.agent"
	pl := &capturePlanner{}
	require.NoError(t, rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:                  agentID,
		Planner:             pl,
		Workflow:            engine.WorkflowDefinition{Name: "wf", Handler: func(engine.WorkflowContext, any) (any, error) { return &RunOutput{}, nil }},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	}))
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{AgentID: agentID, SystemPrompt: "SYS"})
	wf := &testWorkflowContext{ctx: context.Background()}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	_, err := reg.Execute(ctx, planner.ToolRequest{RunID: "r1", SessionID: "s1", Name: tools.Ident("svc.tools.do"), Payload: "hello"})
	require.NoError(t, err)
	require.Len(t, pl.msgs, 2)
	require.Equal(t, "system", pl.msgs[0].Role)
	require.Equal(t, "SYS", pl.msgs[0].Content)
	require.Equal(t, "hello", pl.msgs[1].Content)
}
