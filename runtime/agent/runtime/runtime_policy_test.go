package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
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

func restrictedFinalPlanResult(text string) *PlanResult {
	return &PlanResult{
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
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
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
		planResult: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  "assistant",
					Parts: []model.Part{model.TextPart{Text: "done"}},
				},
			},
		},
		hasPlanResult:   true,
		recoveryCatalog: &RecoveryCatalog{Tools: []tools.Ident{allowedSpec.Name}},
	}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &PlanResult{ToolCalls: []ToolCall{
		{ToolCallID: "allowed-call", Name: tools.Ident("allowed"), Payload: rawjson.Message(`{}`)},
		{ToolCallID: "blocked-call", Name: tools.Ident("blocked"), Payload: rawjson.Message(`{}`)},
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
	call := ToolCall{
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
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "read-call",
		Name:       toolSpec.Name,
		Payload:    rawjson.Message(`{}`),
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

func TestToolCapDeniedCallHydratesFromCanonicalRunLog(t *testing.T) {
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
	initial := &PlanResult{ToolCalls: []ToolCall{{
		Name:       toolSpec.Name,
		ToolCallID: "call-cap-denied",
		Payload:    rawjson.Message(`{"q":"x"}`),
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

	// Finalization plan activities rehydrate tool outputs from the canonical
	// run log by tool_call_id; before the fix this failed with "missing
	// canonical tool history in run log".
	outputs, err := rt.loadPlannerToolOutputs(context.Background(), []*api.ToolOutputRef{{CallRunID: input.RunID, ResultRunID: input.RunID, ToolCallID: "call-cap-denied"}})
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, toolSpec.Name, outputs[0].Name)
	require.NotNil(t, outputs[0].Failure)
	require.Contains(t, outputs[0].Failure.Error.Message, "tool-call cap was exhausted")
	require.JSONEq(t, `{"q":"x"}`, string(outputs[0].Payload))
}

func TestRestrictedRunRecoveryCapFinalizes(t *testing.T) {
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
			Failure: testToolFailure(planner.FailureInvalidCall, planner.RecoveryReplan, "invalid arguments"),
		},
		planResult:    restrictedFinalPlanResult("finalized after recovery cap"),
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
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "read-call",
		Name:       toolSpec.Name,
		Payload:    rawjson.Message(`{}`),
	}}}

	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{
		MaxToolCalls:           5,
		RemainingToolCalls:     5,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 0,
	}, time.Time{}, time.Time{}, "turn-1", nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.NotNil(t, wfCtx.lastPlannerCall.Input.Finalize)
	require.Equal(t, planner.TerminationReasonRecoveryCap, wfCtx.lastPlannerCall.Input.Finalize.Reason)
}

func TestRestrictedUnknownToolFailsBeforeExecution(t *testing.T) {
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
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "missing-call",
		Name:       "svc.tools.missing",
		Payload:    rawjson.Message(`{}`),
	}}}

	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, policy.CapsState{
		MaxToolCalls:           5,
		RemainingToolCalls:     5,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	}, time.Time{}, time.Time{}, "turn-1", nil)
	require.Error(t, err)
	assert.Nil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, err, `planner called unregistered tool "svc.tools.missing"`)
	assert.Empty(t, wfCtx.lastPlannerCall.Name)
}

func TestApplyPerRunOverridesRejectsCallsExcludedByAnyTagClause(t *testing.T) {
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
	allowed, err := rt.applyPerRunOverrides(
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
		[]ToolCall{{ToolCallID: "visible-call", Name: "visible"}},
	)
	require.NoError(t, err)
	require.Equal(t, []ToolCall{{ToolCallID: "visible-call", Name: "visible"}}, allowed)

	for _, call := range []ToolCall{
		{ToolCallID: "missing-call", Name: "missing"},
		{ToolCallID: "denied-call", Name: "denied"},
	} {
		_, err := rt.applyPerRunOverrides(
			context.Background(),
			&RunInput{Policy: &PolicyOverrides{TagClauses: []TagPolicyClause{
				{AllowedAny: []string{"system"}},
				{AllowedAny: []string{"profile"}},
				{DeniedAny: []string{"blocked"}},
			}}},
			[]ToolCall{call},
		)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
	}
}

func TestTagPolicyAllowsUsesRuntimeClauseSemantics(t *testing.T) {
	t.Parallel()

	clauses := []TagPolicyClause{
		{AllowedAny: []string{"system", "profile"}},
		{DeniedAny: []string{"blocked"}},
	}
	assert.True(t, TagPolicyAllows(clauses, []string{"profile"}))
	assert.False(t, TagPolicyAllows(clauses, []string{"other"}))
	assert.False(t, TagPolicyAllows(clauses, []string{"system", "blocked"}))
}

func TestValidateRunPolicyRejectsUnknownMissingFieldsAction(t *testing.T) {
	t.Parallel()

	for _, action := range []MissingFieldsAction{
		"",
		MissingFieldsFinalize,
		MissingFieldsAwaitClarification,
		MissingFieldsResume,
	} {
		require.NoError(t, validateRunPolicy(RunPolicy{OnMissingFields: action}))
	}

	err := validateRunPolicy(RunPolicy{OnMissingFields: "retry"})
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), `unknown missing-fields action "retry"`)
}

func TestFilterToolCallsKeepsToolUnavailable(t *testing.T) {
	filtered := filterToolCalls(
		[]ToolCall{
			{ToolCallID: "allowed-call", Name: "allowed"},
			{ToolCallID: "unavailable-call", Name: tools.ToolUnavailable},
			{ToolCallID: "blocked-call", Name: "blocked"},
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
	contract := definitions[0].Input.Contract()
	require.JSONEq(t, `{"type":"object","properties":{"q":{"type":"string"}}}`, string(contract.Schema))
	require.JSONEq(t, `{"type":"object"}`, string(contract.SchemaWithoutRootExample))
	require.JSONEq(t, `{"q":"status"}`, string(contract.ExampleJSON))
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
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
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
	}, rt.toolMetadata([]ToolCall{{ToolCallID: "search-call", Name: spec.Name}}))
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
