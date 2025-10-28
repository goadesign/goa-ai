//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/agents/runtime/engine"
	"goa.design/goa-ai/agents/runtime/hooks"
	"goa.design/goa-ai/agents/runtime/interrupt"
	"goa.design/goa-ai/agents/runtime/model"
	"goa.design/goa-ai/agents/runtime/planner"
	"goa.design/goa-ai/agents/runtime/policy"
	"goa.design/goa-ai/agents/runtime/run"
	runinmem "goa.design/goa-ai/agents/runtime/run/inmem"
	"goa.design/goa-ai/agents/runtime/telemetry"
	"goa.design/goa-ai/agents/runtime/tools"
)

// nestedPlannerStub discovers children across iterations: first 2 children,
// then 1, then final.
type nestedPlannerStub struct {
	iter int
}

var _ engine.WorkflowContext = (*testWorkflowContext)(nil)
var _ engine.Future = (*testFuture)(nil)

func (p *nestedPlannerStub) PlanStart(ctx context.Context, in planner.PlanInput) (planner.PlanResult, error) {
	p.iter = 0
	return planner.PlanResult{ToolCalls: []planner.ToolCallRequest{{Name: "nested.tools.child1"}, {Name: "nested.tools.child2"}}}, nil
}
func (p *nestedPlannerStub) PlanResume(ctx context.Context, in planner.PlanResumeInput) (planner.PlanResult, error) {
	p.iter++
	if p.iter == 1 {
		return planner.PlanResult{ToolCalls: []planner.ToolCallRequest{{Name: "nested.tools.child3"}}}, nil
	}
	return planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "nested done"}}}, nil
}
func TestStartRunSetsWorkflowName(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		agents: map[string]AgentRegistration{
			"service.agent": {
				ID: "service.agent",
				Workflow: engine.WorkflowDefinition{
					Name:      "service.workflow",
					TaskQueue: "svc.queue",
				},
			},
		},
	}
	_, err := rt.StartRun(context.Background(), RunInput{
		AgentID: "service.agent",
	})
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
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
			"svc.tools": {Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
				return planner.ToolResult{
					Name:  call.Name,
					Error: planner.NewToolError("boom"),
				}, nil
			}},
		},
		toolSpecs: map[string]tools.ToolSpec{
			"svc.tools.fail": newAnyJSONSpec("svc.tools.fail"),
		},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Error: "boom"}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := planner.PlanResult{ToolCalls: []planner.ToolCallRequest{{Name: "svc.tools.fail"}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
		Policy:              RunPolicy{MaxConsecutiveFailedToolCalls: 1},
	}, input, base, initial, initialCaps(RunPolicy{MaxConsecutiveFailedToolCalls: 1}), time.Time{}, 2, nil, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "consecutive failed tool call cap exceeded")
}

func TestStartRunForwardsWorkflowOptions(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		engine: eng,
		agents: map[string]AgentRegistration{
			"service.agent": {ID: "service.agent", Workflow: engine.WorkflowDefinition{Name: "service.workflow", TaskQueue: "defaultq"}},
		},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	in := RunInput{
		AgentID: "service.agent",
		RunID:   "run-x",
		WorkflowOptions: &WorkflowOptions{
			TaskQueue:        "customq",
			Memo:             map[string]any{"k": "v"},
			SearchAttributes: map[string]any{"sa": "x"},
			RetryPolicy:      engine.RetryPolicy{MaxAttempts: 5, InitialInterval: 5 * time.Second, BackoffCoefficient: 1.5},
		},
	}
	_, err := rt.StartRun(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, "customq", eng.last.TaskQueue)
	require.Equal(t, in.RunID, eng.last.ID)
	require.Equal(t, in.WorkflowOptions.Memo, eng.last.Memo)
	require.Equal(t, in.WorkflowOptions.SearchAttributes, eng.last.SearchAttributes)
	require.Equal(t, 5, eng.last.RetryPolicy.MaxAttempts)
	require.Equal(t, 5*time.Second, eng.last.RetryPolicy.InitialInterval)
	require.Equal(t, 1.5, eng.last.RetryPolicy.BackoffCoefficient)
}

func TestTimeBudgetExceeded(t *testing.T) {
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
			return planner.ToolResult{
				Name: call.Name,
			}, nil
		}}},
		toolSpecs: map[string]tools.ToolSpec{"svc.ts.tool": newAnyJSONSpec("svc.ts.tool")},
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
	}
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := planner.PlanResult{ToolCalls: []planner.ToolCallRequest{{Name: "svc.ts.tool"}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Now().Add(-time.Second), 2, nil, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "time budget exceeded")
}

func TestConvertRunOutputToToolResult(t *testing.T) {
	t.Run("aggregates_telemetry_without_error", func(t *testing.T) {
		out := RunOutput{
			Final: planner.AgentMessage{Content: "final"},
			ToolEvents: []planner.ToolResult{
				{Telemetry: &telemetry.ToolTelemetry{TokensUsed: 10, DurationMs: 100, Model: "m1"}},
				{Telemetry: &telemetry.ToolTelemetry{TokensUsed: 5, DurationMs: 50, Model: "m1"}},
			},
		}
		tr := ConvertRunOutputToToolResult("parent.tool", out)
		require.Nil(t, tr.Error)
		require.NotNil(t, tr.Telemetry)
		require.Equal(t, 15, tr.Telemetry.TokensUsed)
		require.Equal(t, int64(150), tr.Telemetry.DurationMs)
		require.Equal(t, "m1", tr.Telemetry.Model)
		require.Equal(t, "final", tr.Payload)
	})
	t.Run("propagates_error_when_all_nested_fail", func(t *testing.T) {
		out := RunOutput{
			Final: planner.AgentMessage{Content: "final"},
			ToolEvents: []planner.ToolResult{
				{Error: planner.NewToolError("e1")},
				{Error: planner.NewToolError("e2")},
			},
		}
		tr := ConvertRunOutputToToolResult("parent.tool", out)
		require.Error(t, tr.Error)
	})
}

func TestAgentAsToolNestedUpdates(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		hooks:   recorder,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}

	// Register nested tools toolset used by nested agent
	rt.toolsets = map[string]ToolsetRegistration{
		"nested.tools": {
			Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
				return planner.ToolResult{
					Name:    call.Name,
					Payload: map[string]string{"ok": "true"},
				}, nil
			},
		},
	}
	rt.toolSpecs = map[string]tools.ToolSpec{
		"nested.tools.child1": newAnyJSONSpec("nested.tools.child1"),
		"nested.tools.child2": newAnyJSONSpec("nested.tools.child2"),
		"nested.tools.child3": newAnyJSONSpec("nested.tools.child3"),
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
	// Add activity routes to call runtime handlers
	routes := map[string]engine.ActivityDefinition{
		"nested.plan": {Name: "nested.plan", Handler: func(ctx context.Context, input any) (any, error) {
			return rt.PlanStartActivity(ctx, input.(PlanActivityInput))
		}},
		"nested.resume": {Name: "nested.resume", Handler: func(ctx context.Context, input any) (any, error) {
			return rt.PlanResumeActivity(ctx, input.(PlanActivityInput))
		}},
		"nested.execute": {Name: "nested.execute", Handler: func(ctx context.Context, input any) (any, error) {
			return rt.ExecuteToolActivity(ctx, input.(ToolInput))
		}},
		"execute": {Name: "execute", Handler: func(ctx context.Context, input any) (any, error) {
			return rt.ExecuteToolActivity(ctx, input.(ToolInput))
		}},
		"resume": {Name: "resume", Handler: func(context.Context, any) (any, error) {
			return PlanActivityOutput{Result: planner.PlanResult{
				FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "done"}},
			}}, nil
		}},
	}
	wfCtx := &routeWorkflowContext{ctx: context.Background(), runID: "run-parent", routes: routes}

	// Parent agent-tools toolset that invokes nested agent inline
	agentTools := ToolsetRegistration{
		Name: "svc.agenttools",
		Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
			wf := engine.WorkflowContextFromContext(ctx)
			if wf == nil {
				wf = wfCtx
			}
			msgs := []planner.AgentMessage{{Role: "user", Content: "go"}}
			nestedCtx := run.Context{RunID: NestedRunID(call.RunID, call.Name), SessionID: call.SessionID, TurnID: call.TurnID, ParentToolCallID: call.ToolCallID}
			// Inject nested agent registration into runtime for lookup
			rt.mu.Lock()
			rt.agents = map[string]AgentRegistration{"nested.agent": nestedReg}
			rt.mu.Unlock()
			out, err := rt.ExecuteAgentInline(wf, "nested.agent", msgs, nestedCtx)
			if err != nil {
				return planner.ToolResult{}, err
			}
			return ConvertRunOutputToToolResult(call.Name, out), nil
		},
	}
	// Register parent toolset
	rt.mu.Lock()
	rt.toolsets[agentTools.Name] = agentTools
	rt.toolSpecs["svc.agenttools.invoke"] = newAnyJSONSpec("svc.agenttools.invoke")
	rt.mu.Unlock()

	// Parent run requests a single agent-tool invocation
	parentInput := &RunInput{AgentID: "parent.agent", RunID: "run-parent", TurnID: "turn-1"}
	base := planner.PlanInput{RunContext: run.Context{RunID: parentInput.RunID, TurnID: parentInput.TurnID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: parentInput.AgentID, runID: parentInput.RunID})}
	initial := planner.PlanResult{ToolCalls: []planner.ToolCallRequest{{Name: "svc.agenttools.invoke"}}}

	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  parentInput.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, parentInput, base, initial, policy.CapsState{MaxToolCalls: 3, RemainingToolCalls: 3}, time.Time{}, 2, &turnSequencer{turnID: parentInput.TurnID}, nil, nil)
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
	ids := []tools.ID{"svc.ts.a", "svc.ts.b"}
	// Missing both
	err := ValidateAgentToolCoverage(nil, nil, ids)
	require.Error(t, err)

	// Duplicate for A
	err = ValidateAgentToolCoverage(
		map[tools.ID]string{"svc.ts.a": "x"},
		map[tools.ID]*template.Template{"svc.ts.a": template.Must(template.New("t").Parse("{{.}}"))},
		ids,
	)
	require.Error(t, err)

	// OK: A text, B template
	err = ValidateAgentToolCoverage(
		map[tools.ID]string{"svc.ts.a": "x"},
		map[tools.ID]*template.Template{"svc.ts.b": template.Must(template.New("t").Parse("{{.}}"))},
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
				Execute: func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
					return planner.ToolResult{
						Name: call.Name,
					}, nil
				},
			},
		},
		toolSpecs: make(map[string]tools.ToolSpec),
		hooks:     recorder,
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
	}
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		asyncResult: ToolOutput{Payload: []byte("null")},
	}
	tracker := newChildTracker("parent-123")
	calls := []planner.ToolCallRequest{
		{Name: "svc.export.child1"},
		{Name: "svc.export.child2"},
	}
	seq := &turnSequencer{turnID: "turn-1"}
	_, err := rt.executeToolCalls(wfCtx, "execute", "run-1", "agent-1", calls, 0, seq, tracker)
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
	store := runinmem.New()
	bus := hooks.NewBus()
	decision := policy.Decision{
		AllowedTools: []policy.ToolHandle{{ID: "svc.tools.search"}},
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
		policy: &stubPolicyEngine{decision: decision},
		runs:   store,
		hooks:  bus,
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {
				Metadata: policy.ToolMetadata{
					ID:   "svc.tools.search",
					Name: "svc.tools.search",
				},
			},
		},
		toolSpecs: map[string]tools.ToolSpec{
			"svc.tools.search": newAnyJSONSpec("svc.tools.search"),
		},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		models:  make(map[string]model.Client),
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

	base := planner.PlanInput{
		Messages: []planner.AgentMessage{
			{Role: "user", Content: "hello"},
		},
		RunContext: run.Context{
			RunID:  input.RunID,
			Labels: cloneLabels(input.Labels),
		},
		Agent: newAgentContext(agentContextOptions{
			runtime: rt,
			agentID: input.AgentID,
			runID:   input.RunID,
		}),
	}

	wfCtx := &testWorkflowContext{
		ctx:           context.Background(),
		asyncResult:   ToolOutput{Payload: []byte("null")},
		planResult:    planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "done"}}},
		hasPlanResult: true,
	}

	initial := planner.PlanResult{
		ToolCalls: []planner.ToolCallRequest{
			{Name: "svc.tools.search", Payload: map[string]string{"query": "status"}},
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
		caps,
		time.Time{},
		2,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	require.NotNil(t, policyEvent)
	require.Equal(t, hooks.PolicyDecision, policyEvent.Type())
	require.Equal(t, []string{"svc.tools.search"}, policyEvent.AllowedTools)
	require.Equal(t, decision.Metadata, policyEvent.Metadata)
	require.Equal(t, decision.Caps, policyEvent.Caps)
	require.Equal(t, decision.Labels, policyEvent.Labels)

	rec, err := store.Load(context.Background(), input.RunID)
	require.NoError(t, err)
	require.Equal(t, "acme", rec.Labels["tenant"])
	require.Equal(t, "basic", rec.Labels["policy_engine"])
	meta, ok := rec.Metadata[policyDecisionMetadataKey].([]map[string]any)
	require.True(t, ok)
	require.Len(t, meta, 1)
	entry := meta[0]
	require.Equal(t, decision.Caps, entry["caps"])
	require.Equal(t, decision.Metadata, entry["metadata"])
	require.Equal(t, []string{"svc.tools.search"}, entry["allowed_tools"])
	require.NotNil(t, entry["timestamp"])
}
