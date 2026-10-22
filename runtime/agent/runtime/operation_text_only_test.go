// Exact-operation tests exercise generated text-only contracts through public
// start and repair methods, provider preparation, and saved execution state.
package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/internal/tooloperation"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	operationPreparationReply struct {
		engine.WorkflowContext
		prepared *api.RunInput
	}
	operationRequestEngine struct {
		engine.Engine
		request         engine.WorkflowStartRequest
		starts, queries int
	}
)

func TestExactAgentOperationTextOnly(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, caller := range []bool{false, true} {
			for _, provider := range []bool{false, true} {
				t.Run(fmt.Sprintf("registered=%t/caller=%t/provider=%t", registered, caller, provider), func(t *testing.T) {
					resolved, invocation := textOnlyOperationFixture(true)
					declaration := resolved.Toolset.Tools[0]
					compiled, err := registrycontract.Resolve(resolved)
					require.NoError(t, err)
					spec := compiled.Specs[genrecords.Read]
					store := newTestStore()
					rt := New(store, WithEngine(engineinmem.New()))
					definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
					plans, preparations := 0, 0
					registration := AgentRegistration{Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
						PlanActivityName: "exact.plan", ResumeActivityName: "exact.resume", ExecuteToolActivity: "exact.execute",
						Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
							plans++
							assert.Equal(t, caller || provider, input.RunContext.TextOnly)
							require.NotNil(t, input.ParentTool)
							if caller || provider {
								assert.NotContains(t, string(input.ParentTool.Payload.Schema), "render_ui")
							}
							nested := input.RunContext
							nested.ParentRunID = nested.RunID
							nested.ParentAgentID = definition.route.ID
							nested.ParentToolCallID = "delegated-call"
							nested.RunID = "child-run"
							child, err := agentChildRunInput(definition, agentChildRequest{policy: &PolicyOverrides{}, runContext: nested})
							require.NoError(t, err)
							assert.Equal(t, caller || provider, child.Policy.TextOnly)
							return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"count":2}`)}}, nil
						}},
					}
					require.NoError(t, rt.RegisterAgent(t.Context(), registration))
					config := &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{userMsg("Read active records.")}), Policy: &PolicyOverrides{TextOnly: provider, MaxToolCalls: 3, TagClauses: []TagPolicyClause{{DeniedAny: []string{"restricted"}}}}}
					require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, _ string, call *ToolCall) (*AgentToolConfiguration, error) {
						preparations++
						assert.Equal(t, caller, call.TextOnly)
						assert.JSONEq(t, `{"query":"active"}`, string(call.Payload))
						return config, nil
					}))
					var opts []RunOption
					if caller {
						opts = []RunOption{WithTextOnly(true)}
					}
					for attempt := range 2 {
						if !caller && attempt == 1 {
							opts = []RunOption{WithTextOnly(false)}
						}
						var handle engine.WorkflowHandle
						if registered {
							handle, err = rt.StartOneShotToolOperation(t.Context(), "registry", resolved, invocation, nil, nil, opts...)
						} else {
							handle, err = rt.StartOneShotOperation(t.Context(), "service-root", declaration, invocation.Arguments, nil, nil, opts...)
						}
						require.NoError(t, err)
						output, err := handle.Wait(t.Context())
						require.NoError(t, err)
						assert.JSONEq(t, `{"count":2}`, string(output.FinalToolResult.Result))
					}
					assert.Equal(t, provider, config.Policy.TextOnly)
					assert.Equal(t, 1, plans)
					assert.Equal(t, 1, preparations)
					run, err := store.LoadRun(t.Context(), "service-root")
					require.NoError(t, err)
					seed, err := store.LoadRunSeed(t.Context(), "service-root", run.SeedEndID)
					require.NoError(t, err)
					accepted, found, err := store.FindRunPreparation(t.Context(), storage.PreparationOperation{Subject: seed.Declaration.Subject, RunID: seed.Declaration.RunID, SessionID: seed.Declaration.SessionID, CommandID: seed.Declaration.CommandID})
					require.NoError(t, err)
					require.True(t, found)
					prepared, err := recoverOperationPreparation(t.Context(), store, accepted)
					require.NoError(t, err)
					assert.Equal(t, 3, prepared.Policy.MaxToolCalls)
					assert.Equal(t, config.Policy.TagClauses, prepared.Policy.TagClauses)
					assert.Equal(t, caller || provider, prepared.Policy.TextOnly)
				})
			}
		}
	}
}

func TestExactOperationRejectsOtherOptionsBeforeApplying(t *testing.T) {
	options := []RunOption{nil, WithRunID("other"), WithTurnID("other"), WithLabels(map[string]string{"owner": "other"}), WithMetadata(map[string]any{"other": true}), WithRenderedPrompts(nil), WithPreparation("command", "attempt"), WithoutPriorReasoning(), WithTaskQueue("other"), WithMemo(map[string]any{"other": true}), WithSearchAttributes(map[string]any{"other": true}), WithTiming(Timing{}), WithProviderRetryBudget(1), WithRunMaxToolCalls(1), WithRunMaxRecoveryTurns(1), WithRunCompletionTool("other.tool"), WithRunTimeBudget(1), WithRunFinalizerGrace(1), WithRestrictToTool("other.tool"), WithTagPolicyClauses(nil), WithLimitTerminalPlans(LimitTerminalPlans{}), runOption(func(*runStart) { t.Fatal("unsupported option was applied") })}
	for index, option := range options {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			resolved, invocation := textOnlyOperationFixture(true)
			backend := &operationRequestEngine{Engine: engineinmem.New()}
			rt := New(newTestStore(), WithEngine(backend), WithToolWorkflowQueue("worker"))
			declaration := resolved.Toolset.Tools[0]
			_, err := rt.StartOperation(t.Context(), "session", "service-root", declaration, invocation.Arguments, nil, nil, WithTextOnly(true), option)
			require.ErrorContains(t, err, "only WithTextOnly")
			_, err = rt.StartOneShotOperation(t.Context(), "service-root", declaration, invocation.Arguments, nil, nil, option)
			require.ErrorContains(t, err, "only WithTextOnly")
			require.ErrorContains(t, rt.EnsureOperationCompletion(t.Context(), "session", "service-root", declaration, invocation.Arguments, nil, nil, option), "only WithTextOnly")
			require.ErrorContains(t, rt.EnsureOneShotOperationCompletion(t.Context(), "service-root", declaration, invocation.Arguments, nil, nil, option), "only WithTextOnly")
			_, err = rt.StartToolOperation(t.Context(), "session", "registry", resolved, invocation, nil, nil, option)
			require.ErrorContains(t, err, "only WithTextOnly")
			_, err = rt.StartOneShotToolOperation(t.Context(), "registry", resolved, invocation, nil, nil, option)
			require.ErrorContains(t, err, "only WithTextOnly")
			require.ErrorContains(t, rt.EnsureToolOperationCompletion(t.Context(), "session", "registry", resolved, invocation, nil, nil, option), "only WithTextOnly")
			require.ErrorContains(t, rt.EnsureOneShotToolOperationCompletion(t.Context(), "registry", resolved, invocation, nil, nil, option), "only WithTextOnly")
			assert.Zero(t, backend.starts)
			assert.Zero(t, backend.queries)
		})
	}
}

func TestExactOperationStartAndRepairHaveSameTextOnlyRequest(t *testing.T) {
	for _, kind := range []string{"direct", "native", "service"} {
		for _, sessionful := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/session=%t", kind, sessionful), func(t *testing.T) {
				resolved, invocation := textOnlyOperationFixture(kind != "service")
				backend := &operationRequestEngine{Engine: engineinmem.New()}
				rt := New(newTestStore(), WithEngine(backend), WithToolWorkflowQueue("worker"))
				declaration := resolved.Toolset.Tools[0]
				memo := map[string]any{"owner": "original"}
				target := engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-a"}
				var err error
				switch {
				case kind == "direct" && sessionful:
					_, err = rt.StartOperation(t.Context(), "session", "service-root", declaration, invocation.Arguments, nil, memo, WithTextOnly(true), WithRootExecutionTarget(target))
				case kind == "direct":
					_, err = rt.StartOneShotOperation(t.Context(), "service-root", declaration, invocation.Arguments, nil, memo, WithTextOnly(true), WithRootExecutionTarget(target))
				case sessionful:
					_, err = rt.StartToolOperation(t.Context(), "session", "registry", resolved, invocation, nil, memo, WithTextOnly(true), WithRootExecutionTarget(target))
				default:
					_, err = rt.StartOneShotToolOperation(t.Context(), "registry", resolved, invocation, nil, memo, WithTextOnly(true), WithRootExecutionTarget(target))
				}
				require.NoError(t, err)
				assert.Equal(t, &target, backend.request.RootExecutionTarget)
				for _, restricted := range []bool{true, false} {
					switch {
					case kind == "direct" && sessionful:
						err = rt.EnsureOperationCompletion(t.Context(), "session", "service-root", declaration, invocation.Arguments, nil, memo, WithTextOnly(restricted), WithRootExecutionTarget(target))
					case kind == "direct":
						err = rt.EnsureOneShotOperationCompletion(t.Context(), "service-root", declaration, invocation.Arguments, nil, memo, WithTextOnly(restricted), WithRootExecutionTarget(target))
					case sessionful:
						err = rt.EnsureToolOperationCompletion(t.Context(), "session", "registry", resolved, invocation, nil, memo, WithTextOnly(restricted), WithRootExecutionTarget(target))
					default:
						err = rt.EnsureOneShotToolOperationCompletion(t.Context(), "registry", resolved, invocation, nil, memo, WithTextOnly(restricted), WithRootExecutionTarget(target))
					}
					if restricted {
						require.ErrorIs(t, err, ErrRunCompletionNotReady)
					} else {
						require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
					}
				}
				assert.Equal(t, 1, backend.starts)
			})
		}
	}
}

func TestExactOperationFalsePreservesUnrestrictedRequest(t *testing.T) {
	for _, kind := range []string{"direct", "native", "service"} {
		for _, sessionID := range []string{"", "session"} {
			t.Run(kind+"/"+sessionID, func(t *testing.T) {
				resolved, invocation := textOnlyOperationFixture(kind != "service")
				backend := &operationRequestEngine{Engine: engineinmem.New()}
				rt := New(newTestStore(), WithEngine(backend), WithToolWorkflowQueue("worker"))
				memo := map[string]any{"owner": "original"}
				var original engine.WorkflowStartRequest
				for index, opts := range [][]RunOption{nil, {WithTextOnly(false)}, {WithTextOnly(true), WithTextOnly(false)}} {
					var request engine.WorkflowStartRequest
					var err error
					if kind == "direct" {
						request, err = prepareOperationRequest(sessionID, "service-root", resolved.Toolset.Tools[0], invocation.Arguments, nil, memo, opts...)
					} else {
						request, err = rt.prepareRegisteredRootRequest(sessionID, "registry", resolved, invocation, nil, memo, opts...)
					}
					require.NoError(t, err)
					assert.Nil(t, request.Input.Policy)
					if index == 0 {
						original = request
						_, err = rt.startWorkflow(t.Context(), original)
						require.NoError(t, err)
					} else {
						before, err := startrecipe.SnapshotRequest(original)
						require.NoError(t, err)
						after, err := startrecipe.SnapshotRequest(request)
						require.NoError(t, err)
						assert.Equal(t, before, after)
					}
				}
				var err error
				switch {
				case kind == "direct" && sessionID != "":
					err = rt.EnsureOperationCompletion(t.Context(), sessionID, "service-root", resolved.Toolset.Tools[0], invocation.Arguments, nil, memo, WithTextOnly(false))
				case kind == "direct":
					err = rt.EnsureOneShotOperationCompletion(t.Context(), "service-root", resolved.Toolset.Tools[0], invocation.Arguments, nil, memo, WithTextOnly(false))
				case sessionID != "":
					err = rt.EnsureToolOperationCompletion(t.Context(), sessionID, "registry", resolved, invocation, nil, memo, WithTextOnly(false))
				default:
					err = rt.EnsureOneShotToolOperationCompletion(t.Context(), "registry", resolved, invocation, nil, memo, WithTextOnly(false))
				}
				require.ErrorIs(t, err, ErrRunCompletionNotReady)
				assert.Equal(t, 1, backend.starts)
			})
		}
	}
}

func TestExactOperationRejectsInteractiveRoots(t *testing.T) {
	for _, kind := range []string{"direct", "native", "service"} {
		for _, reason := range []string{"requires UI", "confirmation", "missing contract"} {
			t.Run(kind+"/"+reason, func(t *testing.T) {
				resolved, invocation := textOnlyOperationFixture(kind != "service")
				declaration := resolved.Toolset.Tools[0]
				switch reason {
				case "requires UI":
					declaration.ConsumerContract.RequiresUI = true
				case "confirmation":
					declaration.ConsumerContract.Confirmation = &genregistry.ToolConfirmation{PromptTemplate: "Read records?", DeniedResultTemplate: `{ "count":0 }`}
				case "missing contract":
					declaration.ConsumerContract.TextOnly = nil
				}
				rt := New(newTestStore(), WithToolWorkflowQueue("worker"))
				for _, restricted := range []bool{false, true} {
					var err error
					if kind == "direct" {
						_, err = prepareOperationRequest("", "service-root", declaration, invocation.Arguments, nil, nil, WithTextOnly(restricted))
					} else {
						_, err = rt.prepareRegisteredRootRequest("", "registry", resolved, invocation, nil, nil, WithTextOnly(restricted))
					}
					if restricted {
						require.ErrorContains(t, err, "operation requires UI or has no generated text-only contract")
					} else {
						require.NoError(t, err)
					}
				}
			})
		}
	}
}

func TestExactOperationPreparationCannotRemoveTextOnly(t *testing.T) {
	resolved, invocation := textOnlyOperationFixture(true)
	request, err := prepareOperationRequest("", "service-root", resolved.Toolset.Tools[0], invocation.Arguments, nil, nil, WithTextOnly(true))
	require.NoError(t, err)
	for _, policy := range []*PolicyOverrides{nil, {}, {TextOnly: true}} {
		prepared := *request.Input
		prepared.SeedEndID = "prepared"
		prepared.Policy = policy
		wf := operationPreparationReply{prepared: &prepared}
		_, _, err := New(newTestStore()).prepareAgentOperation(wf, request.Input)
		if policy != nil && policy.TextOnly {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "removed the accepted text-only restriction")
		}
	}
	request.Input.Policy.MaxToolCalls = 1
	_, err = New(newTestStore()).prepareAgentOperationActivity(t.Context(), request.Input)
	require.ErrorContains(t, err, "only the text-only caller restriction")
}

func TestExactOperationUsesCompleteTextOnlyArguments(t *testing.T) {
	resolved, invocation := textOnlyOperationFixture(true)
	rt := New(newTestStore())
	for _, arguments := range []string{`{"query":"active"}`, `{"query":"active","render_ui":false}`} {
		invocation.Arguments = []byte(arguments)
		direct, err := prepareOperationRequest("", "service-root", resolved.Toolset.Tools[0], invocation.Arguments, nil, nil, WithTextOnly(true))
		require.NoError(t, err)
		require.NoError(t, validateDirectOperationInput(direct.Input))
		directCheckpoint := &workflowCheckpoint{
			AgentID: "generic.agent",
			Context: checkpointRunContext{
				TextOnly:  true,
				Operation: direct.Input.Operation,
				Tool:      direct.Input.Tool,
				ToolArgs:  direct.Input.ToolArgs,
			},
		}
		_, err = operationCheckpointDeclaration(directCheckpoint)
		require.NoError(t, err)
		request, err := rt.prepareRegisteredRootRequest("", "registry", resolved, invocation, nil, nil, WithTextOnly(true))
		require.NoError(t, err)
		checkpoint := &workflowCheckpoint{
			AgentID: "generic.agent",
			Context: checkpointRunContext{
				TextOnly:             true,
				RegisteredInvocation: request.Input.RegisteredInvocation,
				ToolRegistry:         request.Input.ToolRegistry,
				Tool:                 genrecords.Read,
				ToolArgs:             invocation.Arguments,
			},
		}
		_, err = registeredCheckpointDeclaration(checkpoint)
		require.NoError(t, err)
		checkpoint.Context.RegisteredInvocation.Arguments = []byte(`{"query":"active","render_ui":true}`)
		checkpoint.Context.ToolArgs = checkpoint.Context.RegisteredInvocation.Arguments
		_, err = registeredCheckpointDeclaration(checkpoint)
		require.Error(t, err)
		direct.Input.ToolArgs = checkpoint.Context.ToolArgs
		require.Error(t, validateDirectOperationInput(direct.Input))
		directCheckpoint.Context.ToolArgs = checkpoint.Context.ToolArgs
		_, err = operationCheckpointDeclaration(directCheckpoint)
		require.Error(t, err)
	}
	invocation.Arguments = []byte(`{"query":"active","render_ui":true}`)
	_, err := prepareOperationRequest("", "service-root", resolved.Toolset.Tools[0], invocation.Arguments, nil, nil, WithTextOnly(true))
	require.Error(t, err)
	_, err = rt.prepareRegisteredRootRequest("", "registry", resolved, invocation, nil, nil, WithTextOnly(true))
	require.Error(t, err)
}

func TestExactServiceOperationCarriesTextOnlyAndRejectsInput(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		resolved, invocation := textOnlyOperationFixture(false)
		rt := New(newTestStore(), WithToolWorkflowQueue("worker"))
		request, err := rt.prepareRegisteredRootRequest("", "registry", resolved, invocation, nil, nil, WithTextOnly(restricted))
		require.NoError(t, err)
		input, state, err := restoreRegisteredRoot(request.Input)
		require.NoError(t, err)
		assert.Equal(t, restricted, state.call.TextOnly)
		state.queue = "worker"
		providerState := "provider-state"
		pending, err := tooloperation.NewPendingInput(&mcp.InputRequired{RequestState: &providerState})
		require.NoError(t, err)
		state.pending = pending
		suspension, err := registeredRootSuspension(input, state)
		require.NoError(t, err)
		_, err = decodeRegisteredRootCheckpoint(suspension)
		if restricted {
			require.ErrorContains(t, err, "cannot retain host input")
		} else {
			require.NoError(t, err)
		}
	}
}

// textOnlyOperationFixture uses generated contracts for one service operation,
// optionally assigning the same portable contract to a native Agent provider.
func textOnlyOperationFixture(native bool) (*genregistry.ResolvedToolset, *genregistry.RegisteredInvocation) {
	resolved, invocation := registeredRootFixture()
	for _, declaration := range genrecords.ToolSchemas() {
		if declaration.Name != genrecords.Read.String() {
			continue
		}
		resolved.Toolset.Tools = []*genregistry.ToolSchema{declaration}
		if native {
			declaration.ConsumerContract.Kind = registryAgentKind
			declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Configuration: "revision/1", Workflow: "generic.workflow", Queue: "generic.queue"}
			invocation.Target.Target = genregistry.NewCatalogTargetNative(&genregistry.NativeCatalogTarget{Toolset: resolved.Toolset.Name, Tool: declaration.Name})
		} else {
			target, _ := invocation.Target.Target.AsService()
			target.Tool = declaration.Name
		}
		invocation.Arguments = []byte(`{"query":"active"}`)
		return resolved, invocation
	}
	panic("generated read declaration is required")
}

func (wf operationPreparationReply) ExecuteAgentOperationActivity(engine.AgentOperationActivityCall) (*api.AgentOperationActivityResult, error) {
	return &api.AgentOperationActivityResult{Prepared: wf.prepared}, nil
}

func (backend *operationRequestEngine) StartWorkflow(_ context.Context, request engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	backend.request = request
	backend.starts++
	return nil, nil
}

func (backend *operationRequestEngine) QueryRunCompletion(context.Context, string) (engine.RunCompletion, error) {
	backend.queries++
	snapshot, err := startrecipe.SnapshotRequest(backend.request)
	if err != nil {
		return engine.RunCompletion{}, err
	}
	return engine.RunCompletion{Status: engine.RunStatusRunning, StartRequestDigest: snapshot.Digest}, nil
}
