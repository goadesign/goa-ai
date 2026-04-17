package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
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

func TestPolicyAllowlistTrimsToolExecution(t *testing.T) {
	recorder := &recordingHooks{}
	allowedSpec := newAnyJSONSpec("allowed", "svc.tools")
	blockedSpec := newAnyJSONSpec("blocked", "svc.tools")
	rt := &Runtime{
		Bus:                recorder,
		Policy:             &stubPolicyEngine{decision: policy.Decision{AllowedTools: []tools.Ident{tools.Ident("allowed")}}},
		logger:             telemetry.NoopLogger{},
		metrics:            telemetry.NoopMetrics{},
		tracer:             telemetry.NoopTracer{},
		RunEventStore:      runloginmem.New(),
		policyToolMetadata: canonicalMetadataMap(allowedSpec, blockedSpec),
	}
	rt.toolsets = map[string]ToolsetRegistration{"svc.tools": {
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:   call.Name,
				Result: map[string]any{"ok": true},
			}, nil
		})}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		"allowed": allowedSpec,
		"blocked": blockedSpec,
	}
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
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("allowed")}, {Name: tools.Ident("blocked")}}}
	out, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, model.TokenUsage{}, policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5}, time.Time{}, time.Time{}, 2, "turn-1", nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, tools.Ident("allowed"), out.ToolEvents[0].Name)
	var scheduled []tools.Ident
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolCallScheduledEvent); ok {
			scheduled = append(scheduled, e.ToolName)
		}
	}
	require.Equal(t, []tools.Ident{tools.Ident("allowed")}, scheduled)
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
	rt := &Runtime{
		logger:             telemetry.NoopLogger{},
		policyToolMetadata: canonicalMetadataMap(visibleSpec, missingSpec, deniedSpec),
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"visible": visibleSpec,
			"missing": missingSpec,
			"denied":  deniedSpec,
		},
	}
	rewritten, err := rt.applyPerRunOverrides(
		context.Background(),
		&RunInput{
			Policy: &PolicyOverrides{
				AllowedTags: []string{"system"},
				TagClauses: []TagPolicyClause{
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
	require.JSONEq(t, `{"requested_tool":"missing"}`, string(rewritten[1].Payload))
	require.JSONEq(t, `{"requested_tool":"denied"}`, string(rewritten[2].Payload))
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
	visible.Payload.Schema = []byte(`{"type":"object","properties":{"q":{"type":"string"}}}`)
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
	schema, ok := definitions[0].InputSchema.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "object", schema["type"])
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
