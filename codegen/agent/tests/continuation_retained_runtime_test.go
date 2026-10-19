// These tests generate a provider and its bound consumer, then execute paging
// through the public runtime so generated codecs validate the retained input.
package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedContinuationRetainsBoundExecutionSubset(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", func() {
		API("paging", func() {})
		scope := Type("Workspace", func() {
			Attribute("name", String, func() { MinLength(1) })
			Attribute("revision", Int)
			Required("name", "revision")
		})
		provider := Toolset("records", func() {
			Tool("search", "Find records.", func() {
				Args(func() {
					Attribute("workspace", scope)
					Attribute("query", String)
					Attribute("optional", Boolean)
					Required("workspace", "query")
				})
				Return(func() { Attribute("items", ArrayOf(String)); Required("items") })
				BoundedResult(func() { ContinueWith("continue_search", "cursor"); NextCursor("next_cursor") })
			})
			Tool("continue_search", "Read the next page.", func() {
				Args(func() {
					Attribute("workspace", scope)
					Attribute("optional", Boolean)
					Attribute("cursor", String)
					Required("workspace", "cursor")
				})
				Return(func() { Attribute("items", ArrayOf(String)); Required("items") })
				BoundedResult(func() { Cursor("cursor"); NextCursor("next_cursor") })
			})
		})
		Service("reader", func() {
			Agent("bound", "Reads one selected workspace.", func() { Use(provider, func() { BindInput("search", "workspace") }) })
		})
	})
	root := writeGeneratedModuleKeepingGen(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/reader/agents/bound/retained_runtime_test.go"), []byte(generatedRetainedRuntimeSource), 0600))
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "-v", "./gen/reader/agents/bound"))
}

const generatedRetainedRuntimeSource = `package bound_test

import (
 "context"
 "fmt"
 "sync/atomic"
 "testing"

 genbound "generated.local/gen/reader/agents/bound"
 genbinding "generated.local/gen/reader/agents/bound/records/inputbinding"
 genrecords "generated.local/gen/reader/toolsets/records"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 "goa.design/goa-ai/runtime/agent"
 engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
 "goa.design/goa-ai/runtime/agent/model"
 "goa.design/goa-ai/runtime/agent/planner"
 "goa.design/goa-ai/runtime/agent/rawjson"
 "goa.design/goa-ai/runtime/agent/runtime"
 storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type retainedPlanner struct { t *testing.T; optional bool }
func(p *retainedPlanner) PlanStart(_ context.Context,in *planner.PlanInput)(*planner.PlanResult,error){
 for _,def:=range in.Agent.AdvertisedToolDefinitions(){
  if def.Name=="records.search" { assert.NotContains(p.t,string(def.Input.Contract().Schema),"workspace") }
 }
 input:=rawjson.Message("{\"query\":\"active\"}")
 if p.optional {input=rawjson.Message("{\"query\":\"active\",\"optional\":false}")}
 return &planner.PlanResult{ToolCalls:[]planner.ToolRequest{{Name:"records.search",Payload:input}}},nil
}
func(p *retainedPlanner) PlanResume(_ context.Context,in *planner.PlanResumeInput)(*planner.PlanResult,error){
 return &planner.PlanResult{FinalResponse:&planner.FinalResponse{Message:&model.Message{Role:model.ConversationRoleAssistant,Parts:[]model.Part{model.TextPart{Text:"finished"}}}}},nil
}
func TestGeneratedRetainedSubsetInConcurrentBoundRuns(t *testing.T){
 for _,scope:=range []string{"first","second"}{
  t.Run(scope,func(t *testing.T){
   t.Parallel()
   optional:=scope=="second"
   rt:=runtime.New(storageinmem.New(),runtime.WithEngine(engineinmem.New()))
   var executions atomic.Int32
   cfg:=genbound.BoundAgentConfig{Planner:&retainedPlanner{t:t,optional:optional},PrepareInputs:func(_ context.Context,c *runtime.RegistryCatalog)error{
    return genbinding.BindSearchInput(c,&genrecords.Workspace{Name:scope,Revision:7})
   }}
   registration,err:=genbound.NewBoundAgentRegistration(rt,cfg)
   require.NoError(t,err)
   client:=rt.MustClientFor(registration.Definition)
   source,continuation:=genrecords.SpecSearch(),genrecords.SpecContinueSearch()
   assert.Equal(t,[]string{"optional","workspace"},continuation.Bounds.Paging.RetainedFields)
   execute:=runtime.ToolCallExecutorFunc(func(_ context.Context,_ *runtime.ToolCallMeta,call *runtime.ToolCall)(*runtime.ToolExecutionResult,error){
    executions.Add(1)
    result:=&planner.ToolResult{Name:call.Name,ToolCallID:call.ToolCallID}
    assert.Equal(t,source.Name,call.Name)
    decoded,err:=source.ExecutionPayloadCodec.FromJSON(call.Payload)
    if err!=nil{return nil,err}
    switch payload:=decoded.(type) {
    case *genrecords.SearchPayload:
     assert.Equal(t,scope,payload.Workspace.Name)
     assert.Equal(t,7,payload.Workspace.Revision)
     assert.Equal(t,"active",payload.Query)
     assert.NotContains(t,string(call.ModelPayload),"workspace")
     value,err:=source.Result.Codec.FromJSON([]byte("{\"items\":[]}"))
     if err!=nil{return nil,err}
     result.Result=value
     cursor:=scope+"-next"
     result.Bounds=&agent.Bounds{Returned:0,Truncated:true,NextCursor:&cursor}
    case *genrecords.ContinueSearchPayload:
     encoded,err:=source.ExecutionPayloadCodec.ToJSON(payload)
     if err!=nil{return nil,err}
     want:=fmt.Sprintf("{\"workspace\":{\"name\":%q,\"revision\":7},\"cursor\":%q}",scope,scope+"-next")
     if optional {want=fmt.Sprintf("{\"workspace\":{\"name\":%q,\"revision\":7},\"cursor\":%q,\"optional\":false}",scope,scope+"-next")}
     assert.JSONEq(t,want,string(encoded))
     assert.NotContains(t,string(encoded),"query")
     result.Result,err=source.Result.Codec.FromJSON([]byte("{\"items\":[\"match\"]}"))
     if err!=nil{return nil,err}
     result.Bounds=&agent.Bounds{Returned:1}
    default:return nil,fmt.Errorf("unexpected input type %T",decoded)
    }
    return runtime.Executed(result),nil
   })
   require.NoError(t,genbound.RegisterUsedToolsets(t.Context(),rt,genbound.WithRecordsExecutor(execute)))
   require.NoError(t,rt.RegisterAgent(t.Context(),registration))
   out,err:=client.OneShotRun(t.Context(),[]*model.Message{{Role:model.ConversationRoleUser,Parts:[]model.Part{model.TextPart{Text:"read records"}}}})
   require.NoError(t,err)
   assert.Equal(t,"finished",out.Final.Text())
   assert.EqualValues(t,2,executions.Load())
  })
 }
}
`
