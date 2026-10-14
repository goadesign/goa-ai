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
	"sync/atomic"
	"testing"

	genbound "generated.local/gen/reader/agents/bound"
	genbinding "generated.local/gen/reader/agents/bound/records/inputbinding"
	genspecialist "generated.local/gen/worker/agents/specialist"
	genagenttools "generated.local/gen/worker/agents/specialist/agenttools/records"
	genrecords "generated.local/gen/worker/agents/specialist/exports/records"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type bindingPlanner struct {
	t *testing.T
	child, typed bool
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
`
