// Revision-owned providers admit independently saved contracts and retain them
// through preparation recovery and result validation without generated exports.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestAgentOperationProviderRegistration(t *testing.T) {
	provider := func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*genregistry.ToolSchema, *AgentToolConfiguration, error) {
		return nil, nil, errors.New("unused")
	}
	resolver := func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		return nil, errors.New("unused")
	}
	for _, first := range []string{"generated", "revision"} {
		t.Run(first, func(t *testing.T) {
			rt := New(newTestStore(), WithEngine(&stubEngine{}))
			if first == "generated" {
				require.NoError(t, rt.RegisterAgentToolResolver("generic.agent", resolver))
			} else {
				require.NoError(t, rt.RegisterAgentOperationProvider("generic.agent", provider))
			}
			require.Error(t, rt.RegisterAgentToolResolver("generic.agent", resolver))
			require.Error(t, rt.RegisterAgentOperationProvider("generic.agent", provider))
			require.NoError(t, rt.Seal(t.Context()))
			require.ErrorIs(t, rt.RegisterAgentOperationProvider("other.agent", provider), ErrRegistrationClosed)
		})
	}
	rt := New(newTestStore())
	require.Error(t, rt.RegisterAgentOperationProvider("", provider))
	require.Error(t, rt.RegisterAgentOperationProvider("generic.agent", nil))
}

func TestAgentOperationProviderDirectExecution(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "typed result", false: "invalid result"}[valid], func(t *testing.T) {
			owned := revisionOperation("revision/1").Toolset.Tools[0]
			selected := revisionOperation("revision/1").Toolset.Tools[0]
			definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
			rt := New(newTestStore(), WithEngine(engineinmem.New()))
			preparations := 0
			require.NoError(t, rt.RegisterAgentOperationProvider(definition.route.ID, func(_ context.Context, origin genregistry.ExecutionInvocation, reference string, call *ToolCall) (*genregistry.ToolSchema, *AgentToolConfiguration, error) {
				preparations++
				root, ok := origin.Invocation.AsRoot()
				require.True(t, ok)
				assert.Equal(t, "revision-run", root.RunID)
				assert.Equal(t, "revision/1", reference)
				assert.Equal(t, tools.Ident(owned.Name), call.Name)
				assert.Nil(t, call.Registry)
				return owned, &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Return the quantity."}}}})}, nil
			}))
			require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
				Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "revision.plan", ResumeActivityName: "revision.resume", ExecuteToolActivity: "revision.execute",
				Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
					require.NotNil(t, input.ParentTool)
					assert.Equal(t, tools.Ident(owned.Name), input.ParentTool.Name)
					result := rawjson.Message(`{"value":7}`)
					if !valid {
						result = rawjson.Message(`{"value":"wrong"}`)
					}
					return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: result}}, nil
				}},
			}))
			handle, err := rt.StartOneShotOperation(t.Context(), "revision-run", selected, rawjson.Message(`{"value":1}`), nil, nil)
			require.NoError(t, err)
			output, err := handle.Wait(t.Context())
			if !valid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, output.FinalToolResult)
				assert.JSONEq(t, `{"value":7}`, string(output.FinalToolResult.Result))
			}
			assert.Equal(t, 1, preparations)
		})
	}
}

func TestAgentOperationProviderRejectsChangedContract(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		for _, variation := range []string{"name", "payload schema", "result schema", "configuration", "workflow", "queue", "executor", "description", "tags", "invalid payload", "missing owned declaration"} {
			t.Run(map[bool]string{false: "direct", true: "delegated"}[delegated]+"/"+variation, func(t *testing.T) {
				resolution := revisionOperation("revision/1")
				selected := resolution.Toolset.Tools[0]
				owned := revisionOperation("revision/1").Toolset.Tools[0]
				payload := rawjson.Message(`{"value":1}`)
				switch variation {
				case "name":
					owned.Name = "reports.other"
				case "payload schema":
					owned.PayloadSchema = []byte(`{"type":"string"}`)
					owned.ExecutionPayloadSchema = owned.PayloadSchema
					owned.ConsumerContract.Payload.SchemaWithoutRootExample = owned.PayloadSchema
				case "result schema":
					owned.ResultSchema = []byte(`{"type":"string"}`)
					owned.ConsumerContract.Result.SchemaWithoutRootExample = owned.ResultSchema
				case "configuration":
					owned.ConsumerContract.Agent.Configuration = "revision/2"
				case "workflow":
					selected.ConsumerContract.Agent.Workflow = "foreign.workflow"
				case "queue":
					selected.ConsumerContract.Agent.Queue = "foreign.queue"
				case "executor":
					selected.ConsumerContract.Agent.Executor = "foreign.agent"
				case "description":
					text := "Different description"
					owned.Description = &text
				case "tags":
					owned.Tags = []string{"different"}
				case "invalid payload":
					payload = rawjson.Message(`{"value":"wrong"}`)
				case "missing owned declaration":
					owned = nil
				}
				rt := New(newTestStore())
				definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
				rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
				calls := 0
				require.NoError(t, rt.RegisterAgentOperationProvider(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*genregistry.ToolSchema, *AgentToolConfiguration, error) {
					calls++
					return owned, &AgentToolConfiguration{History: LiteralAgentHistory(nil)}, nil
				}))
				var err error
				runID := "reject-run"
				if delegated {
					call := revisionOperationCall(t, resolution)
					call.Payload = payload
					testToolHistory(t, rt, testSubjectAgent(t, call.Subject), run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}, nil)
					runID = agentChildRunContext(&call).RunID
					_, err = rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}})
				} else {
					_, err = rt.prepareAgentOperationActivity(t.Context(), revisionOperationInput(selected, runID, payload))
				}
				require.Error(t, err)
				if variation == "workflow" || variation == "queue" || variation == "executor" || variation == "invalid payload" {
					assert.Zero(t, calls)
				} else {
					assert.Equal(t, 1, calls)
					assert.True(t, engine.IsActivityErrorNonRetryable(err))
				}
				_, err = rt.Store.LoadRunSeed(t.Context(), runID, storage.EmptySeedEndID)
				require.ErrorIs(t, err, storage.ErrSeedNotFound)
			})
		}
	}
}

func TestAgentOperationProviderPreparationRecovery(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "delegated"}[delegated], func(t *testing.T) {
			resolution := revisionOperation("revision/1")
			selected := resolution.Toolset.Tools[0]
			owned := revisionOperation("revision/1").Toolset.Tools[0]
			definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
			rt := New(newTestStore())
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			preparations := 0
			require.NoError(t, rt.RegisterAgentOperationProvider(definition.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, reference string, call *ToolCall) (*genregistry.ToolSchema, *AgentToolConfiguration, error) {
				preparations++
				assert.Equal(t, owned.Name, string(call.Name))
				assert.Nil(t, call.Registry)
				if delegated {
					assert.NotEqual(t, call.Name, call.TranscriptName())
				}
				return owned, &AgentToolConfiguration{History: LiteralAgentHistory(nil), Labels: map[string]string{"revision": reference}}, nil
			}))
			call := revisionOperationCall(t, resolution)
			parent := run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
			input := revisionOperationInput(selected, "recover-revision", call.Payload)
			childInput := &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: parent}
			var retained run.Context
			if delegated {
				childInput.HistoryEndID = testToolHistory(t, rt, testSubjectAgent(t, call.Subject), parent, nil)
				first, err := rt.prepareAgentChildActivity(t.Context(), childInput)
				require.NoError(t, err)
				rt.agentOperationProviders[definition.route.ID] = func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*genregistry.ToolSchema, *AgentToolConfiguration, error) {
					t.Fatal("accepted preparation must not reload configuration")
					return nil, nil, nil
				}
				second, err := rt.prepareAgentChildActivity(t.Context(), childInput)
				require.NoError(t, err)
				assert.Equal(t, first, second)
				wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, agentChildOutput: first}
				request, stopped, err := rt.prepareAgentChild(wf, call, "", parent)
				require.NoError(t, err)
				require.Nil(t, stopped)
				retained = request.runContext
				assert.Equal(t, call.Registry, retained.ToolRegistry)
			} else {
				first, err := rt.prepareAgentOperationActivity(t.Context(), input)
				require.NoError(t, err)
				rt.agentOperationProviders[definition.route.ID] = func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*genregistry.ToolSchema, *AgentToolConfiguration, error) {
					t.Fatal("accepted preparation must not reload configuration")
					return nil, nil, nil
				}
				second, err := rt.prepareAgentOperationActivity(t.Context(), input)
				require.NoError(t, err)
				assert.Equal(t, first, second)
				retained = run.Context{Tool: input.Tool, ToolArgs: input.ToolArgs, Operation: input.Operation}
			}
			assert.Equal(t, 1, preparations)
			restored := restoreCheckpointRunContext(checkpointContextFromRun(retained), &api.RunInput{Subject: input.Subject, RunID: "successor"})
			spec, err := selectedRunTool(restored, definition.route.ID, definition)
			require.NoError(t, err)
			_, err = spec.Result.Codec.FromJSON(rawjson.Message(`{"value":8}`))
			require.NoError(t, err)
			_, err = spec.Result.Codec.FromJSON(rawjson.Message(`{"value":"wrong"}`))
			require.Error(t, err)
		})
	}
}

func TestAgentOperationProviderConcurrentRevisions(t *testing.T) {
	rt := New(newTestStore())
	definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	owned := map[string]*genregistry.ToolSchema{
		"revision/1": revisionOperation("revision/1").Toolset.Tools[0],
		"revision/2": revisionOperation("revision/2").Toolset.Tools[0],
	}
	textSchema := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
	owned["revision/2"].ResultSchema = textSchema
	owned["revision/2"].ConsumerContract.Result.SchemaWithoutRootExample = textSchema
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	require.NoError(t, rt.RegisterAgentOperationProvider(definition.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, reference string, _ *ToolCall) (*genregistry.ToolSchema, *AgentToolConfiguration, error) {
		entered <- struct{}{}
		<-release
		return owned[reference], &AgentToolConfiguration{History: LiteralAgentHistory(nil), Labels: map[string]string{"revision": reference}}, nil
	}))
	go func() {
		<-entered
		<-entered
		close(release)
	}()
	for reference, declaration := range owned {
		t.Run(reference, func(t *testing.T) {
			t.Parallel()
			input := revisionOperationInput(declaration, "run-"+reference, rawjson.Message(`{"value":1}`))
			prepared, err := rt.prepareAgentOperationActivity(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, reference, prepared.Prepared.Labels["revision"])
			assert.Equal(t, declaration, prepared.Prepared.Operation)
			spec, err := selectedRunTool(run.Context{Tool: input.Tool, Operation: prepared.Prepared.Operation}, definition.route.ID, definition)
			require.NoError(t, err)
			result := rawjson.Message(`{"value":7}`)
			wrong := rawjson.Message(`{"value":"seven"}`)
			if reference == "revision/2" {
				result, wrong = wrong, result
			}
			_, err = spec.Result.Codec.FromJSON(result)
			require.NoError(t, err)
			_, err = spec.Result.Codec.FromJSON(wrong)
			require.Error(t, err)
		})
	}
}

func TestAgentOperationProviderContinuationRetainsExport(t *testing.T) {
	privateTool := newAnyJSONSpec("internal.lookup")
	definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{privateTool}, nil)
	suspension := suspensionContractFixtureWithContext(t, privateTool.Name, "generic.agent", "original-run", nil, nil)
	operation := revisionOperation("revision/1").Toolset.Tools[0]
	rewriteSuspensionCheckpointAndPublic(t, suspension, func(saved *workflowCheckpoint) {
		saved.Context.Operation = operation
		saved.Context.Tool = tools.Ident(operation.Name)
		saved.Context.ToolArgs = rawjson.Message(`{"value":1}`)
	})
	checkpoint, err := decodeWorkflowCheckpoint(suspension, definition)
	require.NoError(t, err)
	assert.NotContains(t, checkpoint.RequiredTools, tools.Ident(operation.Name))
	restored := restoreCheckpointRunContext(checkpoint.Context, &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("generic.agent"))}, RunID: "continued-run"})
	spec, err := selectedRunTool(restored, definition.route.ID, definition)
	require.NoError(t, err)
	assert.Equal(t, operation, restored.Operation)
	_, err = spec.Result.Codec.FromJSON(rawjson.Message(`{"value":4}`))
	require.NoError(t, err)
	_, err = spec.Result.Codec.FromJSON(rawjson.Message(`{"value":"four"}`))
	require.Error(t, err)
}

// revisionOperation supplies a complete synthetic saved export with no matching
// generated tool; every call returns independent data for owner comparison.
func revisionOperation(reference string) *genregistry.ResolvedToolset {
	resolution := testNativeRegistryResolution(reference)
	resolution.Toolset.Tools[0].Name = "reports.summarize"
	return resolution
}

// revisionOperationCall derives the consumer alias from a registered resource,
// leaving the original export name in the saved provider declaration.
func revisionOperationCall(t *testing.T, resolution *genregistry.ResolvedToolset) ToolCall {
	t.Helper()
	source := &genregistry.CatalogSource{Name: "reporter", Scope: "workspace", Target: genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: resolution.Toolset.Name})}
	resolved, err := registrycontract.ResolveRegistered(testRegistryName, source, resolution)
	require.NoError(t, err)
	var name tools.Ident
	for candidate := range resolved.Specs {
		name = candidate
	}
	binding, err := resolved.Select(testRegistryName, name)
	require.NoError(t, err)
	return ToolCall{Name: name, Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("records.agent"))}, RunID: "parent-run", SessionID: "session-1", TurnID: "turn-1", ToolCallID: "revision-call", Payload: rawjson.Message(`{"value":1}`), Registry: binding}
}

// revisionOperationInput keeps the direct call independent of registry aliases.
func revisionOperationInput(declaration *genregistry.ToolSchema, runID string, payload rawjson.Message) *api.RunInput {
	return &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("generic.agent"))}, RunID: runID, Tool: tools.Ident(declaration.Name), ToolArgs: payload, Operation: declaration}
}
