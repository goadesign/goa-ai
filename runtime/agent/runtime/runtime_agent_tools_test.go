//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

const jsonNullLiteral = "null"

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
		AgentToolContent: AgentToolContent{
			Templates: map[tools.Ident]*template.Template{"tool": tmpl},
			Texts:     map[tools.Ident]string{"tool": "fallback"},
		},
	}

	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
		Payload:   rawjson.Message([]byte(`{"x":"world"}`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
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
		AgentToolContent: AgentToolContent{
			Texts: map[tools.Ident]string{"tool": "just text"},
		},
	}
	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{Name: tools.Ident("tool"), RunID: "run", SessionID: "sess-1"}
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
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

func TestDefaultAgentToolExecute_DefaultContentFromPayload(t *testing.T) {
	var got []*model.Message
	rt, ctx := setupTestAgentWithPlanner(func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		if input != nil {
			got = append([]*model.Message{}, input.Messages...)
		}
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
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
		Payload:   rawjson.Message([]byte(`{"x":"world"}`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
	res, err := exec(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "ok", res.Result)
	require.Len(t, got, 1)
	require.Equal(t, model.ConversationRoleUser, got[0].Role)
	require.JSONEq(t, `{"x":"world"}`, firstText(got[0]))
}

func TestDefaultAgentToolExecute_PreChildValidatorReturnsToolResult(t *testing.T) {
	rt, ctx := setupTestAgentWithPlanner(func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		t.Fatal("planner must not run when pre-child validation fails")
		return nil, nil
	})
	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		PreChildValidator: func(context.Context, *AgentToolValidationInput) *AgentToolValidationError {
			return NewAgentToolValidationError(
				"sources must come from prior evidence",
				[]*tools.FieldIssue{
					{
						Field:      "sources",
						Constraint: "invalid_format",
					},
				},
				map[string]string{
					"sources": "sources must come from prior evidence",
				},
			)
		},
	}
	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
		Payload:   rawjson.Message([]byte(`{"sources":["x"]}`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)

	result, err := exec(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Error)
	require.NotNil(t, result.RetryHint)
	require.Equal(t, planner.RetryReasonInvalidArguments, result.RetryHint.Reason)
	require.Equal(t, []string{"sources"}, result.RetryHint.MissingFields)
	require.True(t, result.RetryHint.RestrictToTool)
}

func TestDefaultAgentToolExecute_PromptSpecPreferredOverTemplateTextPromptBuilder(t *testing.T) {
	var got []*model.Message
	rt, ctx := setupTestAgentWithPlanner(func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		if input == nil {
			return &planner.PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  "assistant",
						Parts: []model.Part{model.TextPart{Text: "ok"}},
					},
				},
			}, nil
		}
		got = append([]*model.Message{}, input.Messages...)
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  "assistant",
					Parts: []model.Part{model.TextPart{Text: "ok"}},
				},
			},
		}, nil
	})
	rt.PromptRegistry = prompt.NewRegistry(nil)
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "agent.tool.prompt",
		AgentID:  "svc.agent",
		Role:     prompt.PromptRoleUser,
		Template: "from-spec {{ .x }}",
	}))

	tmpl := template.Must(template.New("fallback").Parse("from-template {{ .x }}"))
	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		AgentToolContent: AgentToolContent{
			PromptSpecs: map[tools.Ident]prompt.Ident{
				"tool": "agent.tool.prompt",
			},
			Templates: map[tools.Ident]*template.Template{
				"tool": tmpl,
			},
			Texts: map[tools.Ident]string{
				"tool": "from-text",
			},
			Prompt: func(id tools.Ident, payload any) string {
				return "from-builder"
			},
		},
	}
	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
		Payload:   rawjson.Message([]byte(`{"x":"world"}`)),
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)

	res, err := exec(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "ok", res.Result)
	require.Len(t, got, 1)
	if tp, ok := got[0].Parts[0].(model.TextPart); ok {
		require.Equal(t, "from-spec world", tp.Text)
	} else {
		t.Fatalf("expected TextPart in user message, got %#v", got[0].Parts)
	}
}

func TestDefaultAgentToolExecute_PromptSpecMissingReturnsError(t *testing.T) {
	rt, ctx := setupTestAgentWithPlanner(func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  "assistant",
					Parts: []model.Part{model.TextPart{Text: "ok"}},
				},
			},
		}, nil
	})
	rt.PromptRegistry = prompt.NewRegistry(nil)
	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		AgentToolContent: AgentToolContent{
			PromptSpecs: map[tools.Ident]prompt.Ident{
				"tool": "missing.prompt",
			},
		},
	}
	exec := defaultAgentToolExecute(rt, cfg)
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
	}
	rt.toolSpecs[call.Name] = newAnyJSONSpec(call.Name, "svc.tools")
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)

	_, err := exec(ctx, &call)
	require.Error(t, err)
	require.Contains(t, err.Error(), "render prompt")
}

func TestDefaultAgentToolExecute_PromptSpecRendersWithSchemaKeys(t *testing.T) {
	type payload struct {
		TimeContext string `json:"time_context"`
	}
	var got []*model.Message
	rt, ctx := setupTestAgentWithPlanner(func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		if input != nil {
			got = append([]*model.Message{}, input.Messages...)
		}
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "ok"}},
				},
			},
		}, nil
	})
	rt.PromptRegistry = prompt.NewRegistry(nil)
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "agent.tool.prompt",
		AgentID:  "svc.agent",
		Role:     prompt.PromptRoleUser,
		Template: "from-spec {{ .time_context }}",
	}))
	codec := tools.JSONCodec[any]{
		ToJSON: func(v any) ([]byte, error) {
			typed, ok := v.(*payload)
			if !ok {
				return nil, fmt.Errorf("expected *payload, got %T", v)
			}
			return json.Marshal(typed)
		},
		FromJSON: func(data []byte) (any, error) {
			if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == jsonNullLiteral {
				return nil, nil
			}
			var decoded payload
			if err := json.Unmarshal(data, &decoded); err != nil {
				return nil, err
			}
			return &decoded, nil
		},
	}
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
		Payload:   rawjson.Message([]byte(`{"time_context":"last 48h"}`)),
	}
	spec := newAnyJSONSpec(call.Name, "svc.tools")
	spec.Payload.Codec = codec
	rt.toolSpecs[call.Name] = spec
	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		AgentToolContent: AgentToolContent{
			PromptSpecs: map[tools.Ident]prompt.Ident{
				call.Name: "agent.tool.prompt",
			},
		},
	}
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
	exec := defaultAgentToolExecute(rt, cfg)
	_, err := exec(ctx, &call)
	require.NoError(t, err)
	require.Len(t, got, 1)
	text, ok := got[0].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Equal(t, "from-spec last 48h", text.Text)
}

func TestDefaultAgentToolExecute_PromptSpecRejectsNonObjectPayloadShape(t *testing.T) {
	rt, ctx := setupTestAgentWithPlanner(func(_ context.Context, _ *planner.PlanInput) (*planner.PlanResult, error) {
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "ok"}},
				},
			},
		}, nil
	})
	rt.PromptRegistry = prompt.NewRegistry(nil)
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "agent.tool.prompt",
		AgentID:  "svc.agent",
		Role:     prompt.PromptRoleUser,
		Template: "from-spec {{ .time_context }}",
	}))
	stringCodec := tools.JSONCodec[any]{
		ToJSON: func(v any) ([]byte, error) {
			typed, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("expected string, got %T", v)
			}
			return json.Marshal(typed)
		},
		FromJSON: func(data []byte) (any, error) {
			if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == jsonNullLiteral {
				return nil, nil
			}
			var decoded string
			if err := json.Unmarshal(data, &decoded); err != nil {
				return nil, err
			}
			return decoded, nil
		},
	}
	call := planner.ToolRequest{
		Name:      tools.Ident("tool"),
		RunID:     "run",
		SessionID: "sess-1",
		Payload:   rawjson.Message([]byte(`"last 48h"`)),
	}
	spec := newAnyJSONSpec(call.Name, "svc.tools")
	spec.Payload.Codec = stringCodec
	rt.toolSpecs[call.Name] = spec
	cfg := AgentToolConfig{
		AgentID: "svc.agent",
		Route: AgentRoute{
			ID:               agent.Ident("svc.agent"),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		AgentToolContent: AgentToolContent{
			PromptSpecs: map[tools.Ident]prompt.Ident{
				call.Name: "agent.tool.prompt",
			},
		},
	}
	seedParentRun(t, rt.SessionStore, call.RunID, call.SessionID)
	exec := defaultAgentToolExecute(rt, cfg)
	_, err := exec(ctx, &call)
	require.Error(t, err)
	require.Contains(t, err.Error(), "prompt payload must render from a JSON object")
}

func TestBuildAgentChildRequest_PreservesCanonicalToolArgs(t *testing.T) {
	rt := &Runtime{
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
	}

	toolName := tools.Ident("tool")
	spec := newAnyJSONSpec(toolName, "svc.tools")
	spec.Payload.Codec = tools.JSONCodec[any]{
		ToJSON: func(v any) ([]byte, error) {
			panic(fmt.Sprintf("payload codec ToJSON must not be called in child args handoff, got %T", v))
		},
		FromJSON: func(data []byte) (any, error) {
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				return nil, err
			}
			return decoded, nil
		},
	}
	rt.toolSpecs[toolName] = spec

	payload := rawjson.Message([]byte(`{"time_context":"last 48h","scope_context":{"type":"site","id":"s1"}}`))
	call := &planner.ToolRequest{
		Name:       toolName,
		RunID:      "run-1",
		ToolCallID: "tooluse_123",
		SessionID:  "sess-1",
		Payload:    payload,
	}
	cfg := &AgentToolConfig{
		AgentToolContent: AgentToolContent{
			Texts: map[tools.Ident]string{
				toolName: "use payload",
			},
		},
	}

	_, nestedRunCtx, err := rt.buildAgentChildRequest(context.Background(), cfg, call, nil, nil)
	require.NoError(t, err)
	require.Equal(t, payload, nestedRunCtx.ToolArgs)
}
