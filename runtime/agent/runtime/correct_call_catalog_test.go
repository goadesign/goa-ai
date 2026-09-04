// This file verifies that the existing resume activity derives the exact tools
// needed to correct saved failures without widening ordinary agent catalogs.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCorrectCallCatalogDerivesOrderedUniqueNames(t *testing.T) {
	first := tools.Ident("catalog.first")
	second := tools.Ident("catalog.second")
	outputs := []*planner.ToolOutput{
		recoveryOutput(first, "call-1", planner.RecoveryCorrectCall),
		recoveryOutput(first, "call-2", planner.RecoveryCorrectCall),
		recoveryOutput("catalog.replan", "call-3", planner.RecoveryReplan),
		recoveryOutput(second, "call-4", planner.RecoveryCorrectCall),
	}

	require.Equal(t, []tools.Ident{first, second}, correctCallCatalog(outputs))
}

func TestRegisterAgentKeepsExistingPlannerActivities(t *testing.T) {
	eng := &stubEngine{}
	rt := New(newTestStore(), WithEngine(eng), WithLogger(telemetry.NoopLogger{}))
	registration := correctionTestRegistration(rt, &stubPlanner{}, nil)

	require.NoError(t, rt.RegisterAgent(t.Context(), registration))
	require.Len(t, eng.registeredPlannerActivityOptions, 2)
	require.Contains(t, eng.registeredPlannerActivityOptions, registration.PlanActivityName)
	require.Contains(t, eng.registeredPlannerActivityOptions, registration.ResumeActivityName)
}

func TestPublicContinuationRejectsToolRemovedFromGeneratedDefinition(t *testing.T) {
	ctx := t.Context()
	store, retired, current := suspendedRetiredToolFixture(t)

	secondRuntime := New(store,
		WithEngine(engineinmem.New()),
		WithLogger(telemetry.NoopLogger{}),
	)
	var executions, resumes int
	require.NoError(t, secondRuntime.RegisterToolset(ToolsetRegistration{
		Name:  "catalog",
		Specs: []tools.ToolSpec{retired, current},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			require.Equal(t, retired.Name, call.Name)
			return successfulToolResult(call), nil
		}),
	}))
	secondPlanner := &stubPlanner{
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if resumes == 1 {
				assertAdvertisedTools(t, input, retired.Name)
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name:    retired.Name,
					Payload: rawjson.Message(`{"query":"corrected"}`),
				}}}, nil
			}
			assertAdvertisedTools(t, input, current.Name)
			return finalPlannerResult("saved lookup completed"), nil
		},
	}
	require.NoError(t, secondRuntime.RegisterAgent(ctx, correctionTestRegistration(
		secondRuntime,
		secondPlanner,
		[]tools.ToolSpec{current},
	)))

	client := secondRuntime.MustClient(agent.Ident("catalog.agent"))
	_, err := client.PrepareContinuation(
		ctx,
		"session-correction",
		"run-before-retirement",
		"run-after-retirement",
		"turn-after-retirement",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID:     "correction-input",
			Answer: "Use the corrected query.",
		}},
		WorkflowOptions{},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, `requires tool "catalog.lookup_retired" removed from the current agent definition`)
	require.Zero(t, executions)
	require.Zero(t, resumes)
}

func TestPublicContinuationRejectsFreshRuntimeWithoutRetiredRegistration(t *testing.T) {
	ctx := t.Context()
	store, retired, current := suspendedRetiredToolFixture(t)
	runtime := New(store,
		WithEngine(engineinmem.New()),
		WithLogger(telemetry.NoopLogger{}),
	)
	require.NoError(t, runtime.RegisterToolset(ToolsetRegistration{
		Name:  "catalog",
		Specs: []tools.ToolSpec{current},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		}),
	}))
	var plannerCalls int
	require.NoError(t, runtime.RegisterAgent(ctx, correctionTestRegistration(
		runtime,
		&stubPlanner{resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			plannerCalls++
			return finalPlannerResult("unexpected"), nil
		}},
		[]tools.ToolSpec{current},
	)))
	schemaHolder := correctionTestRegistration(runtime, &stubPlanner{}, []tools.ToolSpec{retired})
	schemaHolder.Definition = testAgentDefinition(
		"catalog.schema_holder", "catalog.schema_holder.workflow", "test",
		[]tools.ToolSpec{retired}, nil)

	schemaHolder.PlanActivityName = "catalog.schema_holder.plan"
	schemaHolder.ResumeActivityName = "catalog.schema_holder.resume"
	schemaHolder.ExecuteToolActivity = "catalog.schema_holder.execute_tool"
	require.NoError(t, runtime.RegisterAgent(ctx, schemaHolder))

	client := runtime.MustClient(agent.Ident("catalog.agent"))
	_, err := client.PrepareContinuation(
		ctx,
		"session-correction",
		"run-before-retirement",
		"run-without-retired-registration",
		"turn-without-retired-registration",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID:     "correction-input",
			Answer: "Use the corrected query.",
		}},
		WorkflowOptions{},
	)
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, `requires tool "catalog.lookup_retired" removed from the current agent definition`)
	require.Zero(t, plannerCalls)
}

// suspendedRetiredToolFixture creates a current suspension with one typed
// correct-call failure while the failed tool is still executable.
func suspendedRetiredToolFixture(
	t *testing.T,
) (*testStore, tools.ToolSpec, tools.ToolSpec) {
	t.Helper()
	ctx := t.Context()
	retired := newAnyJSONSpec("catalog.lookup_retired")
	current := newAnyJSONSpec("catalog.lookup_current")
	store := newTestStore()
	runtime := New(store,
		WithEngine(engineinmem.New()),
		WithLogger(telemetry.NoopLogger{}),
	)
	require.NoError(t, runtime.RegisterToolset(ToolsetRegistration{
		Name:  "catalog",
		Specs: []tools.ToolSpec{retired},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return invalidCallResult(call), nil
		}),
	}))
	runtime.models["test"] = correctionToolCallModel(t, retired)
	plannerImpl := &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			summary, err := client.Stream(ctx, &model.Request{
				Model:    "test-model",
				Messages: input.Messages,
				Tools:    input.Agent.AdvertisedToolDefinitions(),
			})
			if err != nil {
				return nil, err
			}
			return &planner.PlanResult{ToolCalls: summary.ToolCalls}, nil
		},
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			assertAdvertisedTools(t, input, retired.Name)
			return &planner.PlanResult{Await: planner.NewAwait(
				planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID:            "correction-input",
					Question:      "Provide the corrected input.",
					MissingFields: []string{"query"},
				}),
			)}, nil
		},
	}
	require.NoError(t, runtime.RegisterAgent(ctx, correctionTestRegistration(
		runtime,
		plannerImpl,
		[]tools.ToolSpec{retired},
	)))
	_, err := createSessionForTest(ctx, runtime.Store, "session-correction")
	require.NoError(t, err)
	output, err := runtime.MustClient("catalog.agent").Run(
		ctx,
		"session-correction",
		[]*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Run the saved lookup."}},
		}},
		WithRunID("run-before-retirement"),
		WithTurnID("turn-before-retirement"),
	)
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	require.NotContains(t, string(output.Suspension.Checkpoint), "PendingRecoveryCatalog")
	var checkpoint workflowCheckpoint
	require.NoError(t, json.Unmarshal(output.Suspension.Checkpoint, &checkpoint))
	require.Len(t, checkpoint.State.PendingRecovery, 1)
	require.Nil(t, checkpoint.State.PendingRecoveryCatalog)
	return store, retired, current
}

func TestCorrectCallRecoveryUsesOnlySavedTool(t *testing.T) {
	retired := newAnyJSONSpec("catalog.lookup_retired")
	unrelated := newAnyJSONSpec("catalog.list_retired")
	current := newAnyJSONSpec("catalog.lookup_current")
	var executions, resumes int

	h := newRecoveryHarness(
		t,
		"exact-correct-call",
		[]tools.ToolSpec{retired, unrelated, current},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			assert.Equal(t, retired.Name, call.Name)
			if executions == 1 {
				return invalidCallResult(call), nil
			}
			return successfulToolResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				assertAdvertisedTools(t, input, retired.Name)
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name:    retired.Name,
					Payload: rawjson.Message(`{"query":"corrected"}`),
				}}}, nil
			case 2:
				assertAdvertisedTools(t, input, current.Name)
				return finalPlannerResult("saved work completed"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)
	h.runtime.agentToolSpecs[h.input.AgentID] = []tools.ToolSpec{current}

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name:       retired.Name,
		ToolCallID: "saved-call",
		Payload:    rawjson.Message(`{"query":"invalid"}`),
	}}}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "saved work completed", agentMessageText(out.Final))
	assert.Equal(t, 2, executions)
	assert.Equal(t, 2, resumes)
	require.Len(t, out.ToolEvents, 2)
	assert.NotEqual(t, out.ToolEvents[0].ToolCallID, out.ToolEvents[1].ToolCallID)
	assert.Equal(t, "resume", h.workflow.lastPlannerCall.Name)
}

// correctionToolCallModel returns one validated provider call for the retired
// tool so the public workflow stores genuine model-authored recovery state.
func correctionToolCallModel(t *testing.T, spec tools.ToolSpec) model.Client {
	t.Helper()
	call := model.ToolCall{
		ID:      "provider-call-before-retirement",
		Name:    spec.Name,
		Payload: rawjson.Message(`{"query":"invalid"}`),
	}
	response := &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    call.ID,
				Name:  call.Name.String(),
				Input: call.Payload,
			}},
		}},
		StopReason: "tool_use",
	}
	return mustTestModelClient(stubModelClient{
		stream: func(_ context.Context, request *model.Request) (model.Streamer, error) {
			require.Len(t, request.Tools, 1)
			require.Equal(t, spec.Name.String(), request.Tools[0].Name)
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.ToolCallChunk{ToolCall: call},
					model.StopChunk{Reason: response.StopReason},
				},
				response: response,
			}, nil
		},
	})
}

func TestCorrectCallRecoveryUsesActiveTool(t *testing.T) {
	active := newAnyJSONSpec("catalog.lookup")
	var executions, resumes int
	h := newRecoveryHarness(
		t,
		"active-correct-call",
		[]tools.ToolSpec{active},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			if executions == 1 {
				return invalidCallResult(call), nil
			}
			return successfulToolResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			assertAdvertisedTools(t, input, active.Name)
			if resumes == 1 {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name:    active.Name,
					Payload: rawjson.Message(`{"query":"corrected"}`),
				}}}, nil
			}
			return finalPlannerResult("active work completed"), nil
		},
	)

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name:       active.Name,
		ToolCallID: "active-call",
		Payload:    rawjson.Message(`{"query":"invalid"}`),
	}}}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "active work completed", agentMessageText(out.Final))
	assert.Equal(t, 2, executions)
	assert.Equal(t, 2, resumes)
}

func TestCorrectCallRecoveryRejectsUnregisteredToolBeforePlanner(t *testing.T) {
	tool := newAnyJSONSpec("catalog.lookup")
	var plannerCalls int
	h := newRecoveryHarness(
		t,
		"unregistered-correct-call",
		[]tools.ToolSpec{tool},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return invalidCallResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			plannerCalls++
			return finalPlannerResult("unexpected"), nil
		},
	)
	h.workflow.plannerRoutes["resume"] = func(
		ctx context.Context,
		input *PlanActivityInput,
	) (*PlanActivityOutput, error) {
		h.runtime.mu.Lock()
		delete(h.runtime.toolSpecs, tool.Name)
		h.runtime.mu.Unlock()
		return h.runtime.PlanResumeActivity(ctx, input)
	}

	_, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name:       tool.Name,
		ToolCallID: "saved-call",
		Payload:    rawjson.Message(`{"query":"invalid"}`),
	}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))

	require.ErrorContains(t, err, `correct-call recovery references unregistered tool "catalog.lookup"`)
	require.Zero(t, plannerCalls)
}

func TestAgentRegistrationSpecDoesNotGrantCorrectCallRecovery(t *testing.T) {
	tool := newAnyJSONSpec("catalog.lookup")
	runtime := New(newTestStore(),
		WithEngine(&stubEngine{}),
		WithLogger(telemetry.NoopLogger{}),
	)
	registration := correctionTestRegistration(runtime, &stubPlanner{}, []tools.ToolSpec{tool})
	registration.Definition = testAgentDefinition(
		"catalog.other_agent", "catalog.other_agent.workflow", "test",
		[]tools.ToolSpec{tool}, nil)

	require.NoError(t, runtime.RegisterAgent(t.Context(), registration))
	_, specRegistered := runtime.ToolSpec(tool.Name)
	require.True(t, specRegistered)
	require.NotContains(t, runtime.toolsetNames, tool.Name)

	_, err := runtime.correctCallSpecs([]*planner.ToolOutput{
		recoveryOutput(tool.Name, "saved-call", planner.RecoveryCorrectCall),
	})
	require.ErrorContains(
		t,
		err,
		`correct-call recovery tool "catalog.lookup" has no executable toolset registration`,
	)
}

func TestCorrectCallRecoveryRequiresOwningExecutableRegistration(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Runtime, tools.ToolSpec)
		wantErr string
	}{
		{
			name: "registration removed before resume",
			mutate: func(runtime *Runtime, spec tools.ToolSpec) {
				delete(runtime.toolsets, runtime.toolsetNames[spec.Name])
			},
			wantErr: `correct-call recovery tool "catalog.lookup" has no executable toolset registration`,
		},
		{
			name: "registration no longer admits tool",
			mutate: func(runtime *Runtime, spec tools.ToolSpec) {
				toolsetName := runtime.toolsetNames[spec.Name]
				registration := runtime.toolsets[toolsetName]
				registration.Specs = nil
				runtime.toolsets[toolsetName] = registration
			},
			wantErr: `correct-call recovery toolset "catalog" no longer admits tool "catalog.lookup"`,
		},
		{
			name: "registration contract changed",
			mutate: func(runtime *Runtime, spec tools.ToolSpec) {
				toolsetName := runtime.toolsetNames[spec.Name]
				registration := runtime.toolsets[toolsetName]
				registration.Specs[0].Description = "different generated contract"
				runtime.toolsets[toolsetName] = registration
			},
			wantErr: `correct-call recovery tool "catalog.lookup" has mismatched global and executable registrations`,
		},
		{
			name: "agent tool route is incomplete",
			mutate: func(runtime *Runtime, spec tools.ToolSpec) {
				globalSpec := runtime.toolSpecs[spec.Name]
				globalSpec.IsAgentTool = true
				globalSpec.AgentID = "catalog.provider"
				runtime.toolSpecs[spec.Name] = globalSpec
				toolsetName := runtime.toolsetNames[spec.Name]
				registration := runtime.toolsets[toolsetName]
				registration.Inline = true
				registration.Specs[0] = globalSpec
				registration.AgentTool = nil
				runtime.toolsets[toolsetName] = registration
			},
			wantErr: `agent tool "catalog.lookup" requires agent-tool execution configuration`,
		},
		{
			name: "agent execution configuration lacks generated marker",
			mutate: func(runtime *Runtime, spec tools.ToolSpec) {
				toolsetName := runtime.toolsetNames[spec.Name]
				registration := runtime.toolsets[toolsetName]
				registration.Inline = true
				registration.AgentTool = &AgentToolConfig{
					Definition: testAgentDefinition("catalog.provider", "catalog.provider.workflow", "catalog.provider.queue", nil, nil),
				}
				runtime.toolsets[toolsetName] = registration
			},
			wantErr: `agent toolset "catalog" requires tool "catalog.lookup" to be marked as an agent tool`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := newAnyJSONSpec("catalog.lookup")
			var plannerCalls int
			h := newRecoveryHarness(
				t,
				"executable-"+test.name,
				[]tools.ToolSpec{tool},
				func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					return invalidCallResult(call), nil
				},
				func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
					plannerCalls++
					return finalPlannerResult("unexpected"), nil
				},
			)
			h.workflow.plannerRoutes["resume"] = func(
				ctx context.Context,
				input *PlanActivityInput,
			) (*PlanActivityOutput, error) {
				h.runtime.mu.Lock()
				test.mutate(h.runtime, tool)
				_, specRemains := h.runtime.toolSpecs[tool.Name]
				h.runtime.mu.Unlock()
				require.True(t, specRemains)
				return h.runtime.PlanResumeActivity(ctx, input)
			}

			_, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
				Name:       tool.Name,
				ToolCallID: "saved-call",
				Payload:    rawjson.Message(`{"query":"invalid"}`),
			}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))

			require.ErrorContains(t, err, test.wantErr)
			require.Zero(t, plannerCalls)
		})
	}
}

func TestCorrectCallRecoveryPreservesCurrentPolicyAndAuthorization(t *testing.T) {
	t.Run("run catalog policy denies before planner", func(t *testing.T) {
		retired := newAnyJSONSpec("catalog.lookup_retired")
		current := newAnyJSONSpec("catalog.lookup_current")
		var plannerCalls int
		h := newRecoveryHarness(
			t,
			"run-policy",
			[]tools.ToolSpec{retired, current},
			func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
				return invalidCallResult(call), nil
			},
			func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
				plannerCalls++
				return finalPlannerResult("unexpected"), nil
			},
		)
		h.workflow.plannerRoutes["resume"] = func(
			ctx context.Context,
			input *PlanActivityInput,
		) (*PlanActivityOutput, error) {
			input.Policy = &PolicyOverrides{RestrictToTool: current.Name}
			return h.runtime.PlanResumeActivity(ctx, input)
		}

		_, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
			Name:       retired.Name,
			ToolCallID: "saved-call",
			Payload:    rawjson.Message(`{"query":"invalid"}`),
		}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))

		require.ErrorContains(t, err, "resolved to advertised tools []")
		require.Zero(t, plannerCalls)
	})

	t.Run("runtime policy denies corrected execution", func(t *testing.T) {
		tool := newAnyJSONSpec("catalog.lookup")
		deny := &countingPolicyEngine{decision: policy.Decision{DisableTools: true}}
		var h *recoveryHarness
		var executions int
		h = newRecoveryHarness(
			t,
			"runtime-policy",
			[]tools.ToolSpec{tool},
			func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
				executions++
				return invalidCallResult(call), nil
			},
			func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				assertAdvertisedTools(t, input, tool.Name)
				h.runtime.Policy = deny
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name:    tool.Name,
					Payload: rawjson.Message(`{"query":"corrected"}`),
				}}}, nil
			},
		)

		_, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
			Name:       tool.Name,
			ToolCallID: "saved-call",
			Payload:    rawjson.Message(`{"query":"invalid"}`),
		}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))

		require.ErrorContains(t, err, "tool execution disabled by policy")
		require.Equal(t, 1, deny.calls)
		require.Equal(t, 1, executions)
	})

	t.Run("downstream executor denies corrected call", func(t *testing.T) {
		tool := newAnyJSONSpec("catalog.lookup")
		var executions, resumes int
		h := newRecoveryHarness(
			t,
			"downstream-policy",
			[]tools.ToolSpec{tool},
			func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
				executions++
				if executions == 1 {
					return invalidCallResult(call), nil
				}
				return nil, errors.New("downstream authorization denied")
			},
			func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				resumes++
				if resumes == 1 {
					assertAdvertisedTools(t, input, tool.Name)
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
						Name:    tool.Name,
						Payload: rawjson.Message(`{"query":"corrected"}`),
					}}}, nil
				}
				assert.Empty(t, input.Agent.AdvertisedToolDefinitions())
				require.NotNil(t, input.Finalize)
				return finalPlannerResult("downstream denied the correction"), nil
			},
		)

		out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
			Name:       tool.Name,
			ToolCallID: "saved-call",
			Payload:    rawjson.Message(`{"query":"invalid"}`),
		}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))

		require.NoError(t, err)
		require.Equal(t, "downstream denied the correction", agentMessageText(out.Final))
		require.Equal(t, 2, executions)
		require.Equal(t, 2, resumes)
	})
}

type countingPolicyEngine struct {
	decision policy.Decision
	calls    int
}

func (engine *countingPolicyEngine) Decide(context.Context, policy.Input) (policy.Decision, error) {
	engine.calls++
	return engine.decision, nil
}

// correctionTestRegistration returns the generated-style runtime registration
// used by the public continuation test.
func correctionTestRegistration(
	rt *Runtime,
	plannerImpl planner.Planner,
	specs []tools.ToolSpec,
) AgentRegistration {
	executable := make([]tools.Ident, len(specs))
	for i, spec := range specs {
		executable[i] = spec.Name
	}
	return AgentRegistration{Definition: NewAgentDefinition(
		AgentRoute{ID: "catalog.agent", WorkflowName: "catalog.agent.workflow", DefaultTaskQueue: "test"},
		specs,
		nil,
		nil,
		executable,
		nil,
	),

		WorkflowHandler: (engine.WorkflowDefinition{
			Name:    "catalog.agent.workflow",
			Handler: rt.ExecuteWorkflow,
		}).Handler, Planner: plannerImpl,

		PlanActivityName:    "catalog.agent.plan",
		ResumeActivityName:  "catalog.agent.resume",
		ExecuteToolActivity: "catalog.agent.execute_tool",

		Policy: RunPolicy{
			MaxToolCalls:     4,
			MaxRecoveryTurns: 3,
			OnMissingFields:  MissingFieldsAwaitClarification,
		},
	}
}
