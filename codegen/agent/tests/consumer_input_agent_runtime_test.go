// These tests execute generated bindings for an exported Agent. The consuming
// reference must retain the provider route while hiding application-owned input.
package tests

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestConsumerInputGeneratedAgentIntegration(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", func() {
		API("delegation", func() {})
		records := Toolset("records", func() {
			Tool("read", "Read records in a selected scope.", func() {
				Args(func() {
					Attribute("scope", String, "The scope selected by the application.")
					Attribute("query", String, "The records requested by the caller.")
					Required("scope", "query")
				})
				Return(String)
			})
		})
		Service("worker", func() {
			Agent("specialist", "Read the selected records.", func() { Export(records) })
		})
		Service("reader", func() {
			Agent("bound", "Delegate record reading.", func() {
				Use(AgentToolset("worker", "specialist", "records"), func() { BindInput("read", "scope") })
			})
		})
	})
	binding := fileContent(t, files, "gen/reader/agents/bound/records/inputbinding/binding.go")
	local, source, found := strings.Cut(binding, "func BindReadSourceInput")
	require.True(t, found)
	assert.Contains(t, local, "original.IsAgentTool = true")
	assert.Contains(t, local, `original.AgentID = "worker.specialist"`)
	assert.NotContains(t, source, "original.IsAgentTool =")
	assert.NotContains(t, source, "original.AgentID =")
	provider := fileContent(t, files, "gen/worker/agents/specialist/exports/records/specs.go")
	assert.NotContains(t, provider, "IsAgentTool")
	assert.NotContains(t, provider, "worker.specialist")
	root := writeGeneratedModuleKeepingGen(t, files)
	writeGeneratedPackageTest(t, root, "bindingintegration/agent_binding_test.go", consumerAgentRuntimeSource)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "-v", "./bindingintegration"))
}

const consumerAgentRuntimeSource = `package bindingintegration

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	genbound "generated.local/gen/reader/agents/bound"
	genbinding "generated.local/gen/reader/agents/bound/records/inputbinding"
	genspecialist "generated.local/gen/worker/agents/specialist"
	genagenttools "generated.local/gen/worker/agents/specialist/agenttools/records"
	genrecords "generated.local/gen/worker/agents/specialist/exports/records"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type bindingPlanner struct {
	t               *testing.T
	child, typed    bool
	starts, resumes atomic.Int32
}

func (p *bindingPlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	p.starts.Add(1)
	if !p.child {
		definitions := input.Agent.AdvertisedToolDefinitions()
		require.Len(p.t, definitions, 1)
		assert.Equal(p.t, "records.read", definitions[0].Name)
		assert.NotContains(p.t, string(definitions[0].Input.Contract().Schema), "scope")
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "records.read", Payload: rawjson.Message("{\"query\":\"active\"}")}}}, nil
	}
	require.NotNil(p.t, input.ParentTool)
	assert.Equal(p.t, "records.read", string(input.ParentTool.Name))
	assert.True(p.t, input.ParentTool.IsAgentTool)
	assert.Equal(p.t, "worker.specialist", input.ParentTool.AgentID)
	assert.NotEmpty(p.t, input.RunContext.ParentRunID)
	assert.NotEmpty(p.t, input.RunContext.ParentToolCallID)
	payload, err := genrecords.ReadPayloadCodec().FromJSON(input.RunContext.ToolArgs)
	require.NoError(p.t, err)
	assert.Equal(p.t, "accepted", payload.Scope)
	assert.Equal(p.t, "active", payload.Query)
	if p.typed {
		return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message("\"child result\"")}}, nil
	}
	return bindingAnswer("child result"), nil
}

func (p *bindingPlanner) PlanResume(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.resumes.Add(1)
	if p.child {
		return nil, fmt.Errorf("child should complete on its first plan")
	}
	require.Len(p.t, input.ToolOutputs, 1)
	output := input.ToolOutputs[0]
	assert.Nil(p.t, output.Failure)
	assert.Equal(p.t, "records.read", string(output.Name))
	assert.JSONEq(p.t, "\"child result\"", string(output.Result))
	assert.JSONEq(p.t, "{\"scope\":\"accepted\",\"query\":\"active\"}", string(output.Payload))
	return bindingAnswer("finished"), nil
}

func bindingAnswer(text string) *planner.PlanResult {
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: text}},
	}}}
}

func TestStaticAgentBindingPreservesRouteAndResults(t *testing.T) {
	for _, typed := range []bool{false, true} {
		for _, textOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("typed=%t/text-only=%t", typed, textOnly), func(t *testing.T) {
				rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
				child := &bindingPlanner{t: t, child: true, typed: typed}
				parent := &bindingPlanner{t: t}
				registration, err := genagenttools.NewRegistration(genspecialist.Definition(), "")
				require.NoError(t, err)
				require.NoError(t, rt.RegisterToolset(registration))
				require.NoError(t, genspecialist.RegisterSpecialistAgent(t.Context(), rt, genspecialist.SpecialistAgentConfig{Planner: child}))
				require.NoError(t, genbound.RegisterBoundAgent(t.Context(), rt, genbound.BoundAgentConfig{
					Planner: parent,
					PrepareInputs: func(_ context.Context, catalog *runtime.RegistryCatalog) error {
						return genbinding.BindReadInput(catalog, "accepted")
					},
				}))
				output, err := rt.MustClient(genbound.AgentID).OneShotRun(t.Context(), []*model.Message{{
					Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "read records"}},
				}}, runtime.WithTextOnly(textOnly))
				require.NoError(t, err)
				assert.Equal(t, "finished", output.Final.Text())
				assert.EqualValues(t, 1, parent.starts.Load())
				assert.EqualValues(t, 1, parent.resumes.Load())
				assert.EqualValues(t, 1, child.starts.Load())
				assert.Zero(t, child.resumes.Load())
				_, err = genbinding.ReadModelInputCodec().FromJSON([]byte("{\"scope\":\"forged\",\"query\":\"active\"}"))
				require.Error(t, err)
				_, err = genrecords.ReadPayloadCodec().FromJSON([]byte("{\"query\":\"active\"}"))
				require.Error(t, err)
			})
		}
	}
}

// selectedNativeSource supplies the complete generated native operation set and
// its owner-selected registration. Resolution never reads a remote catalog.
type selectedNativeSource struct{ selected *genregistry.ResolvedToolset }
type unusedNativePulse struct{ pulse.Client }

func (source selectedNativeSource) Resolve(_ context.Context, catalog *runtime.RegistryCatalog) error {
	return catalog.BindStaticAgent("native", genrecords.ToolSchemas(), source.selected, genrecords.Read)
}

func (source selectedNativeSource) Allows(registry, toolset, _ string) bool {
	return registry == "native" && toolset == source.selected.Toolset.Name
}

func TestSelectedNativeRegistrationPreservesGeneratedInputAndChildResult(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("text-only=%t", textOnly), func(t *testing.T) {
			rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
			child := &bindingPlanner{t: t, child: true, typed: true}
			parent := &bindingPlanner{t: t}
			toolset := genrecords.Toolset()
			toolset.Name = "selected.records"
			toolset.RegisteredAt = "2026-09-17T00:00:00Z"
			for _, operation := range toolset.Tools {
				require.Equal(t, "agent", operation.ConsumerContract.Kind)
				require.Nil(t, operation.ConsumerContract.Agent)
				operation.ConsumerContract.Agent = &genregistry.AgentToolTarget{
					Executor: string(genspecialist.AgentID), Workflow: genspecialist.WorkflowName,
					Queue: genspecialist.DefaultTaskQueue, Configuration: "revision/selected",
				}
			}
			selected := &genregistry.ResolvedToolset{Toolset: toolset, RegistrationToken: strings.Repeat("a", 64)}
			require.NoError(t, rt.RegisterRegistry("native", &genregistry.Client{}, unusedNativePulse{}))
			provider, err := genagenttools.NewRegistration(genspecialist.Definition(), "")
			require.NoError(t, err)
			require.NoError(t, rt.RegisterToolset(provider))
			require.NoError(t, genspecialist.RegisterSpecialistAgent(t.Context(), rt, genspecialist.SpecialistAgentConfig{Planner: child}))
			var resolutions atomic.Int32
			require.NoError(t, rt.RegisterAgentToolResolver(genspecialist.AgentID, func(_ context.Context, _ genregistry.ExecutionInvocation, revision string, call *runtime.ToolCall) (*runtime.AgentToolConfiguration, error) {
				resolutions.Add(1)
				require.Equal(t, "revision/selected", revision)
				input, err := genrecords.ReadPayloadCodec().FromJSON(call.Payload)
				require.NoError(t, err)
				assert.Equal(t, "accepted", input.Scope)
				assert.Equal(t, "active", input.Query)
				return &runtime.AgentToolConfiguration{History: runtime.LiteralAgentHistory(nil)}, nil
			}))
			registration, err := genbound.NewBoundAgentRegistration(rt, genbound.BoundAgentConfig{
				Planner: parent,
				PrepareInputs: func(_ context.Context, catalog *runtime.RegistryCatalog) error {
					return genbinding.BindReadInput(catalog, "accepted")
				},
			})
			require.NoError(t, err)
			registration.Definition = registration.Definition.WithRegistryTools(selectedNativeSource{selected: selected})
			require.NoError(t, rt.RegisterAgent(t.Context(), registration))
			output, err := rt.MustClientFor(registration.Definition).OneShotRun(t.Context(), []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "read records"}}}}, runtime.WithTextOnly(textOnly))
			require.NoError(t, err)
			assert.Equal(t, "finished", output.Final.Text())
			assert.EqualValues(t, 1, resolutions.Load())
			assert.EqualValues(t, 1, parent.resumes.Load())
			assert.EqualValues(t, 1, child.starts.Load())
		})
	}
}
`
