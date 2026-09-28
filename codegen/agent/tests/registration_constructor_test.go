// These tests compile generated constructors and run the resulting clients.
package tests

import (
	"os/exec"
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedRegistrationConstructorComposition(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, func() {
		API("composition", func() {})
		local := Toolset("records", func() {
			Tool("inspect", "Inspect one record.", func() { Args(String); Return(String) })
		})
		Service("composition", func() {
			Agent("reader", "Read records.", func() { Use(local) })
		})
	})
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "composition/constructor_test.go", registrationConstructorTestSource)
	runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./..."))
}

const registrationConstructorTestSource = `package composition

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	genreader "generated.local/gen/composition/agents/reader"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type catalogPlanner struct{ names []string }
func(p *catalogPlanner) PlanStart(_ context.Context,input *planner.PlanInput)(*planner.PlanResult,error){
	for _,tool:=range input.Agent.AdvertisedToolDefinitions(){p.names=append(p.names,tool.Name)}
	return &planner.PlanResult{FinalResponse:&planner.FinalResponse{Message:&model.Message{
		Role:model.ConversationRoleAssistant,Parts:[]model.Part{model.TextPart{Text:"inspected"}},
	}}},nil
}
func(*catalogPlanner) PlanResume(context.Context,*planner.PlanResumeInput)(*planner.PlanResult,error){return nil,errors.New("unexpected resume")}
type scopedSources struct { reads int; label string }
func(s *scopedSources) Resolve(_ context.Context,c *runtime.RegistryCatalog)error{s.reads++;s.label=c.RunLabels()["selection"];return nil}
func(*scopedSources) Allows(string,string,string)bool{return false}

func TestConstructorIsPureAndClientUsesComposedDefinition(t *testing.T){
	pl:=&catalogPlanner{}
	cfg:=genreader.ReaderAgentConfig{Planner:pl}
	_,err:=genreader.NewReaderAgentRegistration(nil,cfg)
	require.ErrorContains(t,err,"runtime is required")
	rt:=runtime.New(storageinmem.New(),runtime.WithEngine(engineinmem.New()))
	_,err=genreader.NewReaderAgentRegistration(rt,genreader.ReaderAgentConfig{})
	require.Error(t,err)
	registration,err:=genreader.NewReaderAgentRegistration(rt,cfg)
	require.NoError(t,err)
	_,err=rt.Client(genreader.AgentID)
	require.ErrorIs(t,err,runtime.ErrAgentNotFound,"constructing must not register workers")
	require.Same(t,pl,registration.Planner)
	require.Equal(t,genreader.Definition().Route(),registration.Definition.Route())
	require.Equal(t,genreader.PlanActivity,registration.PlanActivityName)
	require.Equal(t,genreader.ResumeActivity,registration.ResumeActivityName)
	require.Equal(t,genreader.ExecuteToolActivity,registration.ExecuteToolActivity)
	sources:=&scopedSources{}
	definition:=registration.Definition.WithRegistryTools(sources)
	registration.Definition=definition
	client:=rt.MustClientFor(definition)
	executor:=runtime.ToolCallExecutorFunc(func(context.Context,*runtime.ToolCallMeta,*runtime.ToolCall)(*runtime.ToolExecutionResult,error){return nil,errors.New("must not execute")})
	require.NoError(t,genreader.RegisterUsedToolsets(t.Context(),rt,genreader.WithRecordsExecutor(executor)))
	require.NoError(t,rt.RegisterAgent(t.Context(),registration))
	messages:=[]*model.Message{{Role:model.ConversationRoleUser,Parts:[]model.Part{model.TextPart{Text:"inspect"}}}}
	_,err=client.OneShotRun(t.Context(),messages,runtime.WithLabels(map[string]string{"selection":"first"}))
	require.NoError(t,err)
	require.Equal(t,1,sources.reads)
	require.Equal(t,"first",sources.label)
	require.Equal(t,[]string{"records.inspect"},pl.names)

	// The original generated Register behavior and static definition remain
	// usable independently after composing another copy.
	other:=runtime.New(storageinmem.New(),runtime.WithEngine(engineinmem.New()))
	static:=&catalogPlanner{}
	require.NoError(t,genreader.RegisterUsedToolsets(t.Context(),other,genreader.WithRecordsExecutor(executor)))
	require.NoError(t,genreader.RegisterReaderAgent(t.Context(),other,genreader.ReaderAgentConfig{Planner:static}))
	_,err=genreader.NewClient(other).OneShotRun(t.Context(),messages)
	require.NoError(t,err)
	require.Equal(t,pl.names,static.names)
	require.Equal(t,1,sources.reads,"the generated static definition was not mutated")
}
`
