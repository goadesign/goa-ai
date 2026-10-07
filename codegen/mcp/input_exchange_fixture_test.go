// These synthetic contracts exercise typed input exchanges over generated HTTP
// handlers. Service-selected questions remain separate from tool arguments;
// native endpoint authentication runs again for every continuation request.
package codegen

const inputExchangeDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _ = API("input-peer", func(){ Title("Typed input exchanges") })
var jwt=JWTSecurity("jwt",func(){Scope("read","Read synthetic values")})
var content=Type("FormContent",func(){
 Field(1,"label",String,"Label selected by the host",func(){MinLength(1)})
 Field(2,"quantity",Int,"Optional item count supplied in this answer",func(){Meta("struct:tag:json","itemCount,omitempty")})
 Required("label")
})
var accepted=Type("FormAccepted",func(){Field(1,"content",content,"Accepted form values",func(){Meta("struct:tag:json","serviceValues")});Required("content")})
var empty=Type("EmptyAnswer",func(){})
var formResponse=Type("FormResponse",func(){
 OneOf("answer","Host decision",func(){TypeName("FormAnswer");Attribute("accept",accepted,"Accepted form");Attribute("decline",empty,"Declined form");Attribute("cancel",empty,"Cancelled form")})
 Required("answer")
})
var urlResponse=Type("URLResponse",func(){
 OneOf("answer","Host decision",func(){TypeName("URLAnswer");Attribute("accept",empty,"Accepted URL interaction");Attribute("decline",empty,"Declined URL interaction");Attribute("cancel",empty,"Cancelled URL interaction")})
 Required("answer")
})
var answers=Type("InputAnswers",func(){Field(1,"profile",formResponse,"Profile answer");Field(2,"payment",urlResponse,"Payment consent")})
var continuation=Type("Continuation",func(){
 Field(1,"state",String,"Exact state returned by the service",func(){Meta("struct:field:name","OpaqueState")})
 Field(2,"responses",answers,"Answers for this invocation")
})
var form=Type("FormQuestion",func(){Field(1,"message",String,"Question shown to the consenting user",func(){MinLength(1);Meta("struct:field:name","PromptText")});Required("message")})
var url=Type("URLQuestion",func(){Field(1,"message",String,"Consent requested from the user");Field(2,"url",String,"External consent URL",func(){Format(FormatURI)});Required("message","url")})
var questions=Type("InputQuestions",func(){Field(1,"profile",form,"Profile form");Field(2,"payment",url,"External payment consent")})
var pending=Type("Pending",func(){Field(1,"state",String,"Opaque service state",func(){Meta("struct:field:name","OpaqueState")});Field(2,"requests",questions,"Selected questions")})
var operation=Type("OperationResult",func(){
 OneOf("outcome","Completed value or unfinished operation",func(){TypeName("OperationOutcome");Attribute("complete",String,"Completed label");Attribute("input_required",pending,"Requested input")})
 Required("outcome")
})
var viewedOperation=ResultType("application/vnd.input.operation",func(){
 TypeName("ViewedOperation")
 OneOf("outcome","Completed value or unfinished operation",func(){TypeName("ViewedOutcome");Attribute("complete",String,"Completed label");Attribute("input_required",pending,"Requested input")})
 Required("outcome")
 View("default",func(){Attribute("outcome")})
 View("alternate",func(){Attribute("outcome")})
})
var promptText=Type("PromptText",func(){Field(1,"text",String,"Instructions shown to the user");Required("text")})
var promptMessage=Type("AuthoredMessage",func(){
 Field(1,"role",String,"Message audience",func(){Enum("user","assistant")})
 OneOf("content","Message contents",func(){TypeName("PromptContent");Attribute("text",promptText,"Text instructions")})
 Required("role","content")
})
var promptComplete=Type("PromptComplete",func(){Field(1,"messages",ArrayOfRequired(promptMessage),"Ordered instructions")})
var promptOperation=Type("PromptOperation",func(){
 OneOf("outcome","Completed messages or unfinished operation",func(){TypeName("PromptOutcome");Attribute("complete",promptComplete,"Completed messages");Attribute("input_required",pending,"Requested input")})
 Required("outcome")
})
var viewedPrompt=ResultType("application/vnd.input.prompt",func(){
 TypeName("ViewedPrompt")
 OneOf("outcome","Completed messages or unfinished operation",func(){TypeName("ViewedPromptOutcome");Attribute("complete",promptComplete,"Completed messages");Attribute("input_required",pending,"Requested input")})
 Required("outcome")
 View("default",func(){Attribute("outcome")})
 View("alternate",func(){Attribute("outcome")})
})
var _=Service("records",func(){
 Description("Reads synthetic records after collecting user input.")
 JSONRPC(func(){POST("/mcp")})
 MCP("records","1")
 Method("read",func(){
  Description("Returns a selected record label after the host answers a question.")
  Security(jwt,func(){Scope("read")})
  Payload(func(){
   Token("credential",String,"Native endpoint credential")
   Field(1,"target",String,"Requested interaction")
   Field(2,"continuation",continuation,"Host-owned continuation",func(){Meta("struct:field:name","HostInput")})
   Required("credential","target")
  })
  Result(operation)
  InputExchange("continuation","outcome")
  Tool("read","Read a synthetic record")
 })
 Method("read_fixed",func(){
  Description("Reads a label through a fixed result view after user input.")
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("credential",String,"Native endpoint credential");Field(1,"continuation",continuation,"Host-owned continuation");Required("credential")})
  Result(viewedOperation,func(){View("default")});InputExchange("continuation","outcome")
  Tool("read_fixed","Read a label through the fixed view")
 })
 Method("read_selected",func(){
  Description("Reads a label through the service-selected result view after user input.")
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("credential",String,"Native endpoint credential");Field(1,"continuation",continuation,"Host-owned continuation");Required("credential")})
  Result(viewedOperation);InputExchange("continuation","outcome")
  Tool("read_selected","Read a label through the service-selected view")
 })
 Method("document",func(){
  Description("Reads the reference document after host approval.")
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("credential",String,"Native endpoint credential");Field(1,"continuation",continuation,"Host-owned continuation");Required("credential")})
  Result(operation);InputExchange("continuation","outcome")
  Resource("document","record://reference","text/plain")
 })
 Method("review_selected",func(){
  Description("Returns review instructions through a service-selected view after user input.")
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("credential",String,"Native endpoint credential");Field(1,"continuation",continuation,"Host-owned continuation");Required("credential")})
  Result(viewedPrompt);InputExchange("continuation","outcome")
  Prompt("review_selected","Review a record through the service-selected view")
 })
 Method("review",func(){
  Description("Returns review instructions after collecting a label.")
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("credential",String,"Native endpoint credential");Field(1,"continuation",continuation,"Host-owned continuation");Required("credential")})
  Result(promptOperation);InputExchange("continuation","outcome")
  Prompt("review","Review a synthetic record")
 })
})
`

const inputExchangeRuntime = `package inputpeer
import (
 "context"
 "encoding/json"
 "errors"
 "io"
 "strings"
 "net/http"
 "net/http/httptest"
 "net/url"
 "sync/atomic"
 "testing"

 sdk "github.com/modelcontextprotocol/go-sdk/mcp"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
 genclient "input-peer.local/gen/jsonrpc/mcp_records/client"
 genserver "input-peer.local/gen/jsonrpc/mcp_records/server"
 genmcp "input-peer.local/gen/mcp_records"
 genrecords "input-peer.local/gen/records"
)
type (
 principalKey struct{}
 recordService struct { calls,auth atomic.Int64 }
 authorizedTransport struct {transport http.RoundTripper}
)
func(c authorizedTransport)RoundTrip(r *http.Request)(*http.Response,error){
 request:=r.Clone(r.Context());request.Header.Set("Authorization","Bearer authorized");return c.transport.RoundTrip(request)
}
func(s *recordService)JWTAuth(ctx context.Context,token string,scheme *security.JWTScheme)(context.Context,error){
 s.auth.Add(1)
 if token!="authorized"||len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="read"{return ctx,errors.New("credential or scope changed")}
 return context.WithValue(ctx,principalKey{},true),nil
}
func(s *recordService)Read(ctx context.Context,p *genrecords.ReadPayload)(*genrecords.OperationResult,error){
 s.calls.Add(1)
 if ctx.Value(principalKey{})!=true||p.Credential!="authorized"{return nil,errors.New("native authenticated input missing")}
 state:=""
 if p.Target=="interop"{state="opaque-for-independent-peer"}
 question:=&genrecords.Pending{OpaqueState:&state}
 if p.Target=="url"{question.Requests=&genrecords.InputQuestions{Payment:&genrecords.URLQuestion{Message:"Approve external interaction",URL:"https://consent.example/approve"}}}else{question.Requests=&genrecords.InputQuestions{Profile:&genrecords.FormQuestion{PromptText:"Choose a label"}}}
 if p.Target=="empty"{
  if p.HostInput!=nil{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeComplete("empty-continuation")},nil}
  return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{Requests:&genrecords.InputQuestions{}})},nil
 }
 if p.Target=="state_only"{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{OpaqueState:&state})},nil}
 if p.Target=="absent"{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeInputRequired(&genrecords.Pending{})},nil}
 if p.Target=="invalid"{question.Requests.Profile.PromptText=""}
 if p.HostInput!=nil{
  if p.HostInput.OpaqueState==nil||*p.HostInput.OpaqueState!=state{return nil,errors.New("state changed")}
  if p.HostInput.Responses!=nil{
   if p.Target=="url"&&p.HostInput.Responses.Payment!=nil{
    answer:=p.HostInput.Responses.Payment.Answer
    if _,ok:=answer.AsAccept();ok{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeComplete("approved")},nil}
    if _,ok:=answer.AsDecline();ok{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeComplete("declined")},nil}
    if _,ok:=answer.AsCancel();ok{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeComplete("cancelled")},nil}
   }
   if p.HostInput.Responses.Profile!=nil{
    answer:=p.HostInput.Responses.Profile.Answer
    if accepted,ok:=answer.AsAccept();ok{if accepted.Content.Label=="integral"&&(accepted.Content.Quantity==nil||*accepted.Content.Quantity!=3){return nil,errors.New("integer answer changed")};return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeComplete(genrecords.OperationOutcomeBranchComplete(accepted.Content.Label))},nil}
    if _,ok:=answer.AsDecline();ok{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeComplete("declined")},nil}
    if _,ok:=answer.AsCancel();ok{return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeComplete("cancelled")},nil}
   }
  }
 }
 return &genrecords.OperationResult{Outcome:genrecords.NewOperationOutcomeInputRequired(question)},nil
}
func(s *recordService)ReadFixed(ctx context.Context,p *genrecords.ReadFixedPayload)(*genrecords.ViewedOperation,error){
 result,err:=s.Read(ctx,&genrecords.ReadPayload{Credential:p.Credential,Target:"form",HostInput:p.Continuation})
 if err!=nil{return nil,err}
 if pending,ok:=result.Outcome.AsInputRequired();ok{return &genrecords.ViewedOperation{Outcome:genrecords.NewViewedOutcomeInputRequired(pending)},nil}
 label,_:=result.Outcome.AsComplete()
 return &genrecords.ViewedOperation{Outcome:genrecords.NewViewedOutcomeComplete(genrecords.ViewedOutcomeBranchComplete(label))},nil
}
func(s *recordService)ReadSelected(ctx context.Context,p *genrecords.ReadSelectedPayload)(*genrecords.ViewedOperation,string,error){
 result,err:=s.ReadFixed(ctx,&genrecords.ReadFixedPayload{Credential:p.Credential,Continuation:p.Continuation})
 return result,"alternate",err
}
func(s *recordService)Document(ctx context.Context,p *genrecords.DocumentPayload)(*genrecords.OperationResult,error){
 return s.Read(ctx,&genrecords.ReadPayload{Credential:p.Credential,Target:"form",HostInput:p.Continuation})
}
func(s *recordService)Review(ctx context.Context,p *genrecords.ReviewPayload)(*genrecords.PromptOperation,error){
 result,err:=s.Read(ctx,&genrecords.ReadPayload{Credential:p.Credential,Target:"form",HostInput:p.Continuation})
 if err!=nil{return nil,err}
 if pending,ok:=result.Outcome.AsInputRequired();ok{return &genrecords.PromptOperation{Outcome:genrecords.NewPromptOutcomeInputRequired(pending)},nil}
 label,_:=result.Outcome.AsComplete()
 message:=&genrecords.AuthoredMessage{Role:"user",Content:genrecords.NewPromptContentText(&genrecords.PromptText{Text:string(label)})}
 return &genrecords.PromptOperation{Outcome:genrecords.NewPromptOutcomeComplete(&genrecords.PromptComplete{Messages:[]*genrecords.AuthoredMessage{message}})},nil
}
func(s *recordService)ReviewSelected(ctx context.Context,p *genrecords.ReviewSelectedPayload)(*genrecords.ViewedPrompt,string,error){
 result,err:=s.Review(ctx,&genrecords.ReviewPayload{Credential:p.Credential,Continuation:p.Continuation})
 if err!=nil{return nil,"alternate",err}
 if pending,ok:=result.Outcome.AsInputRequired();ok{return &genrecords.ViewedPrompt{Outcome:genrecords.NewViewedPromptOutcomeInputRequired(pending)},"alternate",nil}
 complete,_:=result.Outcome.AsComplete()
 return &genrecords.ViewedPrompt{Outcome:genrecords.NewViewedPromptOutcomeComplete(complete)},"alternate",nil
}
func TestGeneratedInputRounds(t *testing.T){
 service:=&recordService{}
 endpoints:=genrecords.NewEndpoints(service)
 var middleware atomic.Int64
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,input any)(any,error){middleware.Add(1);return next(ctx,input)}})
 mux:=goahttp.NewMuxer()
 adapter:=genmcp.NewMCPAdapter(endpoints,nil)
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 peer:=httptest.NewServer(mux);defer peer.Close()
 client:=&http.Client{Transport:authorizedTransport{peer.Client().Transport}}
 caller,err:=mcpruntime.NewHTTPCaller(mcpruntime.HTTPOptions{Endpoint:peer.URL+"/mcp",Client:client,ClientInfo:mcpruntime.ClientInfo{Name:"typed-host",Version:"1"},InputSupport:mcpruntime.InputSupport{Form:true,URL:true}})
 require.NoError(t,err)
 for _,test:=range []struct{target,id,answer,want string}{
  {"form","profile","{\"action\":\"accept\",\"content\":{\"label\":\"selected\",\"extension\":true,\"LABEL\":\"must-not-replace-selected\"},\"ACTION\":\"cancel\",\"CONTENT\":false}","selected"},
  {"form","profile","{\"action\":\"accept\",\"content\":{\"label\":\"integral\",\"itemCount\":3.0}}","integral"},
  {"form","profile","{\"action\":\"decline\"}","declined"},
  {"form","profile","{\"action\":\"cancel\"}","cancelled"},
  {"url","payment","{\"action\":\"accept\"}","approved"},
  {"url","payment","{\"action\":\"decline\"}","declined"},
  {"url","payment","{\"action\":\"cancel\"}","cancelled"},
 }{
  t.Run(test.target+test.want,func(t *testing.T){
   payload:=json.RawMessage("{\"target\":\""+test.target+"\"}")
   initial,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:payload})
   require.NoError(t,err);require.NotNil(t,initial.InputRequired);require.NotNil(t,initial.InputRequired.RequestState)
   assert.Equal(t,"",*initial.InputRequired.RequestState);assert.Contains(t,initial.InputRequired.Requests,test.id)
   request:=initial.InputRequired.Requests[test.id]
   if test.target=="form"{assert.Contains(t,string(request.Params),"requestedSchema");assert.NotContains(t,string(request.Params),"additionalProperties");assert.Contains(t,string(request.Params),"itemCount")}else{assert.Contains(t,string(request.Params),"https://consent.example/approve")}
   missing,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:payload,Continuation:&mcpruntime.CallContinuation{RequestState:initial.InputRequired.RequestState,InputResponses:map[string]json.RawMessage{"unknown":json.RawMessage("{}")}}})
   require.NoError(t,err);assert.NotNil(t,missing.InputRequired)
   complete,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:payload,Continuation:&mcpruntime.CallContinuation{RequestState:initial.InputRequired.RequestState,InputResponses:map[string]json.RawMessage{test.id:json.RawMessage(test.answer)}}})
   require.NoError(t,err);assert.Nil(t,complete.InputRequired);assert.JSONEq(t,"\""+test.want+"\"",string(complete.StructuredContent))
  })
 }
 assert.EqualValues(t,21,service.calls.Load());assert.Equal(t,service.calls.Load(),service.auth.Load());assert.Equal(t,service.calls.Load(),middleware.Load())
 for _,test:=range []struct{name,want string}{
  {"read_fixed","\"viewed\""},{"read_selected","{\"type\":\"alternate\",\"value\":\"viewed\"}"},
 }{
  initial,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:test.name,Payload:json.RawMessage("{}")})
  require.NoError(t,err);require.NotNil(t,initial.InputRequired);require.NotNil(t,initial.InputRequired.RequestState)
  complete,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:test.name,Payload:json.RawMessage("{}"),Continuation:&mcpruntime.CallContinuation{RequestState:initial.InputRequired.RequestState,InputResponses:map[string]json.RawMessage{"profile":json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"viewed\"}}")}}})
  require.NoError(t,err);assert.Nil(t,complete.InputRequired);assert.JSONEq(t,test.want,string(complete.StructuredContent))
 }
 // This independent peer owns each HTTP round; the framework owns neither
 // its request encoding nor its result decoding.
 sdkClient:=sdk.NewClient(&sdk.Implementation{Name:"independent-input-host",Version:"1"},&sdk.ClientOptions{Capabilities:&sdk.ClientCapabilities{Elicitation:&sdk.ElicitationCapabilities{Form:&sdk.FormElicitationCapabilities{},URL:&sdk.URLElicitationCapabilities{}}},MultiRoundTrip:&sdk.MultiRoundTripOptions{Disabled:true}})
 sdkSession,err:=sdkClient.Connect(t.Context(),&sdk.StreamableClientTransport{Endpoint:peer.URL+"/mcp",HTTPClient:client,MaxRetries:-1},&sdk.ClientSessionOptions{ProtocolVersion:mcpruntime.ProtocolVersion})
 require.NoError(t,err)
 defer func(){assert.NoError(t,sdkSession.Close())}()
 sdkInitial,err:=sdkSession.CallTool(t.Context(),&sdk.CallToolParams{Name:"read",Arguments:json.RawMessage("{\"target\":\"interop\"}")})
 require.NoError(t,err);assert.True(t,sdkInitial.NeedsInput());assert.Equal(t,"opaque-for-independent-peer",sdkInitial.RequestState)
 sdkComplete,err:=sdkSession.CallTool(t.Context(),&sdk.CallToolParams{Name:"read",Arguments:json.RawMessage("{\"target\":\"interop\"}"),RequestState:sdkInitial.RequestState,InputResponses:sdk.InputResponseMap{"profile":&sdk.ElicitResult{Action:"accept",Content:map[string]any{"label":"independent"}}}})
 require.NoError(t,err);assert.False(t,sdkComplete.NeedsInput());assert.Equal(t,"independent",sdkComplete.StructuredContent)
 // Raw requests exercise metadata shapes that truthful client constructors
 // intentionally do not emit, including the current form-only shorthand.
 for _,test:=range []struct{capabilities string; code int}{
  {"{\"elicitation\":{}}",0},
  {"{\"elicitation\":{\"url\":{}}}",mcpruntime.MissingRequiredClientCapability},
  {"{\"elicitation\":{\"form\":null}}",mcpruntime.JSONRPCInvalidParams},
  {"{\"elicitation\":null}",mcpruntime.JSONRPCInvalidParams},
 }{
  body:="{\"jsonrpc\":\"2.0\",\"id\":\"capability-check\",\"method\":\"tools/call\",\"params\":{\"name\":\"read\",\"arguments\":{\"target\":\"form\"},\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\""+mcpruntime.ProtocolVersion+"\",\"io.modelcontextprotocol/clientCapabilities\":"+test.capabilities+"}}}"
  request,err:=http.NewRequestWithContext(t.Context(),http.MethodPost,peer.URL+"/mcp",strings.NewReader(body));require.NoError(t,err)
  request.Header.Set("Content-Type","application/json");request.Header.Set("Accept","application/json, text/event-stream");request.Header.Set("MCP-Protocol-Version",mcpruntime.ProtocolVersion);request.Header.Set("Mcp-Method","tools/call");request.Header.Set("Mcp-Name","read")
  response,err:=client.Do(request);require.NoError(t,err)
  data,err:=io.ReadAll(response.Body);require.NoError(t,err);require.NoError(t,response.Body.Close())
  var reply struct{Error *struct{Code int;Message string}; Result struct{ResultType string}}
  require.NoError(t,json.Unmarshal(data,&reply))
  if test.code==0{assert.Nil(t,reply.Error,string(data));assert.Equal(t,"input_required",reply.Result.ResultType)}else{require.NotNil(t,reply.Error,string(data));assert.Equal(t,test.code,reply.Error.Code,string(data))}
 }
 for _,target:=range []string{"empty","state_only"}{
  result,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:json.RawMessage("{\"target\":\""+target+"\"}")})
  require.NoError(t,err);require.NotNil(t,result.InputRequired);assert.Empty(t,result.InputRequired.Requests)
  if target=="empty"{
   assert.Nil(t,result.InputRequired.RequestState)
   complete,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:json.RawMessage("{\"target\":\"empty\"}"),Continuation:&mcpruntime.CallContinuation{}})
   require.NoError(t,err);assert.Nil(t,complete.InputRequired);assert.JSONEq(t,"\"empty-continuation\"",string(complete.StructuredContent))
  }else{require.NotNil(t,result.InputRequired.RequestState);assert.Equal(t,"",*result.InputRequired.RequestState)}
 }
 for _,target:=range []string{"absent","invalid"}{
  _,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:json.RawMessage("{\"target\":\""+target+"\"}")})
  var failure *mcpruntime.Error;require.ErrorAs(t,err,&failure);assert.Equal(t,mcpruntime.JSONRPCInternalError,failure.Code)
 }
 for _,answer:=range []string{
  "{\"action\":\"accept\",\"content\":{\"label\":\"\"}}",
  "{\"action\":\"accept\",\"content\":{\"label\":\"ok\",\"extra\":{}}}",
  "{\"action\":\"accept\",\"content\":null}",
  "{\"action\":\"accept\",\"content\":{\"LABEL\":\"cannot-fill-required-label\"}}",
  "{\"action\":\"decline\",\"content\":{}}",
 }{
  before:=service.calls.Load();state:=""
  _,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:json.RawMessage("{\"target\":\"form\"}"),Continuation:&mcpruntime.CallContinuation{RequestState:&state,InputResponses:map[string]json.RawMessage{"profile":json.RawMessage(answer)}}})
  assert.Error(t,err);assert.Equal(t,before,service.calls.Load())
 }
 for _,payload:=range []string{"{\"target\":\"form\",\"continuation\":{}}","{\"target\":\"form\",\"credential\":\"injected\"}"}{
  before:=service.calls.Load();_,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:json.RawMessage(payload)});assert.Error(t,err);assert.Equal(t,before,service.calls.Load())
 }
 location,err:=url.Parse(peer.URL);require.NoError(t,err)
 typedClient:=genclient.NewClient(location.Scheme,location.Host,client,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 typedClient.Doer=mcpruntime.NewHTTPTransport(typedClient.Doer,mcpruntime.ClientInfo{Name:"typed-host",Version:"1"},mcpruntime.HTTPBindings{},mcpruntime.InputSupport{Form:true,URL:true},mcpruntime.HTTPRetryPolicy{})
 typedEmpty,err:=typedClient.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"read",Arguments:json.RawMessage("{\"target\":\"empty\"}"),InputResponses:map[string]json.RawMessage{}})
 require.NoError(t,err);typedComplete,ok:=typedEmpty.(*genmcp.ToolsCallResult).Outcome.AsComplete();require.True(t,ok);assert.JSONEq(t,"\"empty-continuation\"",string(typedComplete.StructuredContent))
 resource,err:=typedClient.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"record://reference"})
 require.NoError(t,err)
 unfinished,ok:=resource.(*genmcp.ResourcesReadResult).Outcome.AsInputRequired();require.True(t,ok);require.NotNil(t,unfinished.RequestState)
 resource,err=typedClient.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"record://reference",RequestState:unfinished.RequestState,InputResponses:map[string]json.RawMessage{"profile":json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"document\"}}")}})
 require.NoError(t,err)
 document,ok:=resource.(*genmcp.ResourcesReadResult).Outcome.AsComplete();require.True(t,ok);require.Len(t,document.Contents,1);require.NotNil(t,document.Contents[0].Text);assert.Equal(t,"document",*document.Contents[0].Text)
 prompt,err:=typedClient.PromptsGet()(t.Context(),&genmcp.PromptsGetPayload{Name:"review"})
 require.NoError(t,err)
 unfinished,ok=prompt.(*genmcp.PromptsGetResult).Outcome.AsInputRequired();require.True(t,ok)
 prompt,err=typedClient.PromptsGet()(t.Context(),&genmcp.PromptsGetPayload{Name:"review",RequestState:unfinished.RequestState,InputResponses:map[string]json.RawMessage{"profile":json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"instructions\"}}")}})
 require.NoError(t,err)
 instructions,ok:=prompt.(*genmcp.PromptsGetResult).Outcome.AsComplete();require.True(t,ok);require.Len(t,instructions.Messages,1);assert.Equal(t,"instructions",*instructions.Messages[0].Content.Text)
 prompt,err=typedClient.PromptsGet()(t.Context(),&genmcp.PromptsGetPayload{Name:"review_selected"})
 require.NoError(t,err)
 unfinished,ok=prompt.(*genmcp.PromptsGetResult).Outcome.AsInputRequired();require.True(t,ok)
 prompt,err=typedClient.PromptsGet()(t.Context(),&genmcp.PromptsGetPayload{Name:"review_selected",RequestState:unfinished.RequestState,InputResponses:map[string]json.RawMessage{"profile":json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"viewed instructions\"}}")}})
 require.NoError(t,err)
 instructions,ok=prompt.(*genmcp.PromptsGetResult).Outcome.AsComplete();require.True(t,ok);require.Len(t,instructions.Messages,1);assert.Equal(t,"viewed instructions",*instructions.Messages[0].Content.Text)
 assert.Equal(t,service.calls.Load(),service.auth.Load());assert.Equal(t,service.calls.Load(),middleware.Load())
 taskCaller,createErr:=genclient.NewCaller(typedClient,mcpruntime.ClientInfo{Name:"task-route-host",Version:"1"},mcpruntime.InputSupport{Form:true,URL:true},mcpruntime.HTTPRetryPolicy{})
 require.NoError(t,createErr)
 _,taskErr:=taskCaller.GetTask(t.Context(),"unknown-task")
 var missingMethod *mcpruntime.Error
 require.ErrorAs(t,taskErr,&missingMethod);assert.Equal(t,mcpruntime.JSONRPCMethodNotFound,missingMethod.Code)
 taskErr=taskCaller.UpdateTask(t.Context(),"unknown-task",map[string]json.RawMessage{})
 require.ErrorAs(t,taskErr,&missingMethod);assert.Equal(t,mcpruntime.JSONRPCMethodNotFound,missingMethod.Code)
 taskErr=taskCaller.CancelTask(t.Context(),"unknown-task")
 require.ErrorAs(t,taskErr,&missingMethod);assert.Equal(t,mcpruntime.JSONRPCMethodNotFound,missingMethod.Code)
 unsupported,err:=mcpruntime.NewHTTPCaller(mcpruntime.HTTPOptions{Endpoint:peer.URL+"/mcp",Client:client,ClientInfo:mcpruntime.ClientInfo{Name:"unsupported-host",Version:"1"}})
 require.NoError(t,err)
 _,err=unsupported.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"read",Payload:json.RawMessage("{\"target\":\"form\"}")})
 var protocolError *mcpruntime.Error
 require.ErrorAs(t,err,&protocolError);assert.Equal(t,mcpruntime.MissingRequiredClientCapability,protocolError.Code)
 assert.JSONEq(t,"{\"requiredCapabilities\":{\"elicitation\":{\"form\":{}}}}",string(protocolError.Data))
}
`
