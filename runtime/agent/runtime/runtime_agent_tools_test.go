//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDefaultAgentToolExecute_TemplatePreferredOverText(t *testing.T) {
	rt := &Runtime{
		agents:  make(map[string]AgentRegistration),
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{ctx: context.Background()}
	ctx := engine.WithWorkflowContext(context.Background(), wf)

	var got []planner.AgentMessage
	rt.agents["svc.agent"] = AgentRegistration{ID: "svc.agent", Planner: &stubPlanner{start: func(ctx context.Context, input planner.PlanInput) (*planner.PlanResult, error) {
		got = append([]planner.AgentMessage{}, input.Messages...)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "ok"}}}, nil
	}}}

	tmpl := template.Must(template.New("t").Parse("hello {{.x}}"))
	cfg := AgentToolConfig{AgentID: "svc.agent", SystemPrompt: "sys", Templates: map[tools.Ident]*template.Template{"svc.ts.tool": tmpl}, Texts: map[tools.Ident]string{"svc.ts.tool": "fallback"}}

	exec := defaultAgentToolExecute(rt, cfg)
	res, err := exec(ctx, planner.ToolRequest{Name: tools.Ident("svc.ts.tool"), RunID: "run", Payload: map[string]string{"x": "world"}})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Result)
	require.Len(t, got, 2)
	require.Equal(t, "system", got[0].Role)
	require.Equal(t, "sys", got[0].Content)
	require.Equal(t, "user", got[1].Role)
	require.Equal(t, "hello world", got[1].Content)
}

func TestDefaultAgentToolExecute_UsesTextWhenNoTemplate(t *testing.T) {
	rt := &Runtime{
		agents:  make(map[string]AgentRegistration),
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{ctx: context.Background()}
	ctx := engine.WithWorkflowContext(context.Background(), wf)

	var got []planner.AgentMessage
	rt.agents["svc.agent"] = AgentRegistration{ID: "svc.agent", Planner: &stubPlanner{start: func(ctx context.Context, input planner.PlanInput) (*planner.PlanResult, error) {
		got = append([]planner.AgentMessage{}, input.Messages...)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "ok"}}}, nil
	}}}

	cfg := AgentToolConfig{AgentID: "svc.agent", Texts: map[tools.Ident]string{"svc.ts.tool": "just text"}}
	exec := defaultAgentToolExecute(rt, cfg)
	res, err := exec(ctx, planner.ToolRequest{Name: tools.Ident("svc.ts.tool"), RunID: "run"})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Result)
	require.Len(t, got, 1)
	require.Equal(t, "user", got[0].Role)
	require.Equal(t, "just text", got[0].Content)
}

func TestDefaultAgentToolExecute_DefaultsWhenMissingContent(t *testing.T) {
	rt := &Runtime{
		agents:  make(map[string]AgentRegistration),
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{ctx: context.Background()}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	var seen []planner.AgentMessage
	rt.agents["svc.agent"] = AgentRegistration{ID: "svc.agent", Planner: &stubPlanner{start: func(ctx context.Context, input planner.PlanInput) (*planner.PlanResult, error) {
		seen = append([]planner.AgentMessage{}, input.Messages...)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "ok"}}}, nil
	}}}
	cfg := AgentToolConfig{AgentID: "svc.agent"}
	exec := defaultAgentToolExecute(rt, cfg)
	res, err := exec(ctx, planner.ToolRequest{Name: tools.Ident("svc.ts.tool"), RunID: "run"})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Result)
	require.Len(t, seen, 1)
	require.Equal(t, "user", seen[0].Role)
	require.Empty(t, seen[0].Content)
}
