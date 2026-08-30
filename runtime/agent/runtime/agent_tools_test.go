package runtime

// This file checks registration and execution contracts for agents exposed as
// tools, including child workflow routing and result conversion.

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

const parentAgentID = "parent.agent"

func TestAgentToolPlannerOutputFailureEndsParentWorkflow(t *testing.T) {
	t.Parallel()

	cause := temporalerrors.Wrap(planner.NewOutputContractError(
		errors.New("summary reply used prose"),
	))
	ready := make(chan struct{})
	close(ready)
	exec := &toolBatchExec{r: &Runtime{}}
	_, _, _, err := exec.collectAgentChildResults(
		&testWorkflowContext{ctx: context.Background()},
		[]agentChildFutureInfo{{
			handle: &controlledChildHandle{ready: ready, err: cause},
			call:   ToolCall{Name: "child", ToolCallID: "child-1"},
		}},
		nil,
	)

	require.Error(t, err)
	require.True(t, temporalerrors.IsOutputContract(err))
}

func TestAgentToolProviderFailureEndsParentWorkflow(t *testing.T) {
	t.Parallel()

	cause := temporalerrors.Wrap(model.NewProviderError(
		"anthropic",
		"complete",
		503,
		model.ProviderErrorKindUnavailable,
		"service_unavailable",
		"provider unavailable",
		"request-1",
		true,
		nil,
	))
	ready := make(chan struct{})
	close(ready)
	exec := &toolBatchExec{r: &Runtime{}}
	results, _, _, err := exec.collectAgentChildResults(
		&testWorkflowContext{ctx: context.Background()},
		[]agentChildFutureInfo{{
			handle: &controlledChildHandle{ready: ready, err: cause},
			call:   ToolCall{Name: "child", ToolCallID: "child-1"},
		}},
		nil,
	)

	require.Error(t, err)
	require.Empty(t, results)
	providerErr, ok := temporalerrors.Provider(err)
	require.True(t, ok)
	require.Equal(t, "anthropic", providerErr.Provider())
	require.True(t, providerErr.Retryable())
}

func TestAgentToolPlannerOutputFailureSkipsParentResume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		wrap func(error) error
	}{
		{name: "native", wrap: func(err error) error { return err }},
		{name: "Temporal", wrap: temporalerrors.Wrap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			const (
				childPlanActivity  = "child.plan"
				childResume        = "child.resume"
				childExecute       = "child.execute"
				parentPlan         = "parent.plan"
				parentResume       = "parent.resume"
				parentExecute      = "parent.execute"
				childWorkflowName  = "child.workflow"
				parentWorkflowName = "parent.workflow"
			)
			childID := agent.Ident("service.child")
			parentID := agent.Ident("service.parent")
			childTool := tools.Ident("child.tools.run")
			childSpec := newAnyJSONSpec(childTool)
			childSpec.IsAgentTool = true
			childSpec.AgentID = string(childID)
			rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

			require.NoError(t, rt.RegisterAgent(context.Background(), AgentRegistration{
				ID: childID,
				Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return nil, test.wrap(planner.NewOutputContractError(
						errors.New("invalid child reply"),
					))
				}},
				Workflow: engine.WorkflowDefinition{
					Name: childWorkflowName,
					Handler: func(wfCtx engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
						return rt.ExecuteWorkflow(wfCtx, input)
					},
				},
				PlanActivityName:    childPlanActivity,
				ResumeActivityName:  childResume,
				ExecuteToolActivity: childExecute,
			}))
			childToolset := NewAgentToolsetRegistration(rt, AgentToolConfig{
				AgentID: childID,
				Route: AgentRoute{
					ID:               childID,
					WorkflowName:     childWorkflowName,
					DefaultTaskQueue: "child.queue",
				},
				Name: "child.tools",
				AgentToolContent: AgentToolContent{
					Texts: map[tools.Ident]string{childTool: "run child"},
				},
			})
			childToolset.Specs = []tools.ToolSpec{childSpec}
			require.NoError(t, rt.RegisterToolset(childToolset))

			var parentResumeCalls atomic.Int32
			parentRegistration := AgentRegistration{
				ID: parentID,
				Planner: &stubPlanner{resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
					parentResumeCalls.Add(1)
					return finalPlannerResult("unexpected resume"), nil
				}},
				Workflow: engine.WorkflowDefinition{
					Name: parentWorkflowName,
					Handler: func(wfCtx engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
						return rt.ExecuteWorkflow(wfCtx, input)
					},
				},
				PlanActivityName:    parentPlan,
				ResumeActivityName:  parentResume,
				ExecuteToolActivity: parentExecute,
				Specs:               []tools.ToolSpec{childSpec},
			}
			require.NoError(t, rt.RegisterAgent(context.Background(), parentRegistration))

			sessionID := "session-" + test.name
			_, err := createSessionForTest(context.Background(), rt.Store, sessionID)
			require.NoError(t, err)
			input := &RunInput{
				AgentID:   parentID,
				RunID:     "parent-run-" + test.name,
				SessionID: sessionID,
				TurnID:    "parent-turn-" + test.name,
			}
			seedRunMeta(t, rt, input)
			wfCtx := &routeWorkflowContext{
				ctx:          context.Background(),
				runID:        input.RunID,
				hookRuntime:  rt,
				childRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					childPlanActivity: rt.PlanStartActivity,
					parentResume:      rt.PlanResumeActivity,
				},
				toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){},
			}

			_, err = rt.runLoop(
				wfCtx,
				parentRegistration,
				input,
				&planner.PlanInput{RunContext: run.Context{
					RunID: input.RunID, SessionID: sessionID, TurnID: input.TurnID, Attempt: 1,
				}},
				&PlanResult{ToolCalls: []ToolCall{{
					ToolCallID: "child-call",
					Name:       childTool,
					Payload:    rawjson.Message(`{}`),
				}}},
				policy.CapsState{MaxToolCalls: 2, RemainingToolCalls: 2},
				time.Time{},
				time.Time{},
				input.TurnID,
				nil,
			)

			require.Error(t, err)
			require.True(t, temporalerrors.IsOutputContract(err), "unexpected error: %v", err)
			require.Zero(t, parentResumeCalls.Load())
		})
	}
}

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
// to exist in the runtime store before hook events are processed.
func seedParentRun(t *testing.T, store storage.Store, runID, sessionID string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := createSessionForTest(context.Background(), store, sessionID)
	require.NoError(t, err)
	admitRunForTest(t, store, session.RunMeta{
		AgentID:   parentAgentID,
		RunID:     runID,
		SessionID: sessionID,
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	})
}

func TestAgentTool_DefaultContentFromPayload(t *testing.T) {
	rt := &Runtime{
		agents:    make(map[agent.Ident]AgentRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		Engine:    engineinmem.New(),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       noopHooks{},
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
	call := ToolCall{
		ToolCallID: "call-1",
		RunID:      "r1",
		SessionID:  "s1",
		Name:       tools.Ident("svc.tools.do"),
		Payload:    rawjson.Message([]byte(`"hello"`)),
	}
	registerAgentToolTestConfig(rt, *reg.AgentTool, "svc.tools", newAnyJSONSpec(call.Name))
	call.AgentID = parentAgentID
	seedParentRun(t, rt.Store, call.RunID, call.SessionID)
	tr, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.Len(t, pl.msgs, 1)
	require.Equal(t, model.ConversationRoleUser, pl.msgs[0].Role)
	require.Equal(t, `"hello"`, firstText(pl.msgs[0]))
}

func TestAgentToolRejectsUnknownFieldThroughPayloadCodec(t *testing.T) {
	rt := &Runtime{
		agents:    make(map[agent.Ident]AgentRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		Engine:    engineinmem.New(),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       noopHooks{},
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

	type strictPayload struct {
		SourcesRef string `json:"sources_ref"`
	}
	callName := tools.Ident("svc.tools.get_time_series")
	spec := tools.ToolSpec{
		Name: callName,
		Payload: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					var raw map[string]json.RawMessage
					if err := json.Unmarshal(data, &raw); err != nil {
						return nil, err
					}
					if _, ok := raw["server_data"]; ok {
						return nil, tools.NewValidationError(
							"unknown field server_data",
							[]*tools.FieldIssue{{Field: "server_data", Constraint: "unknown_field"}},
							nil,
						)
					}
					var out strictPayload
					if err := json.Unmarshal(data, &out); err != nil {
						return nil, err
					}
					return &out, nil
				},
			},
		},
		Result: tools.TypeSpec{Codec: tools.AnyJSONCodec},
	}
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{
		AgentID: agentID,
		Route: AgentRoute{
			ID:               agent.Ident(agentID),
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
		AgentToolContent: AgentToolContent{
			Prompt: func(_ tools.Ident, payload any) string {
				typed, ok := payload.(*strictPayload)
				require.True(t, ok)
				return typed.SourcesRef
			},
		},
	})
	registerAgentToolTestConfig(rt, *reg.AgentTool, "svc.tools", spec)

	call := ToolCall{
		ToolCallID:      "call-1",
		ModelToolCallID: "call-1",
		RunID:           "r1",
		SessionID:       "s1",
		Name:            callName,
		Payload:         rawjson.Message([]byte(`{"sources_ref":"src_1","server_data":"on"}`)),
	}
	call.AgentID = parentAgentID
	seedParentRun(t, rt.Store, call.RunID, call.SessionID)
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	ctx := engine.WithWorkflowContext(context.Background(), wf)
	tr, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.NotNil(t, tr.ToolResult)
	require.NotNil(t, tr.ToolResult.Failure)
	require.Equal(t, planner.FailureInvalidCall, tr.ToolResult.Failure.Kind)
	require.Equal(t, planner.RecoveryCorrectCall, tr.ToolResult.Failure.Recovery.Action)
	require.Empty(t, tr.ToolResult.Failure.Recovery.PriorInput)
	require.Empty(t, tr.ToolResult.Failure.Recovery.ExampleJSON)
	require.Empty(t, pl.msgs)
	require.Empty(t, wf.childRequests)
}

func TestAgentTool_TextContent(t *testing.T) {
	rt := &Runtime{
		agents:    make(map[agent.Ident]AgentRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		Engine:    engineinmem.New(),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       noopHooks{},
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
	call := ToolCall{
		ToolCallID: "call-1",
		RunID:      "r1",
		SessionID:  "s1",
		Name:       tools.Ident("svc.tools.do"),
		Payload:    rawjson.Message([]byte(`"hello"`)),
	}
	registerAgentToolTestConfig(rt, *reg.AgentTool, "svc.tools", newAnyJSONSpec(call.Name))
	call.AgentID = parentAgentID
	seedParentRun(t, rt.Store, call.RunID, call.SessionID)
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
		agents:    make(map[agent.Ident]AgentRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		Engine:    engineinmem.New(),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       noopHooks{},
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
	call := ToolCall{
		ToolCallID: "call-1",
		RunID:      "r1",
		SessionID:  "s1",
		Name:       tools.Ident("svc.tools.do"),
		Payload:    rawjson.Message([]byte(`"hello"`)),
	}
	registerAgentToolTestConfig(rt, *reg.AgentTool, "svc.tools", newAnyJSONSpec(call.Name))
	call.AgentID = parentAgentID
	seedParentRun(t, rt.Store, call.RunID, call.SessionID)
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
		agents:    make(map[agent.Ident]AgentRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		Engine:    engineinmem.New(),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       noopHooks{},
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
	call := ToolCall{
		ToolCallID: "call-1",
		RunID:      "r1",
		SessionID:  "s1",
		Name:       tools.Ident("svc.tools.do"),
		Payload:    rawjson.Message([]byte(`"hello"`)),
	}
	registerAgentToolTestConfig(rt, *reg.AgentTool, "svc.tools", newAnyJSONSpec(call.Name))
	call.AgentID = parentAgentID
	seedParentRun(t, rt.Store, call.RunID, call.SessionID)
	_, err := reg.Execute(ctx, &call)
	require.NoError(t, err)
	require.Len(t, pl.msgs, 2)
	require.Equal(t, model.ConversationRoleSystem, pl.msgs[0].Role)
	require.Equal(t, "SYS", firstText(pl.msgs[0]))
	require.Equal(t, "hello", firstText(pl.msgs[1]))
}

func TestAgentTool_UsesFinalToolResultBeforeAggregation(t *testing.T) {
	rt := &Runtime{
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
	}

	type ParentResult struct {
		Events []string `json:"events"`
	}

	parent := tools.ToolSpec{
		Name: tools.Ident(parentAgentID),
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
	call := &ToolCall{
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

	tr, err := rt.adaptAgentChildOutput(cfg, call, run.Context{RunID: "child-run"}, out)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.Nil(t, tr.Failure)
	typed, ok := tr.Result.(*ParentResult)
	require.True(t, ok)
	require.Equal(t, []string{"ok"}, typed.Events)
	require.NotNil(t, tr.RunLink)
	require.Equal(t, "child-run", tr.RunLink.RunID)
}

func TestConvertRunOutputToToolResultKeepsTerminalChildFailureHistorical(t *testing.T) {
	output := &RunOutput{
		Final: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "completed after recovery"}},
		},
		ToolEvents: []*api.ToolEvent{
			{
				Name:    "child.correctable",
				Failure: testToolFailure(planner.FailureInvalidCall, planner.RecoveryCorrectCall, "bad call"),
			},
			{
				Name:    "child.internal",
				Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "broken"),
			},
		},
	}

	result, err := ConvertRunOutputToToolResult("parent.agent_tool", output)

	require.NoError(t, err)
	require.Nil(t, result.Failure)
	require.Equal(t, "completed after recovery", result.Result)
}

func TestConvertRunOutputToToolResultKeepsNonTerminalChildFailuresHistorical(t *testing.T) {
	output := &RunOutput{
		Final: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "completed without the unavailable tool"}},
		},
		ToolEvents: []*api.ToolEvent{
			{
				Name:    "child.search",
				Failure: testToolFailure(planner.FailureUnavailable, planner.RecoveryReplan, "offline"),
			},
		},
	}

	result, err := ConvertRunOutputToToolResult("parent.agent_tool", output)

	require.NoError(t, err)
	require.Nil(t, result.Failure)
	require.Equal(t, "completed without the unavailable tool", result.Result)
}
