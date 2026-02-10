//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// setupTestAgentWithPlanner creates a test runtime with an agent that uses the provided planner function.
func setupTestAgentWithPlanner(plannerFn func(context.Context, *planner.PlanInput) (*planner.PlanResult, error)) (*Runtime, context.Context) {
	rt := &Runtime{
		agents:        make(map[agent.Ident]AgentRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
		SessionStore:  sessioninmem.New(),
	}
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	rt.agents["svc.agent"] = AgentRegistration{
		ID:                  "svc.agent",
		Planner:             &stubPlanner{start: plannerFn},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
		Workflow:            engine.WorkflowDefinition{Name: "wf", Handler: func(engine.WorkflowContext, *RunInput) (*RunOutput, error) { return &RunOutput{}, nil }},
	}
	return rt, ctx
}

func TestDefaultAgentToolExecute_TemplatePreferredOverText(t *testing.T) {
	var got []*model.Message
	rt, ctx := setupTestAgentWithPlanner(func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		if input == nil {
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
		}
		got = append([]*model.Message{}, input.Messages...)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
	})

	tmpl := template.Must(template.New("t").Parse("hello {{.x}}"))
	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		SystemPrompt: "sys",
		Templates:    map[tools.Ident]*template.Template{"tool": tmpl},
		Texts:        map[tools.Ident]string{"tool": "fallback"},
	}

	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
		Payload:   json.RawMessage(`{"x":"world"}`),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	_, err := rt.CreateSession(context.Background(), call.SessionID)
	require.NoError(t, err)
	res, err := exec(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "ok", res.Result)
	// Agent-as-tool must attach a RunLink for the nested agent run.
	require.NotNil(t, res.RunLink)
	require.Equal(t, "run/agent/tool", res.RunLink.RunID)
	require.Equal(t, agent.Ident("svc.agent"), res.RunLink.AgentID)
	require.Equal(t, "run", res.RunLink.ParentRunID)
	require.Empty(t, res.RunLink.ParentToolCallID)
	require.Len(t, got, 2)
	require.Equal(t, model.ConversationRoleSystem, got[0].Role)
	if tp, ok := got[0].Parts[0].(model.TextPart); ok {
		require.Equal(t, "sys", tp.Text)
	} else {
		t.Fatalf("expected TextPart in system message, got %#v", got[0].Parts)
	}
	require.Equal(t, model.ConversationRoleUser, got[1].Role)
	if tp, ok := got[1].Parts[0].(model.TextPart); ok {
		require.Equal(t, "hello world", tp.Text)
	} else {
		t.Fatalf("expected TextPart in user message, got %#v", got[1].Parts)
	}
}

func TestDefaultAgentToolExecute_UsesTextWhenNoTemplate(t *testing.T) {
	var got []*model.Message
	rt, ctx := setupTestAgentWithPlanner(func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		if input == nil {
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
		}
		got = append([]*model.Message{}, input.Messages...)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
	})

	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		Texts: map[tools.Ident]string{"tool": "just text"},
	}
	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{Name: tools.Ident("tool"), RunID: "run", SessionID: "sess-1"}
	_, err := rt.CreateSession(context.Background(), call.SessionID)
	require.NoError(t, err)
	res, err := exec(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "ok", res.Result)
	require.Len(t, got, 1)
	require.Equal(t, model.ConversationRoleUser, got[0].Role)
	if tp, ok := got[0].Parts[0].(model.TextPart); ok {
		require.Equal(t, "just text", tp.Text)
	} else {
		t.Fatalf("expected TextPart in user message, got %#v", got[0].Parts)
	}
}

func TestDefaultAgentToolExecute_DefaultsWhenMissingContent(t *testing.T) {
	var seen []*model.Message
	rt, ctx := setupTestAgentWithPlanner(func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		if input == nil {
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
		}
		seen = append([]*model.Message{}, input.Messages...)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
	})
	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
	}
	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{Name: tools.Ident("tool"), RunID: "run", SessionID: "sess-1"}
	_, err := rt.CreateSession(context.Background(), call.SessionID)
	require.NoError(t, err)
	res, err := exec(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "ok", res.Result)
	require.Len(t, seen, 1)
	require.Equal(t, model.ConversationRoleUser, seen[0].Role)
	// When no content provided, default should be empty text part
	if len(seen[0].Parts) == 0 {
		// ok
	} else if tp, ok := seen[0].Parts[0].(model.TextPart); ok {
		require.Empty(t, tp.Text)
	} else {
		t.Fatalf("expected empty or TextPart in user message, got %#v", seen[0].Parts)
	}
}
