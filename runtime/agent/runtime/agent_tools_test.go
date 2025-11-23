package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// planner that captures messages passed to PlanStart and returns a final response
type capturePlanner struct {
	msgs []*model.Message
}

func (p *capturePlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	if in == nil {
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
	}
	p.msgs = append([]*model.Message{}, in.Messages...)
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
}
func (p *capturePlanner) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "done"}}}}}, nil
}

func firstText(m *model.Message) string {
	if m == nil || len(m.Parts) == 0 {
		return ""
	}
	if tp, ok := m.Parts[0].(model.TextPart); ok {
		return tp.Text
	}
	return ""
}

func TestAgentTool_DefaultsFromPayload(t *testing.T) {
	rt := &Runtime{
		agents:  make(map[string]AgentRegistration),
		Engine:  engineinmem.New(),
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	const agentID = "svc.agent"
	pl := &capturePlanner{}
	// Register nested agent
	require.NoError(t, rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:                  agentID,
		Planner:             pl,
		Workflow:            engine.WorkflowDefinition{Name: "wf", Handler: func(engine.WorkflowContext, *RunInput) (*RunOutput, error) { return &RunOutput{}, nil }},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	}))

	// Build registration with no per-tool content: default uses PayloadToString
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{
		AgentID: agentID,
		Route: AgentRoute{
			ID:               agent.Ident(agentID),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
	})
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	// String payload path (canonical JSON)
	call := planner.ToolRequest{
		RunID:     "r1",
		SessionID: "s1",
		Name:      tools.Ident("svc.tools.do"),
		Payload:   json.RawMessage(`"hello"`),
	}
	tr, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.Equal(t, tools.Ident("svc.tools.do"), tr.Name)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, model.ConversationRoleUser, pl.msgs[0].Role)
	require.Equal(t, "hello", firstText(pl.msgs[0]))
}

func TestAgentTool_PromptBuilderOverrides(t *testing.T) {
	rt := &Runtime{
		agents:  make(map[string]AgentRegistration),
		Engine:  engineinmem.New(),
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	const agentID = "svc.agent"
	pl := &capturePlanner{}
	require.NoError(t, rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:                  agentID,
		Planner:             pl,
		Workflow:            engine.WorkflowDefinition{Name: "wf", Handler: func(engine.WorkflowContext, *RunInput) (*RunOutput, error) { return &RunOutput{}, nil }},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	}))
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{
		AgentID: agentID,
		Route: AgentRoute{
			ID:               agent.Ident(agentID),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		Prompt: func(_ tools.Ident, payload any) string {
			return "PB:" + PayloadToString(payload)
		},
	})
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	call := planner.ToolRequest{
		RunID:     "r1",
		SessionID: "s1",
		Name:      tools.Ident("svc.tools.do"),
		Payload:   json.RawMessage(`"hello"`),
	}
	tr, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.Equal(t, tools.Ident("svc.tools.do"), tr.Name)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, "PB:hello", firstText(pl.msgs[0]))
}

func TestAgentTool_SystemPromptPrepended(t *testing.T) {
	rt := &Runtime{
		agents:  make(map[string]AgentRegistration),
		Engine:  engineinmem.New(),
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	const agentID = "svc.agent"
	pl := &capturePlanner{}
	require.NoError(t, rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:                  agentID,
		Planner:             pl,
		Workflow:            engine.WorkflowDefinition{Name: "wf", Handler: func(engine.WorkflowContext, *RunInput) (*RunOutput, error) { return &RunOutput{}, nil }},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	}))
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{
		AgentID: agentID,
		Route: AgentRoute{
			ID:               agent.Ident(agentID),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		SystemPrompt: "SYS",
	})
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	call := planner.ToolRequest{
		RunID:     "r1",
		SessionID: "s1",
		Name:      tools.Ident("svc.tools.do"),
		Payload:   json.RawMessage(`"hello"`),
	}
	_, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.Len(t, pl.msgs, 2)
	require.Equal(t, model.ConversationRoleSystem, pl.msgs[0].Role)
	require.Equal(t, "SYS", firstText(pl.msgs[0]))
	require.Equal(t, "hello", firstText(pl.msgs[1]))
}

func TestToolResultFinalizerInvokesTool(t *testing.T) {
	finalizer := ToolResultFinalizer("svc.aggregate.finalize", func(_ context.Context, input FinalizerInput) (any, error) {
		return map[string]any{"parent": input.Parent.ToolName}, nil
	})
	input := FinalizerInput{
		Parent: ParentCall{
			ToolName:   "ada.method",
			ToolCallID: "parent-call",
		},
		Children: []ChildCall{
			{ToolName: "child", Status: "ok", Result: map[string]string{"foo": "bar"}},
		},
		Invoke: ToolInvokerFunc(func(ctx context.Context, tool tools.Ident, payload any) (*planner.ToolResult, error) {
			require.Equal(t, tools.Ident("svc.aggregate.finalize"), tool)
			require.Equal(t, map[string]any{"parent": tools.Ident("ada.method")}, payload)
			return &planner.ToolResult{
				Result: map[string]any{"status": "ok"},
			}, nil
		}),
	}
	result, err := finalizer.Finalize(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"status": "ok"}, result.Result)
}

func TestToolResultFinalizerMissingInvoker(t *testing.T) {
	finalizer := ToolResultFinalizer("svc.aggregate.finalize", func(context.Context, FinalizerInput) (any, error) {
		return map[string]any{}, nil
	})
	_, err := finalizer.Finalize(context.Background(), FinalizerInput{
		Parent: ParentCall{ToolName: "ada.method"},
	})
	require.Error(t, err)
}
