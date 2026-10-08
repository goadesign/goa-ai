// The job owner in this fixture is synthetic. It exercises task identity,
// retained native authorization, typed host input and exact result conversion.
package codegen

const taskOwnerDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _=API("task-peer",func(){Title("Typed job owner")})
var jwt=JWTSecurity("jwt",func(){Scope("jobs","Read and update synthetic jobs")})
var owner=Type("OwnerID",String,func(){MinLength(1)})
var empty=Type("Empty",func(){})
var content=Type("FormContent",func(){Field(1,"label",String,"Label supplied by the host");Required("label")})
var accept=Type("AcceptedForm",func(){Field(1,"content",content,"Accepted form content");Required("content")})
var answer=Type("FormResponse",func(){
 OneOf("answer","Host decision",func(){TypeName("FormDecision");Attribute("accept",accept,"Accepted form");Attribute("decline",empty,"Declined form");Attribute("cancel",empty,"Cancelled form")})
 Required("answer")
})
var question=Type("FormQuestion",func(){Field(1,"message",String,"Question shown to the host",func(){Meta("struct:field:name","PromptText")});Required("message")})
var request=Type("JobRequest",func(){OneOf("request","Typed job question",func(){TypeName("QuestionKind");Attribute("profile",question,"Request a profile")});Required("request")})
var response=Type("JobResponse",func(){OneOf("response","Typed host answer",func(){TypeName("ResponseKind");Attribute("profile",answer,"Answer a profile")});Required("response")})
var pending=Type("JobInput",func(){Field(1,"requests",MapOf(String,request),"All outstanding questions");Required("requests")})
var metadata=Type("JobMetadata",func(){
 Field(1,"taskId",String,"Native durable job identity",func(){Meta("struct:field:name","NativeID")})
 Field(2,"createdAt",String,"Creation time");Field(3,"lastUpdatedAt",String,"Observation time")
 Field(4,"ttlMs",Int64,"Retention, absent for unlimited")
 Field(5,"pollIntervalMs",Int64,"Suggested polling interval",func(){Default(3)})
 Required("taskId","createdAt","lastUpdatedAt")
})
var failure=Type("JobFailure",func(){
 Field(1,"code",Int,"Explicit JSON-RPC execution error code")
 Field(2,"message",String,"Execution error description")
 Field(3,"data",Any,"Optional exact error data",func(){Meta("struct:field:type","rawjson.Message","goa.design/goa-ai/runtime/agent/rawjson")})
 Required("code","message")
})
var observation=Type("Observation",func(){
 Field(1,"task",metadata,"Current job metadata",func(){Meta("struct:field:name","JobInfo")})
 OneOf("outcome","Current state and associated data",func(){TypeName("JobState");Attribute("working",empty,"Work continues");Attribute("input_required",pending,"Outstanding questions");Attribute("complete",String,"Finished label");Attribute("failed",failure,"Execution error");Attribute("cancelled",empty,"Cancellation completed")})
 Required("task","outcome")
})
var _=Service("jobs",func(){
 Description("Owns durable synthetic jobs and accepts host input.")
 JSONRPC(func(){POST("/owners/{tenant}/mcp");Param("ownerId:tenant")})
 MCP("jobs","1")
 Method("create",func(){
  Security(jwt,func(){Scope("jobs")})
  Description("Starts a durable job whose state is immediately readable.")
  Payload(func(){Token("credential",String,"Native bearer credential");Field(1,"ownerId",owner,"Authorized owner",func(){Meta("struct:field:name","OwnerContext")});Field(2,"query",String,"Job to create");Required("credential","ownerId","query")})
  Result(observation);TaskExchange("read","answer","cancel");Tool("create","Create a durable synthetic job")
 })
 Method("read",func(){
  Security(jwt,func(){Scope("jobs")})
  Description("Authorizes an existing job read and reports its current state.")
  Payload(func(){Token("credential",String,"Native bearer credential");Field(1,"ownerId",owner,"Authorized owner",func(){Meta("struct:field:name","OwnerIdentity")});Field(2,"taskId",String,"Native job identity",func(){Meta("struct:field:name","JobID")});Field(3,"selection",String,"Read profile selected by this method",func(){Default("standard");Enum("standard")});Required("credential","ownerId","taskId")})
  Result(observation)
 })
 Method("answer",func(){
  Security(jwt,func(){Scope("jobs")})
  Description("Accepts typed answers for currently outstanding job questions.")
  Payload(func(){Token("credential",String,"Native bearer credential");Field(1,"ownerId",owner,"Authorized owner");Field(2,"taskId",String,"Native job identity");Field(3,"responses",MapOf(String,response),"Host answers",func(){Meta("struct:field:name","HostAnswers")});Required("credential","ownerId","taskId","responses")})
 })
 Method("cancel",func(){
  Security(jwt,func(){Scope("jobs")})
  Description("Acknowledges a cooperative request to cancel the identified job.")
  Payload(func(){Token("credential",String,"Native bearer credential");Field(1,"ownerId",owner,"Authorized owner");Field(2,"taskId",String,"Native job identity");Required("credential","ownerId","taskId")})
 })
})
`

const taskOwnerRuntime = `package taskpeer
import (
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "net/url"
 "sync"
 "testing"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 "goa.design/goa-ai/runtime/agent/rawjson"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
 genclient "task-peer.local/gen/jsonrpc/mcp_jobs/client"
 genserver "task-peer.local/gen/jsonrpc/mcp_jobs/server"
 genmcp "task-peer.local/gen/mcp_jobs"
 genjobs "task-peer.local/gen/jobs"
)
type (
 principal struct{}
 jobOwner struct{mu sync.Mutex; state genjobs.JobState; starts,reads,answers,cancels,auth,middleware int; ttl *int64; badID bool}
 authorizedTransport struct{transport http.RoundTripper}
)
const nativeID="job / α"
func(c authorizedTransport)RoundTrip(r *http.Request)(*http.Response,error){r=r.Clone(r.Context());r.Header.Set("Authorization","Bearer accepted");return c.transport.RoundTrip(r)}
func(s *jobOwner)JWTAuth(ctx context.Context,token string,scheme *security.JWTScheme)(context.Context,error){
 s.mu.Lock();defer s.mu.Unlock();s.auth++
 if token!="accepted"||len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="jobs"{return ctx,errors.New("wrong native credential or scope")}
 return context.WithValue(ctx,principal{},true),nil
}
func(s *jobOwner)observation()*genjobs.Observation{
 id:=nativeID;if s.badID{id="different"}
 return &genjobs.Observation{JobInfo:&genjobs.JobMetadata{NativeID:id,CreatedAt:"2026-10-08T01:00:00Z",LastUpdatedAt:"2026-10-08T01:00:01Z",TTLMs:s.ttl,PollIntervalMs:3},Outcome:s.state}
}
func authorize(ctx context.Context,credential string,owner genjobs.OwnerID,id string)error{if ctx.Value(principal{})!=true||credential!="accepted"||owner!="owner"||id!=nativeID{return errors.New("native authorization or job identity changed")};return nil}
func(s *jobOwner)Create(ctx context.Context,p *genjobs.CreatePayload)(*genjobs.Observation,error){
 s.mu.Lock();defer s.mu.Unlock();if err:=authorize(ctx,p.Credential,p.OwnerContext,nativeID);err!=nil{return nil,err};s.starts++;return s.observation(),nil
}
func(s *jobOwner)Read(ctx context.Context,p *genjobs.ReadPayload)(*genjobs.Observation,error){
 s.mu.Lock();defer s.mu.Unlock();if err:=authorize(ctx,p.Credential,p.OwnerIdentity,p.JobID);err!=nil{return nil,err};if p.Selection!="standard"{return nil,errors.New("role default was not applied")};s.reads++;return s.observation(),nil
}
func(s *jobOwner)Answer(ctx context.Context,p *genjobs.AnswerPayload)error{
 s.mu.Lock();defer s.mu.Unlock();if err:=authorize(ctx,p.Credential,p.OwnerID,p.TaskID);err!=nil{return err};s.answers++
 if reply:=p.HostAnswers["question / α"];reply!=nil{answer,ok:=reply.Response.AsProfile();if !ok{return errors.New("answer kind changed")};accepted,ok:=answer.Answer.AsAccept();if !ok{return errors.New("answer action changed")};s.state=genjobs.NewJobStateComplete(genjobs.JobStateBranchComplete(accepted.Content.Label))}
 return nil
}
func(s *jobOwner)Cancel(ctx context.Context,p *genjobs.CancelPayload)error{
 s.mu.Lock();defer s.mu.Unlock();if err:=authorize(ctx,p.Credential,p.OwnerID,p.TaskID);err!=nil{return err};s.cancels++;return nil
}
func TestTaskLifecycle(t *testing.T){
 service:=&jobOwner{state:genjobs.NewJobStateWorking(&genjobs.Empty{})}
 endpoints:=genjobs.NewEndpoints(service)
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,p any)(any,error){service.mu.Lock();service.middleware++;service.mu.Unlock();return next(ctx,p)}})
 adapter:=genmcp.NewMCPAdapter(endpoints,nil)
 mux:=goahttp.NewMuxer()
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 peer:=httptest.NewServer(mux);defer peer.Close()
 httpClient:=&http.Client{Transport:authorizedTransport{transport:peer.Client().Transport}}
 transport:=mcpruntime.NewHTTPTransport(httpClient,mcpruntime.ClientInfo{Name:"task-host",Version:"1"},mcpruntime.HTTPBindings{},mcpruntime.InputSupport{Form:true},mcpruntime.HTTPRetryPolicy{})
 endpoint:=peer.URL+"/owners/owner/mcp"
 _,err:=transport.CallTool(t.Context(),endpoint,mcpruntime.CallRequest{Tool:"create",Payload:json.RawMessage("{\"query\":\"report\"}")})
 var missing *mcpruntime.Error;require.ErrorAs(t,err,&missing);assert.Equal(t,mcpruntime.MissingRequiredClientCapability,missing.Code);assert.JSONEq(t,"{\"requiredCapabilities\":{\"extensions\":{\"io.modelcontextprotocol/tasks\":{}}}}",string(missing.Data));assert.Zero(t,service.starts);assert.Zero(t,service.middleware)
 created,err:=transport.CallTool(mcpruntime.WithTaskSupport(t.Context()),endpoint,mcpruntime.CallRequest{Tool:"create",Payload:json.RawMessage("{\"query\":\"report\"}")})
 require.NoError(t,err);require.NotNil(t,created.Task);assert.Equal(t,mcpruntime.TaskWorking,created.Task.Status);assert.Nil(t,created.Task.TTLMs);id:=created.Task.TaskID;assert.NotEqual(t,nativeID,id);assert.Equal(t,1,service.starts)
 task,err:=transport.GetTask(t.Context(),endpoint,id);require.NoError(t,err);assert.Equal(t,mcpruntime.TaskWorking,task.Info().Status)
 service.mu.Lock();service.state=genjobs.NewJobStateInputRequired(&genjobs.JobInput{Requests:map[string]*genjobs.JobRequest{"question / α":{Request:genjobs.NewQuestionKindProfile(&genjobs.FormQuestion{PromptText:"Choose a label"})}}});service.mu.Unlock()
 task,err=transport.GetTask(t.Context(),endpoint,id);require.NoError(t,err);input,ok:=task.AsInputRequired();require.True(t,ok);require.Len(t,input.Requests,1);key:="";for name:=range input.Requests{key=name};assert.NotEqual(t,"question / α",key)
 require.NoError(t,transport.UpdateTask(t.Context(),endpoint,id,map[string]json.RawMessage{}));assert.Equal(t,1,service.answers)
 require.NoError(t,transport.UpdateTask(t.Context(),endpoint,id,map[string]json.RawMessage{key:json.RawMessage("{\"action\":\"accept\",\"content\":{\"label\":\"done\"}}"),"unknown":json.RawMessage("{}")}))
 task,err=transport.GetTask(t.Context(),endpoint,id);require.NoError(t,err);finished,ok:=task.AsCompleted();require.True(t,ok);assert.JSONEq(t,"\"done\"",string(finished.StructuredContent));assert.Equal(t,1,service.starts)
 require.NoError(t,transport.CancelTask(t.Context(),endpoint,id));task,err=transport.GetTask(t.Context(),endpoint,id);require.NoError(t,err);assert.Equal(t,mcpruntime.TaskCompleted,task.Info().Status)
 service.mu.Lock();service.state=genjobs.NewJobStateCancelled(&genjobs.Empty{});service.mu.Unlock();task,err=transport.GetTask(t.Context(),endpoint,id);require.NoError(t,err);assert.Equal(t,mcpruntime.TaskCancelled,task.Info().Status)
 service.mu.Lock();service.state=genjobs.NewJobStateFailed(&genjobs.JobFailure{Code:-32603,Message:"job failed",Data:rawjson.Message("{\"integer\":9007199254740993}")});service.mu.Unlock();task,err=transport.GetTask(t.Context(),endpoint,id);require.NoError(t,err);failure,ok:=task.AsFailed();require.True(t,ok);var protocol *mcpruntime.Error;require.ErrorAs(t,failure,&protocol);assert.Equal(t,-32603,protocol.Code);assert.JSONEq(t,"{\"integer\":9007199254740993}",string(protocol.Data))
 service.mu.Lock();service.badID=true;service.mu.Unlock();_,err=transport.GetTask(t.Context(),endpoint,id);require.ErrorAs(t,err,&protocol);assert.Equal(t,mcpruntime.JSONRPCInternalError,protocol.Code)
 service.mu.Lock();service.badID=false;service.state=genjobs.NewJobStateWorking(&genjobs.Empty{});service.mu.Unlock()
 for _,ttl:=range []int64{0,9007199254740993}{service.mu.Lock();value:=ttl;service.ttl=&value;service.mu.Unlock();task,err=transport.GetTask(t.Context(),endpoint,id);require.NoError(t,err);require.NotNil(t,task.Info().TTLMs);assert.Equal(t,ttl,*task.Info().TTLMs)}
 location,err:=url.Parse(peer.URL);require.NoError(t,err);typed:=genclient.NewClient(location.Scheme,location.Host,httpClient,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 typed.Doer=transport
 discovered,err:=typed.ServerDiscover()(t.Context(),&genmcp.ServerDiscoverPayload{HTTPPath0:"owner"});require.NoError(t,err);assert.JSONEq(t,"{\"io.modelcontextprotocol/tasks\":{}}",string(discovered.(*genmcp.DiscoverResult).Capabilities.Extensions))
 raw,err:=typed.TasksGet()(t.Context(),&genmcp.TasksGetPayload{TaskID:id,HTTPPath0:"owner"});require.NoError(t,err);working,ok:=raw.(*genmcp.TasksGetResult).Outcome.AsWorking();require.True(t,ok);assert.Equal(t,id,working.TaskID);assert.Equal(t,int64(9007199254740993),*working.TTLMs)
 before:=service.reads
 for _,bad:=range []string{"unknown.AQ","Y3JlYXRl.invalid=","missing separator"}{_,err=transport.GetTask(t.Context(),endpoint,bad);assert.Error(t,err)}
 assert.Equal(t,before,service.reads);assert.Equal(t,service.middleware,service.auth)
 list,err:=transport.CallTool(mcpruntime.WithTaskSupport(t.Context()),endpoint,mcpruntime.CallRequest{Tool:"create",Payload:json.RawMessage("{\"ownerId\":\"injected\",\"query\":\"report\"}")});assert.Error(t,err);assert.Nil(t,list.Task)
}
`
