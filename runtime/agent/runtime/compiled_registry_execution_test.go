// Package runtime verifies that compact registry calls keep their contracts
// through activity transport, nested checkpoints, and provider preparation.
package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

// TestCompiledRegistryBatchRoundTrip checks the complete activity envelope.
// The large synthetic result schema makes repeated declarations exceed the
// existing transport guard while the same calls with compiled references fit.
func TestCompiledRegistryBatchRoundTrip(t *testing.T) {
	selected := testRegistryPagingResolution(strings.Repeat("a", 64))
	large := []byte(`{"type":"object","description":"` + strings.Repeat("x", 512*1024) + `"}`)
	for _, declaration := range selected.Toolset.Tools {
		declaration.ResultSchema = large
		declaration.ConsumerContract.Result.SchemaWithoutRootExample = large
	}
	for _, compiled := range []bool{false, true} {
		t.Run(fmt.Sprint(compiled), func(t *testing.T) {
			resolve := registrycontract.Resolve
			if compiled {
				resolve = registrycontract.ResolveCompiled
			}
			resolved, err := resolve(selected)
			require.NoError(t, err)
			binding, err := resolved.Select("company", "records.find")
			require.NoError(t, err)
			output := &PlanActivityOutput{PublicationBatchID: "batch", Result: &PlanResult{}}
			for i := 0; i < 4; i++ {
				output.Result.ToolCalls = append(output.Result.ToolCalls, ToolCall{
					Name: "records.find", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("records.agent")}, ToolCallID: fmt.Sprintf("call-%d", i),
					Payload: rawjson.Message(`{"value":1}`), Registry: binding,
				})
			}
			codec := workflowcodec.NewDataConverter()
			encoded, err := codec.ToPayload(output)
			if !compiled {
				require.ErrorContains(t, err, "maximum aggregate size")
				t.Logf("full selection bytes=%d; full batch rejected", len(binding.Resolution))
				return
			}
			require.NoError(t, err)
			var restored PlanActivityOutput
			require.NoError(t, codec.FromPayload(encoded, &restored))
			require.Equal(t, output.Result.ToolCalls, restored.Result.ToolCalls)
			t.Logf("compact selection bytes=%d; encoded four-call batch bytes=%d", len(binding.Resolution), len(encoded.Data))
		})
	}
}

func TestCompiledRegistryCallResumesFromParentChildDefinition(t *testing.T) {
	_, catalog, selected := staticServiceCatalog(t)
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, verifyTestStaticResource, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	standalone := catalog.definition
	childGraph := NewAgentDefinition(standalone.route, standalone.specs, standalone.metadataFor, standalone.requiredLabels, standalone.executableTools, nil, nil).
		WithRegistryTools(standalone.registryTools).
		WithRegistryDeclarations(genrecords.ToolSchema)
	exported := newAnyJSONSpec("children.read")
	exported.IsAgentTool = true
	exported.AgentID = string(childGraph.route.ID)
	parent := NewAgentDefinition(AgentRoute{ID: "parent.agent", WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"}, []tools.ToolSpec{exported}, nil, nil, []tools.Ident{exported.Name}, []AgentDefinition{childGraph}, nil)
	for _, compiled := range []bool{false, true} {
		t.Run(fmt.Sprint(compiled), func(t *testing.T) {
			selection := testDirectSelection(t, selected, genrecords.Read)
			if compiled {
				selection = binding
			}
			child := suspensionContractFixtureWithContext(t, genrecords.Read, string(childGraph.route.ID), "child-run", nil, nil)
			rewriteSuspensionCheckpoint(t, child, func(checkpoint *workflowCheckpoint) {
				checkpoint.Batch.Calls[0].Registry = selection
				checkpoint.Batch.Result.ToolCalls[0].Registry = selection
				checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
				child.RequiredTools = checkpoint.RequiredTools
			})
			suspension := nestedChildSuspensionFixture(t, "parent.agent", "parent-run", exported, child)
			restored, err := decodeWorkflowCheckpoint(suspension, parent)
			require.NoError(t, err)
			savedChild, err := decodeWorkflowCheckpointState(restored.Batch.Records[0].ChildSuspension)
			require.NoError(t, err)
			require.Equal(t, selection, savedChild.Batch.Calls[0].Registry)
		})
	}
}

// TestRegistryProviderOnlyTextOnlyPreparation supplies the provider export
// while the consuming parent runs on a separate worker.
func TestRegistryProviderOnlyTextOnlyPreparation(t *testing.T) {
	for _, compiled := range []bool{false, true} {
		t.Run(fmt.Sprint(compiled), func(t *testing.T) {
			selected := testNativeRegistryResolution("revision/1")
			declaration := selected.Toolset.Tools[0]
			consumer := declaration.ConsumerContract
			consumer.TextOnly = &genregistry.TextOnlyToolContract{
				Description: "Read values.", Search: consumer.Search,
				PayloadSchema: declaration.PayloadSchema, Payload: consumer.Payload,
				ExecutionSchema: declaration.ExecutionPayloadSchema,
			}
			resolve := registrycontract.Resolve
			if compiled {
				resolve = registrycontract.ResolveCompiled
			}
			resolution, err := resolve(selected)
			require.NoError(t, err)
			call := testNativeRegistryCall(t, "revision/1")
			if compiled {
				call.Registry, err = resolution.Select(testRegistryName, call.Name)
				require.NoError(t, err)
			} else {
				call.Registry = testDirectSelection(t, selected, call.Name)
			}
			call.TextOnly = true
			unbound := *declaration
			contract := *consumer
			contract.Agent = nil
			unbound.ConsumerContract = &contract
			child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{resolution.Specs[call.Name]}, nil).WithRegistryDeclarations(testDeclarations([]*genregistry.ToolSchema{&unbound}))
			rt := New(newTestStore())
			rt.agents[child.route.ID] = AgentRegistration{Definition: child}
			seedTestRegistryDeclarations(rt, child)
			require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
				return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the value."}}}})}, nil
			}))
			parentRun := run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
			position := testToolHistory(t, rt, testSubjectAgent(t, call.Subject), parentRun, nil)
			input := &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: parentRun, HistoryEndID: position}
			output, err := rt.prepareAgentChildActivity(t.Context(), input)
			require.NoError(t, err)
			require.NotNil(t, output.Success)
			require.NotNil(t, output.Success.Policy)
			require.True(t, output.Success.Policy.TextOnly)
		})
	}
}
