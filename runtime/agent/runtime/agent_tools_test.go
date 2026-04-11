package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
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

// seedParentRun ensures runtime tests exercise agent-tool execution with a
// persisted parent run contract. Child-run linkage now requires the parent run
// to exist in the session store before hook events are processed.
func seedParentRun(t *testing.T, store session.Store, runID, sessionID string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := store.CreateSession(context.Background(), sessionID, now)
	require.NoError(t, err)
	err = store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "test.parent",
		RunID:     runID,
		SessionID: sessionID,
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	})
	require.NoError(t, err)
}

func TestAgentTool_DefaultContentFromPayload(t *testing.T) {
	rt := &Runtime{
		agents:        make(map[agent.Ident]AgentRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		Engine:        engineinmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
		SessionStore:  sessioninmem.New(),
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

	// Build registration with no per-tool content.
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
		Payload:   rawjson.Message([]byte(`"hello"`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
	tr, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, model.ConversationRoleUser, pl.msgs[0].Role)
	require.Equal(t, `"hello"`, firstText(pl.msgs[0]))
}

func TestAgentTool_TextContent(t *testing.T) {
	rt := &Runtime{
		agents:        make(map[agent.Ident]AgentRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		Engine:        engineinmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
		SessionStore:  sessioninmem.New(),
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
		AgentToolContent: AgentToolContent{
			Texts: map[tools.Ident]string{
				tools.Ident("svc.tools.do"): "hello",
			},
		},
	})
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	call := planner.ToolRequest{
		RunID:     "r1",
		SessionID: "s1",
		Name:      tools.Ident("svc.tools.do"),
		Payload:   rawjson.Message([]byte(`"hello"`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
	tr, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.NotNil(t, tr.ToolResult)
	require.Equal(t, tools.Ident("svc.tools.do"), tr.ToolResult.Name)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, model.ConversationRoleUser, pl.msgs[0].Role)
	require.Equal(t, "hello", firstText(pl.msgs[0]))
}

func TestAgentTool_PromptBuilderOverrides(t *testing.T) {
	rt := &Runtime{
		agents:        make(map[agent.Ident]AgentRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		Engine:        engineinmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
		SessionStore:  sessioninmem.New(),
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
		AgentToolContent: AgentToolContent{
			Prompt: func(_ tools.Ident, payload any) string {
				text, err := PayloadToString(payload)
				require.NoError(t, err)
				return "PB:" + text
			},
		},
	})
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	call := planner.ToolRequest{
		RunID:     "r1",
		SessionID: "s1",
		Name:      tools.Ident("svc.tools.do"),
		Payload:   rawjson.Message([]byte(`"hello"`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
	tr, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.NotNil(t, tr.ToolResult)
	require.Equal(t, tools.Ident("svc.tools.do"), tr.ToolResult.Name)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, "PB:hello", firstText(pl.msgs[0]))
}

func TestWithPromptSpecSetsPromptID(t *testing.T) {
	cfg := AgentToolConfig{}
	WithPromptSpec("svc.tools.do", "agent.tool.prompt")(&cfg)
	require.Equal(t, prompt.Ident("agent.tool.prompt"), cfg.PromptSpecs["svc.tools.do"])
}

func TestAgentTool_SystemPromptPrepended(t *testing.T) {
	rt := &Runtime{
		agents:        make(map[agent.Ident]AgentRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		Engine:        engineinmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
		SessionStore:  sessioninmem.New(),
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
		AgentToolContent: AgentToolContent{
			Prompt: func(id tools.Ident, payload any) string {
				val, _ := payload.(string)
				return val
			},
		},
	})
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	call := planner.ToolRequest{
		RunID:     "r1",
		SessionID: "s1",
		Name:      tools.Ident("svc.tools.do"),
		Payload:   rawjson.Message([]byte(`"hello"`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
	_, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.Len(t, pl.msgs, 2)
	require.Equal(t, model.ConversationRoleSystem, pl.msgs[0].Role)
	require.Equal(t, "SYS", firstText(pl.msgs[0]))
	require.Equal(t, "hello", firstText(pl.msgs[1]))
}

func TestAgentTool_UsesFinalToolResultBeforeAggregation(t *testing.T) {
	rt := &Runtime{
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
	}

	type ParentResult struct {
		Events []string `json:"events"`
	}

	parent := tools.ToolSpec{
		Name: tools.Ident("test.parent"),
		Payload: tools.TypeSpec{
			Codec: tools.AnyJSONCodec,
		},
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
				FromJSON: func(data []byte) (any, error) {
					var out ParentResult
					if err := json.Unmarshal(data, &out); err != nil {
						return nil, err
					}
					return &out, nil
				},
			},
		},
	}
	rt.toolSpecs[parent.Name] = parent

	cfg := &AgentToolConfig{AgentID: "test.agent"}
	call := &planner.ToolRequest{
		Name:       parent.Name,
		ToolCallID: "toolcall",
		RunID:      "run",
		SessionID:  "sess",
	}
	out := &RunOutput{
		FinalToolResult: &api.ToolEvent{
			Name:   parent.Name,
			Result: rawjson.Message([]byte(`{"events":["ok"]}`)),
		},
		ToolEvents: []*api.ToolEvent{
			{
				Name:   tools.Ident("test.child"),
				Result: rawjson.Message([]byte(`"bad"`)),
			},
		},
	}

	tr, err := rt.adaptAgentChildOutput(context.Background(), cfg, call, run.Context{RunID: "child-run"}, out)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.Nil(t, tr.Error)
	typed, ok := tr.Result.(*ParentResult)
	require.True(t, ok)
	require.Equal(t, []string{"ok"}, typed.Events)
	require.NotNil(t, tr.RunLink)
	require.Equal(t, "child-run", tr.RunLink.RunID)
}
