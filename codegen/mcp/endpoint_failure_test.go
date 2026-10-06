// These tests send synthetic failures through a generated MCP HTTP server.
// They check that original server faults remain internal protocol errors after
// redaction, while domain errors and independent joined failures remain tool
// results. Invalid service-selected views are checked before content encoding.
package codegen

const endpointFailureTest = `package endpointcontract_test

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "net/http/httptest"
 "sync/atomic"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"

 genservice "endpoint-contract.local/gen/secured"
 genmcp "endpoint-contract.local/gen/mcp_secured"
 genserver "endpoint-contract.local/gen/jsonrpc/mcp_secured/server"
 genexecutor "endpoint-contract.local/gen/secured/toolsets/endpoint_contract/mcp"
 "goa.design/goa-ai/runtime/agent/planner"
 "goa.design/goa-ai/runtime/agent/rawjson"
 "goa.design/goa-ai/runtime/agent/runtime"
 "goa.design/goa-ai/runtime/agent/tools"
 "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
)

type (
 invalidResultService struct {
  *service
  result *genservice.ViewedRecord
  view string
 }
 invalidNestedService struct {
  *service
 }
)

func (s *invalidResultService) SelectView(context.Context,*genservice.SelectViewPayload)(*genservice.ViewedRecord,string,error){
 return s.result,s.view,nil
}

func TestEndpointFailureClassification(t *testing.T){
 for _,test:=range []struct{
  name string
  failure error
  pingFault,notifyFault bool
 }{
  {"server fault",goa.Fault("private failure"),true,true},
  {"wrapped fault",fmt.Errorf("operation: %w",goa.Fault("private failure")),true,true},
  {"single joined fault",errors.Join(goa.Fault("private failure")),true,true},
  {"independent joined errors",errors.Join(goa.Fault("private failure"),errors.New("domain rejection")),false,false},
  {"domain owner with fault cause",goa.NewServiceError(goa.Fault("private failure"),"denied",false,false,false),false,false},
  {"ordinary domain error",errors.New("domain rejection"),false,false},
  {"declared standard fault",goa.NewServiceError(errors.New("private failure"),"server_failure",false,false,false),true,false},
  {"declared custom fault",&genservice.EndpointFailure{Name:"server_failure",Message:"private failure"},true,false},
  {"wrapped custom fault",fmt.Errorf("operation: %w",&genservice.EndpointFailure{Name:"server_failure",Message:"private failure"}),true,false},
  {"declared custom domain error",&genservice.EndpointFailure{Name:"denied",Message:"domain rejection"},false,false},
 }{
  for _,mapper:=range []struct{name string;mapError func(error)error}{
   {"original",nil},
   {"redacted domain-shaped error",func(error)error{return errors.New("safe failure")}},
   {"redacted fault-shaped error",func(error)error{return goa.Fault("safe failure")}},
  }{
   t.Run(test.name+"/"+mapper.name,func(t *testing.T){
    endpoints:=genservice.NewEndpoints(&service{},&interceptors{})
    var calls atomic.Int64
    fail:=func(context.Context,any)(any,error){calls.Add(1);return nil,test.failure}
    endpoints.Ping=fail
    endpoints.Notify=fail
    server:=startFailureServer(t,endpoints,mapper.mapError)
    caller:=mcp.NewHTTPTransport(server.Client(),mcp.ClientInfo{Name:"failure-test",Version:"1"},mcp.HTTPBindings{Tools: map[string]mcp.ToolBinding{"ping":{Idempotent:true},"notify":{Idempotent:true}}},mcp.InputSupport{},mcp.HTTPRetryPolicy{MaxAttempts:3,TrustToolAnnotations:true})
    for _,method:=range []struct{name,args string;fault bool}{
     {"ping","{}",test.pingFault},
     {"notify","{\"key\":\"record\"}",test.notifyFault},
    }{
     before:=calls.Load()
     response,err:=caller.CallTool(t.Context(),server.URL+"/mcp",mcp.CallRequest{Tool:method.name,Payload:json.RawMessage(method.args)})
     require.Error(t,err)
     assert.Equal(t,before+1,calls.Load(),"a delivered failure must not replay the endpoint")
     if method.fault {
      var protocol *mcp.Error
      require.ErrorAs(t,err,&protocol)
      assert.Equal(t,mcp.JSONRPCInternalError,protocol.Code)
      var toolFailure *mcp.ToolExecutionError
      assert.False(t,errors.As(err,&toolFailure))
      recovered:=runtime.MCPCallFailure(tools.Ident("endpoint-contract."+method.name),err)
      assert.Equal(t,planner.FailureInternal,recovered.Failure.Kind)
      assert.Equal(t,planner.RecoveryFinish,recovered.Failure.Recovery.Action)
      if mapper.mapError!=nil {assert.Equal(t,"safe failure",protocol.Message)}
     }else{
      var toolFailure *mcp.ToolExecutionError
      require.ErrorAs(t,err,&toolFailure)
      assert.Empty(t,response.StructuredContent)
      if mapper.mapError!=nil {assert.Equal(t,"MCP tool execution error: safe failure",toolFailure.Error())}
     }
    }
   })
  }
 }
}

func TestSelectedResultContracts(t *testing.T){
 for _,test:=range []struct{name,view string;result *genservice.ViewedRecord;fault bool}{
  {"valid empty required string","detailed",&genservice.ViewedRecord{Hidden:"detail"},false},
  {"undeclared view","other",&genservice.ViewedRecord{Visible:"shown"},true},
  {"missing result","default",nil,true},
 }{
  t.Run(test.name,func(t *testing.T){
   s:=&invalidResultService{service:&service{},result:test.result,view:test.view}
   endpoints:=genservice.NewEndpoints(s,&interceptors{})
   server:=startFailureServer(t,endpoints,func(error)error{return errors.New("safe failure")})
   caller,err:=mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint:server.URL+"/mcp",Client:server.Client(),ClientInfo:mcp.ClientInfo{Name:"result-test",Version:"1"}})
   require.NoError(t,err)
   executed,err:=genexecutor.NewMCPExecutor(caller).Execute(t.Context(),nil,&runtime.ToolCall{Name:"endpoint-contract.select_view",Payload:rawjson.Message("{\"view\":\"default\"}")})
   require.NoError(t,err)
   if !test.fault {
    assert.Nil(t,executed.ToolResult.Failure)
    return
   }
   require.NotNil(t,executed.ToolResult.Failure)
   assert.Equal(t,planner.FailureInternal,executed.ToolResult.Failure.Kind)
   assert.Equal(t,planner.RecoveryFinish,executed.ToolResult.Failure.Recovery.Action)
  })
 }
}

func (s *invalidNestedService) Nested(context.Context)(*genservice.NestedRecord,error){
 return &genservice.NestedRecord{Detail:&genservice.ViewedRecord{Visible:"shown",Hidden:"detail"}},nil
}

func TestMissingRequiredResultObjectIsProtocolFault(t *testing.T){
 endpoints:=genservice.NewEndpoints(&invalidNestedService{service:&service{}},&interceptors{})
 server:=startFailureServer(t,endpoints,func(error)error{return errors.New("safe failure")})
 caller,err:=mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint:server.URL+"/mcp",Client:server.Client(),ClientInfo:mcp.ClientInfo{Name:"nested-result-test",Version:"1"}})
 require.NoError(t,err)
 executed,err:=genexecutor.NewMCPExecutor(caller).Execute(t.Context(),nil,&runtime.ToolCall{Name:"endpoint-contract.nested",Payload:rawjson.Message("{}")})
 require.NoError(t,err)
 require.NotNil(t,executed.ToolResult.Failure)
 assert.Equal(t,planner.FailureInternal,executed.ToolResult.Failure.Kind)
 assert.Equal(t,planner.RecoveryFinish,executed.ToolResult.Failure.Recovery.Action)
}

// startFailureServer connects the configured endpoints and disclosure policy to
// the generated HTTP binding and returns a server closed by test cleanup.
func startFailureServer(t *testing.T,endpoints *genservice.Endpoints,mapper func(error)error)*httptest.Server{
 t.Helper()
 mux:=goahttp.NewMuxer()
 adapter:=genmcp.NewMCPAdapter(endpoints,&genmcp.MCPAdapterOptions{ErrorMapper:mapper})
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 httpServer:=httptest.NewServer(mux)
 t.Cleanup(httpServer.Close)
 return httpServer
}
`
