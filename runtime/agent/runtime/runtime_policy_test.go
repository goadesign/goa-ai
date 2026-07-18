package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func canonicalMetadataMap(specs ...tools.ToolSpec) map[tools.Ident]policy.ToolMetadata {
	metas := make(map[tools.Ident]policy.ToolMetadata, len(specs))
	for _, spec := range specs {
		metas[spec.Name] = canonicalToolMetadata(spec, nil)
	}
	return metas
}

func restrictedFinalPlanResult(text string) *planner.PlanResult {
	return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: text}},
			},
		},
	}
}

func TestPolicyAllowlistRewritesDeniedCalls(t *testing.T) {
	recorder := &recordingHooks{}
	allowedSpec := newAnyJSONSpec("allowed", "svc.tools")
	blockedSpec := newAnyJSONSpec("blocked", "svc.tools")
	rt := New()
	rt.Bus = recorder
	rt.Policy = &stubPolicyEngine{decision: policy.Decision{AllowedTools: []tools.Ident{tools.Ident("allowed")}}}
	rt.RunEventStore = runloginmem.New()
	for name, metadata := range canonicalMetadataMap(allowedSpec, blockedSpec) {
		rt.policyToolMetadata[name] = metadata
	}
	rt.toolsets["svc.tools"] = ToolsetRegistration{
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:   call.Name,
				Result: map[string]any{"ok": true},
			}, nil
		}),
	}
	rt.toolSpecs["allowed"] = allowedSpec
	rt.toolSpecs["blocked"] = blockedSpec
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{Payload: []byte("null")},
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  "assistant",
					Parts: []model.Part{model.TextPart{Text: "done"}},
				},
			},
		},
		hasPlanResult: true,
	}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: tools.Ident("allowed"), Payload: rawjson.Message(`{}`)},
		{Name: tools.Ident("blocked"), Payload: rawjson.Message(`{}`)},
	}}
	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5}, time.Time{}, time.Time{}, "turn-1", nil)
	require.NoError(t, err)
	require.Len(t, out.ToolEvents, 2)
	require.Equal(t, tools.Ident("allowed"), out.ToolEvents[0].Name)
	require.Equal(t, tools.ToolUnavailable, out.ToolEvents[1].Name)
	var scheduled []tools.Ident
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolCallScheduledEvent); ok {
			scheduled = append(scheduled, e.ToolName)
		}
	}
	require.Equal(t, []tools.Ident{tools.Ident("allowed"), tools.ToolUnavailable}, scheduled)
}

func TestRewriteToolCallUnavailablePreservesCompiledModelIdentity(t *testing.T) {
	rt := New()
	call := planner.ToolRequest{
		ToolCallID:   "call-1",
		Name:         "service.execute",
		Payload:      []byte(`{"compiled":true}`),
		ModelName:    "planner.resolve",
		ModelPayload: []byte(`{"scope":"all"}`),
	}

	rewritten, err := rt.rewriteToolCallUnavailable(call)

	require.NoError(t, err)
	require.Equal(t, tools.ToolUnavailable, rewritten.Name)
	require.Equal(t, tools.Ident("planner.resolve"), rewritten.ModelName)
	require.JSONEq(t, `{"scope":"all"}`, string(rewritten.ModelPayload))
	var payload toolUnavailablePayload
	require.NoError(t, json.Unmarshal(rewritten.Payload, &payload))
	require.Equal(t, "planner.resolve", payload.RequestedTool)
	require.JSONEq(t, `{"scope":"all"}`, string(payload.RequestedPayload))
}

func TestRestrictedRunToolCapFinalizes(t *testing.T) {
	t.Parallel()

	toolSpec := newAnyJSONSpec("svc.tools.read", "svc.tools")
	rt := &Runtime{
		Bus:           noopHooks{},
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
	}
	seedTestToolSpecs(rt, toolSpec)
	wfCtx := &testWorkflowContext{
		ctx:           context.Background(),
		hookRuntime:   rt,
		planResult:    restrictedFinalPlanResult("finalized after tool cap"),
		hasPlanResult: true,
	}
	input := &RunInput{
		AgentID: "svc.agent",
		RunID:   "run-1",
		Policy:  &PolicyOverrides{RestrictToTool: toolSpec.Name},
	}
	base := &planner.PlanInput{
		RunContext: run.Context{RunID: input.RunID},
		Agent:      newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID}),
	}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    toolSpec.Name,
		Payload: rawjson.Message(`{}`),
	}}}

	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{
		MaxToolCalls:       1,
		RemainingToolCalls: 0,
	}, time.Time{}, time.Time{}, "turn-1", nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Final)
	require.Equal(t, model.ConversationRoleAssistant, out.Final.Role)
	require.NotNil(t, wfCtx.lastPlannerCall.Input.Finalize)
	require.Equal(t, planner.TerminationReasonToolCap, wfCtx.lastPlannerCall.Input.Finalize.Reason)
}

func TestRestrictedRunFailureCapFinalizes(t *testing.T) {
	t.Parallel()

	toolSpec := newAnyJSONSpec("svc.tools.read", "svc.tools")
	rt := &Runtime{
		Bus:           noopHooks{},
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
	}
	seedTestToolSpecs(rt, toolSpec)
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{
			Error: "invalid arguments",
			RetryHint: &planner.RetryHint{
				Reason: planner.RetryReasonInvalidArguments,
				Tool:   toolSpec.Name,
			},
		},
		planResult:    restrictedFinalPlanResult("finalized after failure cap"),
		hasPlanResult: true,
	}
	input := &RunInput{
		AgentID: "svc.agent",
		RunID:   "run-1",
		Policy:  &PolicyOverrides{RestrictToTool: toolSpec.Name},
	}
	base := &planner.PlanInput{
		RunContext: run.Context{RunID: input.RunID},
		Agent:      newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID}),
	}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    toolSpec.Name,
		Payload: rawjson.Message(`{}`),
	}}}

	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{
		MaxToolCalls:                        5,
		RemainingToolCalls:                  5,
		MaxConsecutiveFailedToolCalls:       1,
		RemainingConsecutiveFailedToolCalls: 1,
	}, time.Time{}, time.Time{}, "turn-1", nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.NotNil(t, wfCtx.lastPlannerCall.Input.Finalize)
	require.Equal(t, planner.TerminationReasonFailureCap, wfCtx.lastPlannerCall.Input.Finalize.Reason)
}

func TestRestrictedUnknownToolReachesFailureCap(t *testing.T) {
	t.Parallel()

	rt := New(WithRunEventStore(runloginmem.New()))
	wfCtx := &testWorkflowContext{
		ctx:           context.Background(),
		hookRuntime:   rt,
		asyncResult:   ToolOutput{Payload: []byte("null")},
		planResult:    restrictedFinalPlanResult("finalized after unknown tool"),
		hasPlanResult: true,
	}
	input := &RunInput{
		AgentID: "svc.agent",
		RunID:   "run-1",
		Policy:  &PolicyOverrides{RestrictToTool: "svc.tools.read"},
	}
	base := &planner.PlanInput{
		RunContext: run.Context{RunID: input.RunID},
		Agent:      newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID}),
	}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    "svc.tools.missing",
		Payload: rawjson.Message(`{}`),
	}}}

	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{
		MaxToolCalls:                        5,
		RemainingToolCalls:                  5,
		MaxConsecutiveFailedToolCalls:       1,
		RemainingConsecutiveFailedToolCalls: 1,
	}, time.Time{}, time.Time{}, "turn-1", nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, tools.ToolUnavailable, out.ToolEvents[0].Name)
	require.NotNil(t, out.ToolEvents[0].Error)
	require.NotNil(t, wfCtx.lastPlannerCall.Input.Finalize)
	require.Equal(t, planner.TerminationReasonFailureCap, wfCtx.lastPlannerCall.Input.Finalize.Reason)
}

func TestApplyPerRunOverridesUsesAllTagClauses(t *testing.T) {
	visibleSpec := func() tools.ToolSpec {
		spec := newAnyJSONSpec("visible", "svc.tools")
		spec.Tags = []string{"system", "profile"}
		return spec
	}()
	missingSpec := func() tools.ToolSpec {
		spec := newAnyJSONSpec("missing", "svc.tools")
		spec.Tags = []string{"system"}
		return spec
	}()
	deniedSpec := func() tools.ToolSpec {
		spec := newAnyJSONSpec("denied", "svc.tools")
		spec.Tags = []string{"system", "profile", "blocked"}
		return spec
	}()
	rt := New()
	rt.policyToolMetadata = canonicalMetadataMap(visibleSpec, missingSpec, deniedSpec)
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		"visible": visibleSpec,
		"missing": missingSpec,
		"denied":  deniedSpec,
	}
	rewritten, err := rt.applyPerRunOverrides(
		context.Background(),
		&RunInput{
			Policy: &PolicyOverrides{
				TagClauses: []TagPolicyClause{
					{AllowedAny: []string{"system"}},
					{AllowedAny: []string{"profile"}},
					{DeniedAny: []string{"blocked"}},
				},
			},
		},
		[]planner.ToolRequest{
			{Name: "visible"},
			{Name: "missing"},
			{Name: "denied"},
		},
	)
	require.NoError(t, err)
	require.Len(t, rewritten, 3)
	require.Equal(t, tools.Ident("visible"), rewritten[0].Name)
	require.Equal(t, tools.ToolUnavailable, rewritten[1].Name)
	require.Equal(t, tools.ToolUnavailable, rewritten[2].Name)
	require.Equal(t, tools.Ident("missing"), rewritten[1].ModelName)
	require.Equal(t, tools.Ident("denied"), rewritten[2].ModelName)
	require.JSONEq(t, `{"requested_tool":"missing"}`, string(rewritten[1].Payload))
	require.JSONEq(t, `{"requested_tool":"denied"}`, string(rewritten[2].Payload))

	var hintPayload map[string]any
	require.NoError(t, json.Unmarshal(rewritten[2].Payload.RawMessage(), &hintPayload))
	hint, ok, err := rthints.RenderCallHint(tools.ToolUnavailable, hintPayload)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "Tool not available: denied", hint)
}

func TestRetryRestrictionAllowsBookkeepingTools(t *testing.T) {
	correctionSpec := newAnyJSONSpec("ada.get_time_series", "ada")
	budgetedSpec := newAnyJSONSpec("ada.resolve_sources", "ada")
	progressSpec := newBookkeepingSpec("tasks.progress.update")
	terminalSpec := newTerminalSpec("tasks.progress.complete")
	specs := []tools.ToolSpec{correctionSpec, budgetedSpec, progressSpec, terminalSpec}

	rt := New()
	seedTestToolSpecs(rt, specs...)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{
		"service.agent": specs,
	}
	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTools: []tools.Ident{correctionSpec.Name},
		},
	}

	ctx := newAgentContext(agentContextOptions{
		runtime: rt,
		agentID: "service.agent",
		policy:  compileToolPolicy(input.Policy),
	})
	definitions := ctx.AdvertisedToolDefinitions()
	require.Len(t, definitions, 3)
	require.Equal(t, correctionSpec.Name.String(), definitions[0].Name)
	require.Equal(t, progressSpec.Name.String(), definitions[1].Name)
	require.Equal(t, terminalSpec.Name.String(), definitions[2].Name)

	cases := []struct {
		name      string
		calls     []planner.ToolRequest
		wantNames []tools.Ident
		wantJSON  map[int]string
	}{
		{
			name: "terminal bookkeeping and restricted tool execute while budgeted work rewrites",
			calls: []planner.ToolRequest{
				{Name: correctionSpec.Name},
				{Name: terminalSpec.Name},
				{Name: budgetedSpec.Name},
			},
			wantNames: []tools.Ident{correctionSpec.Name, terminalSpec.Name, tools.ToolUnavailable},
			wantJSON:  map[int]string{2: `{"requested_tool":"ada.resolve_sources"}`},
		},
		{
			name:      "non-terminal bookkeeping executes",
			calls:     []planner.ToolRequest{{Name: progressSpec.Name}},
			wantNames: []tools.Ident{progressSpec.Name},
		},
		{
			name:      "restricted tool executes",
			calls:     []planner.ToolRequest{{Name: correctionSpec.Name}},
			wantNames: []tools.Ident{correctionSpec.Name},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewritten, err := rt.applyPerRunOverrides(context.Background(), input, tc.calls)
			require.NoError(t, err)
			require.Len(t, rewritten, len(tc.wantNames))
			for i, want := range tc.wantNames {
				require.Equal(t, want, rewritten[i].Name)
			}
			for i, want := range tc.wantJSON {
				require.JSONEq(t, want, string(rewritten[i].Payload))
			}
		})
	}
}

func TestFilterToolCallsKeepsToolUnavailable(t *testing.T) {
	filtered := filterToolCalls(
		[]planner.ToolRequest{
			{Name: "allowed"},
			{Name: tools.ToolUnavailable},
			{Name: "blocked"},
		},
		[]tools.Ident{"allowed"},
	)
	require.Len(t, filtered, 2)
	require.Equal(t, tools.Ident("allowed"), filtered[0].Name)
	require.Equal(t, tools.ToolUnavailable, filtered[1].Name)
}

func TestAdvertisedToolDefinitionsHonorCompiledPolicy(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	visible := newAnyJSONSpec("svc.tools.visible", "svc.tools")
	visible.Description = "Visible tool"
	visible.Payload.Schema = tools.RawJSON(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	visible.Payload.SchemaWithoutRootExample = tools.RawJSON(`{"type":"object"}`)
	visible.Payload.ExampleJSON = tools.RawJSON(`{"q":"status"}`)
	visible.Tags = []string{"system", "profile"}
	blocked := newAnyJSONSpec("svc.tools.blocked", "svc.tools")
	blocked.Tags = []string{"system"}
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{
		"service.agent": {visible, blocked},
	}
	ctx := newAgentContext(agentContextOptions{
		runtime: rt,
		agentID: "service.agent",
		policy: compileToolPolicy(&PolicyOverrides{
			TagClauses: []TagPolicyClause{{AllowedAny: []string{"profile"}}},
		}),
	})
	definitions := ctx.AdvertisedToolDefinitions()
	require.Len(t, definitions, 1)
	require.Equal(t, visible.Name.String(), definitions[0].Name)
	require.Equal(t, visible.Description, definitions[0].Description)
	require.JSONEq(t, `{"type":"object","properties":{"q":{"type":"string"}}}`, string(definitions[0].Input.JSONSchema()))
	require.JSONEq(t, `{"type":"object"}`, string(definitions[0].Input.SchemaWithoutRootExample()))
	require.JSONEq(t, `{"q":"status"}`, string(definitions[0].Input.ExampleJSON()))
}

func TestToolMetadataUsesRegisteredCanonicalMetadata(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	spec := newAnyJSONSpec("svc.tools.search", "svc.tools")
	spec.Description = "Spec description should not be re-derived"
	spec.Tags = []string{"spec"}
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc.tools",
		Specs: []tools.ToolSpec{
			spec,
		},
		ToolMetadataLookup: func(name tools.Ident) (policy.ToolMetadata, bool) {
			if name != spec.Name {
				return policy.ToolMetadata{}, false
			}
			return policy.ToolMetadata{
				ID:          name,
				Title:       "Generated Search",
				Description: "Generated metadata wins",
				Tags:        []string{"generated"},
				BudgetClass: policy.ToolBudgetClassBudgeted,
			}, true
		},
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{Name: call.Name}, nil
		}),
	}))

	require.Equal(t, []policy.ToolMetadata{
		{
			ID:          spec.Name,
			Title:       "Generated Search",
			Description: "Generated metadata wins",
			Tags:        []string{"generated"},
			BudgetClass: policy.ToolBudgetClassBudgeted,
		},
	}, rt.toolMetadata([]planner.ToolRequest{{Name: spec.Name}}))
}

func TestPolicyMetadataPanicsWithoutCanonicalMetadata(t *testing.T) {
	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"svc.tools.search": newAnyJSONSpec("svc.tools.search", "svc.tools"),
		},
	}

	require.PanicsWithValue(t, `runtime: missing canonical policy metadata for tool "svc.tools.search"`, func() {
		rt.policyMetadata("svc.tools.search")
	})
}
