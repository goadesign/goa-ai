// Package tests verifies native input rounds through generated service callers.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGeneratedNativeInputExchangeContract keeps host data outside model contracts.
func TestGeneratedNativeInputExchangeContract(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.NativeInputExchange())
	types := fileContent(t, files, "gen/records/toolsets/lookup/types.go")
	assert.NotContains(t, types, "HostInput")
	assert.NotContains(t, types, "OperationOutcome")
	assert.Contains(t, types, "Label")
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "gen/records/agents/scribe/lookup/input_exchange_test.go", nativeInputRuntimeTests)
	runGeneratedPackageTest(t, root, "./gen/records/...")
}

// TestGeneratedNativeInputExchangeView compiles the service-selected view arity.
func TestGeneratedNativeInputExchangeView(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.NativeInputExchangeView())
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "gen/records/agents/scribe/lookup/input_exchange_view_test.go", nativeViewRuntimeTests)
	runGeneratedPackageTest(t, root, "./gen/records/...")
}

const nativeInputRuntimeTests = `package lookup_test

import (
 "context"
 "encoding/json"
 "errors"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genrecords "generated.local/gen/records"
 genlookup "generated.local/gen/records/toolsets/lookup"
 genexecutor "generated.local/gen/records/agents/scribe/lookup"
 "goa.design/goa-ai/runtime/agent/runtime"
 "goa.design/goa-ai/runtime/agent/rawjson"
 "goa.design/goa-ai/runtime/agent/tools"
 "goa.design/goa-ai/runtime/mcp"
 "goa.design/goa-ai/runtime/toolregistry"
)

type recordService struct { calls int }

func (s *recordService) Read(_ context.Context,p *genrecords.ReadPayload)(*genrecords.Operation,error) {
 s.calls++
 if p.SessionID!="session" || p.Target=="" { return nil,errors.New("domain or injected input changed") }
 if p.Target=="empty" {
  if p.HostInput!=nil {return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeComplete(&genrecords.Completed{Label:"empty-round"})},nil}
  return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{Requests:&genrecords.Requests{}})},nil
 }
 if p.Target=="url" {
  if p.HostInput!=nil&&p.HostInput.Responses!=nil&&p.HostInput.Responses.Payment!=nil {
   answer:=p.HostInput.Responses.Payment.Answer
   label:="cancelled"
   if _,ok:=answer.AsAccept();ok {label="approved"}
   if _,ok:=answer.AsDecline();ok {label="declined"}
   return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeComplete(&genrecords.Completed{Label:label})},nil
  }
  state:=""
  return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{State:&state,Requests:&genrecords.Requests{Payment:&genrecords.URLQuestion{Message:"Approve access",URL:"https://consent.example/approve"}}})},nil
 }
 if p.Target=="invalid_result" {return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{})},nil}
 if p.HostInput!=nil {
  if p.HostInput.OpaqueState==nil||*p.HostInput.OpaqueState!="" {return nil,errors.New("exact empty state changed")}
  if p.HostInput.Responses!=nil&&p.HostInput.Responses.Profile!=nil {
   answer:=p.HostInput.Responses.Profile.Answer
   if accepted,ok:=answer.AsAccept();ok {
    if accepted.Content.Quantity==nil||*accepted.Content.Quantity!=3 {return nil,errors.New("typed whole form count changed")}
    return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeComplete(&genrecords.Completed{Label:accepted.Content.Label,Returned:1,Evidence:&genrecords.Evidence{Note:"retained"}})},nil
   }
   if _,ok:=answer.AsDecline();ok {return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeComplete(&genrecords.Completed{Label:"declined"})},nil}
   if _,ok:=answer.AsCancel();ok {return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeComplete(&genrecords.Completed{Label:"cancelled"})},nil}
  }
 }
 state:=""
 return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{State:&state,Requests:&genrecords.Requests{Profile:&genrecords.Question{PromptText:"Choose a label"}}})},nil
}

func hostAnswer(action string)*mcp.CallContinuation {
 state:=""
 answer:=json.RawMessage("{\"action\":\""+action+"\"}")
 if action=="accept" {answer=json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"accepted\",\"quantity\":3.0}}")}
 return &mcp.CallContinuation{RequestState:&state,InputResponses:map[string]json.RawMessage{"profile":answer}}
}

func TestLocalBoundInputRounds(t *testing.T) {
 for _, action:=range []string{"accept","decline","cancel"} {
  t.Run(action,func(t *testing.T){
   service:=&recordService{}
   executor:=genexecutor.NewScribeLookupExec(genexecutor.WithClient(genrecords.NewClient(genrecords.NewEndpoints(service).Read)))
   call:=&runtime.ToolCall{Name:tools.Ident("lookup.read"),Payload:[]byte("{\"target\":\"form\"}")}
   meta:=&runtime.ToolCallMeta{SessionID:"session"}
   pending,err:=executor.Execute(t.Context(),meta,call)
   require.NoError(t,err)
   assert.Nil(t,pending.ToolResult,"pending input must not enter completed tool history")
   call.InputRound=1
   call.MCPContinuation=hostAnswer(action)
   completed,err:=executor.Execute(t.Context(),meta,call)
   require.NoError(t,err)
   require.NotNil(t,completed.ToolResult)
   require.Nil(t,completed.ToolResult.Failure)
   result,ok:=completed.ToolResult.Result.(*genlookup.ReadResult)
   require.True(t,ok)
   expected:=map[string]string{"accept":"accepted","decline":"declined","cancel":"cancelled"}[action]
   assert.Equal(t,expected,result.Label)
   _,err=genlookup.ReadResultCodec().ToJSON(result)
   require.NoError(t,err)
   assert.Equal(t,2,service.calls)
   call.Name="lookup.read_projected"
   projected,err:=executor.Execute(t.Context(),meta,call)
   require.NoError(t,err)
   require.NotNil(t,projected.ToolResult.Bounds)
   if action=="accept" {
    assert.Equal(t,1,projected.ToolResult.Bounds.Returned)
    assert.Contains(t,string(projected.ToolResult.ServerData),"retained")
   }
   projectedValue,ok:=projected.ToolResult.Result.(*genlookup.ReadProjectedResult)
   require.True(t,ok)
   assert.Equal(t,expected,projectedValue.Label)
  })
 }
}

func TestRegistryProviderInputRounds(t *testing.T) {
 service:=&recordService{}
 provider:=genlookup.NewProvider(service)
 msg:=toolregistry.ToolCallMessage{RegistrationToken:"registration",ToolUseID:toolregistry.DeriveToolUseID("run","call",0),Tool:genlookup.Read,Payload:[]byte("{\"target\":\"form\"}"),Meta:&toolregistry.ToolCallMeta{RunID:"run",SessionID:"session",ToolCallID:"call"}}
 pending,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
 require.NoError(t,err)
 require.NotNil(t,pending.InputRequired)
 assert.Empty(t,pending.Result)
 require.NoError(t,pending.InputRequired.Validate(mcp.InputSupport{Form:true}))
 assert.Equal(t,"",*pending.InputRequired.RequestState)
 require.NoError(t,pending.InputRequired.ValidateResponses(hostAnswer("accept").InputResponses))
 msg.Meta.InputRound=1
 msg.Meta.InputContinuation=hostAnswer("accept")
 msg.ToolUseID=toolregistry.DeriveToolUseID("run","call",1)
 completed,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
 require.NoError(t,err)
 assert.Nil(t,completed.InputRequired)
 value,err:=genlookup.ReadResultCodec().FromJSON(completed.Result)
 require.NoError(t,err)
 assert.Equal(t,"accepted",value.Label)
 assert.Equal(t,2,service.calls)
 msg.Tool=genlookup.ReadProjected
 projected,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
 require.NoError(t,err)
 require.NotNil(t,projected.Bounds)
 assert.Equal(t,1,projected.Bounds.Returned)
 require.Len(t,projected.ServerData,1)
 assert.Equal(t,"evidence",projected.ServerData[0].Kind)
 projectedValue,err:=genlookup.ReadProjectedResultCodec().FromJSON(projected.Result)
 require.NoError(t,err)
 assert.Equal(t,"accepted",projectedValue.Label)
}

func TestNativeURLConsent(t *testing.T) {
 for _, action:=range []string{"accept","decline","cancel"} {
  t.Run(action,func(t *testing.T){
   service:=&recordService{}
   provider:=genlookup.NewProvider(service)
   msg:=toolregistry.ToolCallMessage{ToolUseID:toolregistry.DeriveToolUseID("run","url",0),Tool:genlookup.Read,Payload:[]byte("{\"target\":\"url\"}"),Meta:&toolregistry.ToolCallMeta{SessionID:"session"}}
   pending,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
   require.NoError(t,err)
   require.NotNil(t,pending.InputRequired)
   require.NoError(t,pending.InputRequired.Validate(mcp.InputSupport{URL:true}))
   answer:=json.RawMessage("{\"action\":\""+action+"\"}")
   responses:=map[string]json.RawMessage{"payment":answer}
   require.NoError(t,pending.InputRequired.ValidateResponses(responses))
   input:=&mcp.CallContinuation{RequestState:pending.InputRequired.RequestState,InputResponses:responses}
   executor:=genexecutor.NewScribeLookupExec(genexecutor.WithClient(genrecords.NewClient(genrecords.NewEndpoints(service).Read)))
   complete,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{SessionID:"session"},&runtime.ToolCall{Name:"lookup.read",Payload:rawjson.Message(msg.Payload),InputRound:1,MCPContinuation:input})
   require.NoError(t,err)
   require.NotNil(t,complete.ToolResult)
   require.Nil(t,complete.ToolResult.Failure)
   value,ok:=complete.ToolResult.Result.(*genlookup.ReadResult)
   require.True(t,ok)
   expected:=map[string]string{"accept":"approved","decline":"declined","cancel":"cancelled"}[action]
   assert.Equal(t,expected,value.Label)
  })
 }
}

func TestBoundInputBoundaryFailuresAndEmptyRound(t *testing.T) {
 for _, test:=range []struct {name,target string;round uint64;input *mcp.CallContinuation;textOnly bool;calls int;failed bool}{
  {name:"empty continuation",target:"empty",round:1,input:&mcp.CallContinuation{},calls:1},
  {name:"text only pending",target:"form",textOnly:true,calls:1,failed:true},
  {name:"text only continuation",target:"form",round:1,input:hostAnswer("accept"),textOnly:true,failed:true},
  {name:"initial continuation",target:"form",input:hostAnswer("accept"),failed:true},
  {name:"later missing continuation",target:"form",round:1,failed:true},
  {name:"invalid pending",target:"invalid_result",calls:1,failed:true},
  {name:"fractional form",target:"form",round:1,input:&mcp.CallContinuation{InputResponses:map[string]json.RawMessage{"profile":json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"value\",\"quantity\":3.5}}")}},failed:true},
 } {
  t.Run(test.name,func(t *testing.T){
   service:=&recordService{}
   executor:=genexecutor.NewScribeLookupExec(genexecutor.WithClient(genrecords.NewClient(genrecords.NewEndpoints(service).Read)))
   call:=&runtime.ToolCall{Name:"lookup.read",Payload:[]byte("{\"target\":\""+test.target+"\"}"),InputRound:test.round,MCPContinuation:test.input,TextOnly:test.textOnly}
   result,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{SessionID:"session"},call)
   require.NoError(t,err)
   require.NotNil(t,result.ToolResult)
   assert.Equal(t,test.failed,result.ToolResult.Failure!=nil)
   assert.Equal(t,test.calls,service.calls)
   provider:=genlookup.NewProvider(&recordService{})
   msg:=toolregistry.ToolCallMessage{ToolUseID:toolregistry.DeriveToolUseID("run","call",test.round),Tool:genlookup.Read,Payload:json.RawMessage(call.Payload),Meta:&toolregistry.ToolCallMeta{SessionID:"session",InputRound:test.round,InputContinuation:test.input,TextOnly:test.textOnly}}
   output,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
   require.NoError(t,err)
   assert.Equal(t,test.failed,output.Error!=nil)
  })
 }
}
`

const nativeViewRuntimeTests = `package lookup_test

import (
 "context"
 "encoding/json"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genrecords "generated.local/gen/records"
 genlookup "generated.local/gen/records/toolsets/lookup"
 genexecutor "generated.local/gen/records/agents/scribe/lookup"
 "goa.design/goa-ai/runtime/agent/runtime"
 "goa.design/goa-ai/runtime/mcp"
 "goa.design/goa-ai/runtime/toolregistry"
)

type viewedService struct{}

func (*viewedService) Read(_ context.Context,p *genrecords.ReadPayload)(*genrecords.Operation,string,error) {
 if p.HostInput==nil {
  state:=""
  return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{State:&state,Requests:&genrecords.Requests{Profile:&genrecords.Question{PromptText:"Choose a label"}}})},"alternate",nil
 }
 return &genrecords.Operation{Outcome:genrecords.NewOperationOutcomeComplete(&genrecords.Completed{Label:"accepted",Returned:1,Evidence:&genrecords.Evidence{Note:"retained"}})},"alternate",nil
}

// TestLocalViewedService uses the generated override for a service with a view
// return. Tool.Return owns tool output; the HTTP view remains a separate result.
func TestLocalViewedService(t *testing.T) {
 service:=&viewedService{}
 invoke:=func(ctx context.Context,input any)(any,error) {
  result,_,err:=service.Read(ctx,input.(*genrecords.ReadPayload))
  return result,err
 }
 executor:=genexecutor.NewScribeLookupExec(genexecutor.WithRead(invoke),genexecutor.WithReadProjected(invoke))
 call:=&runtime.ToolCall{Name:"lookup.read_projected",Payload:[]byte("{\"target\":\"form\"}")}
 meta:=&runtime.ToolCallMeta{SessionID:"session"}
 pending,err:=executor.Execute(t.Context(),meta,call)
 require.NoError(t,err)
 require.Nil(t,pending.ToolResult)
 call.InputRound=1
 call.MCPContinuation=&mcp.CallContinuation{InputResponses:map[string]json.RawMessage{"profile":json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"value\"}}")}}
 complete,err:=executor.Execute(t.Context(),meta,call)
 require.NoError(t,err)
 require.NotNil(t,complete.ToolResult)
 require.Nil(t,complete.ToolResult.Failure)
 value,ok:=complete.ToolResult.Result.(*genlookup.ReadProjectedResult)
 require.True(t,ok)
 assert.Equal(t,"accepted",value.Label)
 assert.Equal(t,1,complete.ToolResult.Bounds.Returned)
 assert.Contains(t,string(complete.ToolResult.ServerData),"retained")
}

func TestRegistrySelectedView(t *testing.T) {
 provider:=genlookup.NewProvider(&viewedService{})
 msg:=toolregistry.ToolCallMessage{RegistrationToken:"registration",ToolUseID:toolregistry.DeriveToolUseID("run","call",0),Tool:genlookup.ReadProjected,Payload:[]byte("{\"target\":\"form\"}"),Meta:&toolregistry.ToolCallMeta{SessionID:"session"}}
 pending,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
 require.NoError(t,err)
 require.NotNil(t,pending.InputRequired)
 require.NoError(t,pending.InputRequired.Validate(mcp.InputSupport{Form:true}))
 msg.Meta.InputRound=1
 msg.Meta.InputContinuation=&mcp.CallContinuation{RequestState:pending.InputRequired.RequestState,InputResponses:map[string]json.RawMessage{"profile":json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"value\"}}")}}
 msg.ToolUseID=toolregistry.DeriveToolUseID("run","call",1)
 complete,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
 require.NoError(t,err)
 require.NotNil(t,complete.Bounds)
 assert.Equal(t,1,complete.Bounds.Returned)
 require.Len(t,complete.ServerData,1)
 value,err:=genlookup.ReadProjectedResultCodec().FromJSON(complete.Result)
 require.NoError(t,err)
 assert.Equal(t,"accepted",value.Label)
 assert.NotContains(t,string(complete.Result),"retained")
}
`
