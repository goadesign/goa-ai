//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// nestedPlannerStub discovers children across iterations: first 2 children,
// then 1, then final.
type nestedPlannerStub struct {
	iter int
}

var _ engine.WorkflowContext = (*testWorkflowContext)(nil)
var _ engine.Future[*api.ToolOutput] = (*testToolFuture)(nil)

func (p *nestedPlannerStub) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	p.iter = 0
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("child1")}, {Name: tools.Ident("child2")}}}, nil
}
func (p *nestedPlannerStub) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.iter++
	if p.iter == 1 {
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("child3")}}}, nil
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "nested done"}}}}}, nil
}
func TestStartRunSetsWorkflowName(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				ID: "service.agent",
				Workflow: engine.WorkflowDefinition{
					Name:      "service.workflow",
					TaskQueue: "svc.queue",
				},
			},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := client.Start(context.Background(), "sess-1", nil)
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
}

func TestStartRunRequiresSessionID(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {ID: "service.agent", Workflow: engine.WorkflowDefinition{Name: "service.workflow", TaskQueue: "q"}},
		},
	}
	// Empty session ID
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := client.Start(context.Background(), "", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingSessionID)
	// Whitespace session ID
	_, err = client.Start(context.Background(), "  \t  ", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingSessionID)
	// Valid session ID
	_, err = client.Start(context.Background(), "s1", nil)
	require.NoError(t, err)
}

func TestRunOptionsPropagateToStartRequest(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {ID: "service.agent", Workflow: engine.WorkflowDefinition{Name: "service.workflow", TaskQueue: "q"}},
		},
	}

	meta := map[string]any{"source": "test"}
	memo := map[string]any{"wf": "name"}
	sa := map[string]any{"SessionID": "s1"}

	in := RunInput{
		AgentID:   "service.agent",
		SessionID: "sess-1",
	}
	for _, o := range []RunOption{
		WithTurnID("turn-1"),
		WithMetadata(meta),
		WithTaskQueue("custom.q"),
		WithMemo(memo),
		WithSearchAttributes(sa),
	} {
		o(&in)
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := client.Start(
		context.Background(),
		in.SessionID,
		nil,
		WithTurnID(in.TurnID),
		WithMetadata(in.Metadata),
		WithTaskQueue(in.WorkflowOptions.TaskQueue),
		WithMemo(in.WorkflowOptions.Memo),
		WithSearchAttributes(in.WorkflowOptions.SearchAttributes),
	)
	require.NoError(t, err)

	// Engine request
	require.Equal(t, "custom.q", eng.last.TaskQueue)
	require.Equal(t, "service.workflow", eng.last.Workflow)
	require.Equal(t, memo, eng.last.Memo)
	require.Equal(t, sa, eng.last.SearchAttributes)

	// Input payload
	inPtr := eng.last.Input
	require.Equal(t, "sess-1", inPtr.SessionID)
	require.Equal(t, "turn-1", inPtr.TurnID)
	require.Equal(t, meta, inPtr.Metadata)
}

func TestRuntimePauseRunSignalsWorkflow(t *testing.T) {
	rt := &Runtime{
		runHandles: make(map[string]engine.WorkflowHandle),
	}
	handle := &stubWorkflowHandle{}
	rt.storeWorkflowHandle("run-1", handle)

	req := interrupt.PauseRequest{RunID: "run-1", Reason: "human_review"}
	require.NoError(t, rt.PauseRun(context.Background(), req))
	require.Equal(t, interrupt.SignalPause, handle.lastSignal)
}

func TestRuntimeResumeRunSignalsWorkflow(t *testing.T) {
	rt := &Runtime{
		runHandles: make(map[string]engine.WorkflowHandle),
	}
	handle := &stubWorkflowHandle{}
	rt.storeWorkflowHandle("run-1", handle)

	req := interrupt.ResumeRequest{RunID: "run-1", Notes: "resume"}
	require.NoError(t, rt.ResumeRun(context.Background(), req))
	require.Equal(t, interrupt.SignalResume, handle.lastSignal)
}

func TestConsecutiveFailureBreaker(t *testing.T) {
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
				return &planner.ToolResult{
					Name:  call.Name,
					Error: planner.NewToolError("boom"),
				}, nil
			}},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"fail": newAnyJSONSpec("fail", "svc.tools"),
		},
		Bus:     noopHooks{},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Error: "boom"}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("fail")}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
		Policy:              RunPolicy{MaxConsecutiveFailedToolCalls: 1},
	}, input, base, initial, nil, model.TokenUsage{}, initialCaps(RunPolicy{MaxConsecutiveFailedToolCalls: 1}), time.Time{}, 2, "", nil, nil, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "consecutive failed tool call cap exceeded")
}

func TestStartRunForwardsWorkflowOptions(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine: eng,
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {ID: "service.agent", Workflow: engine.WorkflowDefinition{Name: "service.workflow", TaskQueue: "defaultq"}},
		},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	in := RunInput{
		RunID:     "run-x",
		SessionID: "sess-1",
		WorkflowOptions: &WorkflowOptions{
			TaskQueue:        "customq",
			Memo:             map[string]any{"k": "v"},
			SearchAttributes: map[string]any{"sa": "x"},
			RetryPolicy:      api.RetryPolicy{MaxAttempts: 5, InitialInterval: 5 * time.Second, BackoffCoefficient: 1.5},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := client.Start(context.Background(), in.SessionID, nil, WithRunID(in.RunID), WithWorkflowOptions(in.WorkflowOptions))
	require.NoError(t, err)
	require.Equal(t, "customq", eng.last.TaskQueue)
	require.Equal(t, in.RunID, eng.last.ID)
	require.Equal(t, in.WorkflowOptions.Memo, eng.last.Memo)
	require.Equal(t, in.WorkflowOptions.SearchAttributes, eng.last.SearchAttributes)
	require.Equal(t, 5, eng.last.RetryPolicy.MaxAttempts)
	require.Equal(t, 5*time.Second, eng.last.RetryPolicy.InitialInterval)
	require.InEpsilon(t, 1.5, eng.last.RetryPolicy.BackoffCoefficient, 1e-9)
}

func TestRegisterAgentAfterFirstRunIsRejected(t *testing.T) {
	t.Parallel()
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:   eng,
		logger:   telemetry.NoopLogger{},
		metrics:  telemetry.NoopMetrics{},
		tracer:   telemetry.NoopTracer{},
		agents:   make(map[agent.Ident]AgentRegistration),
		toolsets: make(map[string]ToolsetRegistration),
	}
	// Register initial agent so we can start a run
	err := rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:      "service.agent",
		Planner: &stubPlanner{},
		Workflow: engine.WorkflowDefinition{
			Name:      "service.workflow",
			TaskQueue: "q",
			Handler: func(wfctx engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
				return &RunOutput{AgentID: "service.agent", RunID: "r1"}, nil
			},
		},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	})
	require.NoError(t, err)

	// First run closes registration
	_, err = rt.MustClient(agent.Ident("service.agent")).Start(context.Background(), "sess-1", nil)
	require.NoError(t, err)

	// Registering a new agent afterwards is rejected
	err = rt.RegisterAgent(context.Background(), AgentRegistration{
		ID:      "service.other",
		Planner: &stubPlanner{},
		Workflow: engine.WorkflowDefinition{
			Name:      "service.other.workflow",
			TaskQueue: "q",
			Handler:   func(wfctx engine.WorkflowContext, input *RunInput) (*RunOutput, error) { return &RunOutput{}, nil },
		},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	})
	require.ErrorIs(t, err, ErrRegistrationClosed)
}

func TestTimeBudgetExceeded(t *testing.T) {
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name: call.Name,
			}, nil
		}}},
		toolSpecs: map[tools.Ident]tools.ToolSpec{"tool": newAnyJSONSpec("tool", "svc.ts")},
		Bus:       noopHooks{},
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
	}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("tool")}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, model.TokenUsage{}, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, wfCtx.Now().Add(-time.Second), 2, "", nil, nil, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "time budget exceeded")
}

func TestOverridePolicy_AppliesToNewRuns_MaxToolCalls(t *testing.T) {
	agentID := agent.Ident("svc.agent")
	rt := &Runtime{
		agents: map[agent.Ident]AgentRegistration{
			agentID: {
				ID:     agentID,
				Policy: RunPolicy{MaxToolCalls: 5},
			},
		},
	}

	// Override policy to allow only 1 tool call.
	require.NoError(t, rt.OverridePolicy(agentID, RunPolicy{MaxToolCalls: 1}))

	reg := rt.agents[agentID]
	require.Equal(t, 1, reg.Policy.MaxToolCalls)

	// New runs should see the overridden caps when initializing caps state.
	caps := initialCaps(reg.Policy)
	require.Equal(t, 1, caps.MaxToolCalls)
	require.Equal(t, 1, caps.RemainingToolCalls)
}

func TestConvertRunOutputToToolResult(t *testing.T) {
	t.Run("aggregates_telemetry_without_error", func(t *testing.T) {
		out := RunOutput{
			Final: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "final"}}},
			ToolEvents: []*planner.ToolResult{
				{Telemetry: &telemetry.ToolTelemetry{TokensUsed: 10, DurationMs: 100, Model: "m1"}},
				{Telemetry: &telemetry.ToolTelemetry{TokensUsed: 5, DurationMs: 50, Model: "m1"}},
			},
		}
		tr := ConvertRunOutputToToolResult("parent.tool", &out)
		require.Nil(t, tr.Error)
		require.NotNil(t, tr.Telemetry)
		require.Equal(t, 15, tr.Telemetry.TokensUsed)
		require.Equal(t, int64(150), tr.Telemetry.DurationMs)
		require.Equal(t, "m1", tr.Telemetry.Model)
		require.Equal(t, "final", tr.Result)
	})
	t.Run("propagates_error_when_all_nested_fail", func(t *testing.T) {
		out := RunOutput{
			Final: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "final"}}},
			ToolEvents: []*planner.ToolResult{
				{Error: planner.NewToolError("e1")},
				{Error: planner.NewToolError("e2")},
			},
		}
		tr := ConvertRunOutputToToolResult("parent.tool", &out)
		require.Error(t, tr.Error)
	})
}

func TestAgentAsToolNestedUpdates(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
	}

	// Register nested tools toolset used by nested agent
	rt.toolsets = map[string]ToolsetRegistration{
		"nested.tools": {
			Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
				return &planner.ToolResult{
					Name:   call.Name,
					Result: map[string]string{"ok": "true"},
				}, nil
			},
		},
	}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		"child1": newAnyJSONSpec("child1", "nested.tools"),
		"child2": newAnyJSONSpec("child2", "nested.tools"),
		"child3": newAnyJSONSpec("child3", "nested.tools"),
	}

	// Register nested agent (planner + activity names)
	nestedReg := AgentRegistration{
		ID:                  "nested.agent",
		Planner:             &nestedPlannerStub{},
		PlanActivityName:    "nested.plan",
		ResumeActivityName:  "nested.resume",
		ExecuteToolActivity: "nested.execute",
		Policy:              RunPolicy{MaxToolCalls: 10},
	}
	plannerRoutes := map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
		"nested.plan": func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
			return rt.PlanStartActivity(ctx, input)
		},
		"nested.resume": func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
			return rt.PlanResumeActivity(ctx, input)
		},
		//nolint:unparam // error return is part of map type signature; test stub always succeeds
		"resume": func(_ context.Context, _ *PlanActivityInput) (*PlanActivityOutput, error) {
			return &PlanActivityOutput{
				Result: &planner.PlanResult{
					FinalResponse: &planner.FinalResponse{
						Message: &model.Message{
							Role: "assistant",
							Parts: []model.Part{
								model.TextPart{
									Text: "done",
								},
							},
						},
					},
				},
				Transcript: nil,
			}, nil
		},
	}
	toolRoutes := map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
		"nested.execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
			return rt.ExecuteToolActivity(ctx, input)
		},
		"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
			return rt.ExecuteToolActivity(ctx, input)
		},
	}
	wfCtx := &routeWorkflowContext{
		ctx:           context.Background(),
		runID:         "run-parent",
		plannerRoutes: plannerRoutes,
		toolRoutes:    toolRoutes,
		hookRuntime:   rt,
		childRuntime:  rt,
	}

	// Parent agent-tools toolset that invokes nested agent inline
	agentTools := ToolsetRegistration{
		Name: "svc.agenttools",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if call == nil {
				return nil, fmt.Errorf("tool request is nil")
			}
			wf := engine.WorkflowContextFromContext(ctx)
			if wf == nil {
				wf = wfCtx
			}
			msgs := []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "go"}}}}
			nestedCtx := run.Context{
				RunID:            NestedRunIDForToolCall(call.RunID, call.Name, call.ToolCallID),
				ParentToolCallID: call.ToolCallID,
				ParentRunID:      call.RunID,
				ParentAgentID:    call.AgentID,
				SessionID:        call.SessionID,
				TurnID:           call.TurnID,
			}
			// Inject nested agent registration into runtime for lookup
			rt.mu.Lock()
			rt.agents = map[agent.Ident]AgentRegistration{"nested.agent": nestedReg}
			rt.mu.Unlock()
			outPtr, err := rt.ExecuteAgentChildWithRoute(wf, AgentRoute{
				ID:               "nested.agent",
				WorkflowName:     "nested.workflow",
				DefaultTaskQueue: "q",
			}, msgs, nestedCtx)
			if err != nil {
				return nil, err
			}
			if outPtr == nil {
				return nil, fmt.Errorf("nil nested output")
			}
			result := ConvertRunOutputToToolResult(call.Name, outPtr)
			return &result, nil
		},
	}
	// Register parent toolset
	rt.mu.Lock()
	rt.toolsets[agentTools.Name] = agentTools
	rt.toolSpecs["invoke"] = newAnyJSONSpec("invoke", "svc.agenttools")
	rt.mu.Unlock()

	// Parent run requests a single agent-tool invocation
	parentInput := &RunInput{AgentID: "parent.agent", RunID: "run-parent", TurnID: "turn-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: parentInput.RunID, TurnID: parentInput.TurnID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: parentInput.AgentID, runID: parentInput.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("invoke")}}}

	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  parentInput.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, parentInput, base, initial, nil, model.TokenUsage{}, policy.CapsState{MaxToolCalls: 3, RemainingToolCalls: 3}, time.Time{}, 2, parentInput.TurnID, nil, nil, 0)
	require.NoError(t, err)

	// Assert ToolCallUpdatedEvent emitted twice with counts 2 then 3 referencing parent tool call id
	var updates []*hooks.ToolCallUpdatedEvent
	for _, evt := range recorder.events {
		if u, ok := evt.(*hooks.ToolCallUpdatedEvent); ok {
			updates = append(updates, u)
		}
	}
	require.GreaterOrEqual(t, len(updates), 2)
	require.Equal(t, 2, updates[0].ExpectedChildrenTotal)
	require.Equal(t, 3, updates[1].ExpectedChildrenTotal)
}

func TestValidateAgentToolCoverage(t *testing.T) {
	ids := []tools.Ident{"a", "b"}
	// Missing both: allowed (defaults will be used)
	err := ValidateAgentToolCoverage(nil, nil, ids)
	require.NoError(t, err)

	// Duplicate for A
	err = ValidateAgentToolCoverage(
		map[tools.Ident]string{"a": "x"},
		map[tools.Ident]*template.Template{"a": template.Must(template.New("t").Parse("{{.}}"))},
		ids,
	)
	require.Error(t, err)

	// OK: A text, B template
	err = ValidateAgentToolCoverage(
		map[tools.Ident]string{"a": "x"},
		map[tools.Ident]*template.Template{"b": template.Must(template.New("t").Parse("{{.}}"))},
		ids,
	)
	require.NoError(t, err)
}

func TestChildTrackerLifecycle(t *testing.T) {
	tracker := newChildTracker("parent-1")

	require.True(t, tracker.registerDiscovered([]string{"child-1", "child-2"}))
	require.Equal(t, 2, tracker.currentTotal())
	require.True(t, tracker.needsUpdate())

	tracker.markUpdated()
	require.False(t, tracker.needsUpdate())

	require.False(t, tracker.registerDiscovered([]string{"child-2"})) // duplicate ignored
	require.True(t, tracker.registerDiscovered([]string{"child-3"}))
	require.Equal(t, 3, tracker.currentTotal())
	require.True(t, tracker.needsUpdate())
}

func TestExecuteToolCallsPublishesChildUpdates(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.export": {
				Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
					return &planner.ToolResult{
						Name: call.Name,
					}, nil
				},
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("child1"): newAnyJSONSpec("child1", "svc.export"),
			tools.Ident("child2"): newAnyJSONSpec("child2", "svc.export"),
		},
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
	}
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{Payload: []byte("null")},
	}
	tracker := newChildTracker("parent-123")
	calls := []planner.ToolRequest{
		{Name: tools.Ident("child1")},
		{Name: tools.Ident("child2")},
	}
	childCtx := &run.Context{
		ParentRunID:      "run-parent",
		ParentAgentID:    "agent-parent",
		ParentToolCallID: "parent-123",
	}
	_, _, err := rt.executeToolCalls(wfCtx, "execute", engine.ActivityOptions{}, "run-1", "agent-1", childCtx, calls, 0, "turn-1", tracker, time.Time{})
	require.NoError(t, err)

	var update *hooks.ToolCallUpdatedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolCallUpdatedEvent); ok {
			update = e
			break
		}
	}
	require.NotNil(t, update)
	require.Equal(t, "parent-123", update.ToolCallID)
	require.Equal(t, 2, update.ExpectedChildrenTotal)
}

func TestRuntimePublishesPolicyDecision(t *testing.T) {
	bus := hooks.NewBus()
	decision := policy.Decision{
		AllowedTools: []tools.Ident{tools.Ident("search")},
		Caps: policy.CapsState{
			MaxToolCalls:       5,
			RemainingToolCalls: 5,
		},
		Labels: map[string]string{
			"policy_engine": "basic",
		},
		Metadata: map[string]any{
			"engine": "basic",
		},
	}
	rt := &Runtime{
		Policy: &stubPolicyEngine{decision: decision},
		Bus:    bus,
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {
				Metadata: policy.ToolMetadata{
					ID:    "search",
					Title: "Search",
				},
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"search": newAnyJSONSpec("search", "svc.tools"),
		},
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		models:        make(map[string]model.Client),
	}

	var policyEvent *hooks.PolicyDecisionEvent
	sub, err := bus.Register(hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
		if e, ok := evt.(*hooks.PolicyDecisionEvent); ok {
			policyEvent = e
		}
		return nil
	}))
	require.NoError(t, err)
	defer func() {
		if err := sub.Close(); err != nil {
			t.Logf("subscriber close error: %v", err)
		}
	}()

	input := RunInput{
		AgentID:   "svc.agent",
		RunID:     "run-123",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Labels: map[string]string{
			"tenant": "acme",
		},
	}

	base := &planner.PlanInput{
		Messages: []*model.Message{
			{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}},
		},
		RunContext: run.Context{
			RunID:     input.RunID,
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Labels:    cloneLabels(input.Labels),
		},
		Agent: newAgentContext(agentContextOptions{
			runtime: rt,
			agentID: input.AgentID,
			runID:   input.RunID,
		}),
	}

	wfCtx := &testWorkflowContext{
		ctx:           context.Background(),
		hookRuntime:   rt,
		asyncResult:   ToolOutput{Payload: []byte("null")},
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}

	err = rt.publishHook(
		wfCtx.Context(),
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, base.RunContext, input),
		input.TurnID,
	)
	require.NoError(t, err)

	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{
				Name:    tools.Ident("search"),
				Payload: json.RawMessage(`{"query":"status"}`),
			},
		},
	}
	caps := policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5}

	_, err = rt.runLoop(
		wfCtx,
		AgentRegistration{
			ID:                  input.AgentID,
			Planner:             &stubPlanner{},
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		&input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		caps,
		time.Time{},
		2,
		input.TurnID,
		nil,
		nil,
		0,
	)
	require.NoError(t, err)

	require.NotNil(t, policyEvent)
	require.Equal(t, hooks.PolicyDecision, policyEvent.Type())
	require.Equal(t, []tools.Ident{tools.Ident("search")}, policyEvent.AllowedTools)
	require.Equal(t, decision.Metadata, policyEvent.Metadata)
	require.Equal(t, decision.Caps, policyEvent.Caps)
	require.Equal(t, decision.Labels, policyEvent.Labels)
}
