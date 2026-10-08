// Package tests checks generated native job observations and typed host answers.
// The shared converters must preserve job identities, return only finished domain
// output, and retain the map keys and union branches authored by the service.
package tests

import (
	"strings"
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGeneratedNativeTaskConversions(t *testing.T) {
	for _, tc := range []struct {
		name           string
		defaultPolling bool
	}{
		{"optional polling", false},
		{"default polling", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := buildCompleteGeneratedFiles(t, testscenarios.NativeTaskExchange(tc.defaultPolling))
			root := writeCompleteGeneratedModule(t, files)
			writeGeneratedPackageTest(t, root, "gen/jobs/toolsets/reports/task_exchange_test.go", nativeTaskConversionTests)
			runGeneratedPackageTest(t, root, "./gen/jobs/...")
		})
	}
}

func TestGeneratedNativeTaskCreationInput(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.NativeTaskExchangeWithInput())
	root := writeCompleteGeneratedModule(t, files)
	source := strings.Replace(nativeTaskConversionTests, "(*genjobs.JobObservation,error) {\n if p.OwnerContext", "(*genjobs.CreationOutcome,error) {\n if p.OwnerContext", 1)
	source = strings.Replace(source, "return observation(genjobs.NewJobStateWorking(&genjobs.Empty{})),nil", "return &genjobs.CreationOutcome{Outcome:genjobs.NewCreationStateComplete(observation(genjobs.NewJobStateWorking(&genjobs.Empty{})))},nil", 1)
	source = strings.Replace(source, "lastState genjobs.JobState }", "lastState genjobs.JobState; askBeforeCreation bool }", 1)
	source = strings.Replace(source, "s.creates++", `s.creates++
 if s.askBeforeCreation {
  if p.HostInput==nil {
   state:="permission"
   return &genjobs.CreationOutcome{Outcome:genjobs.NewCreationStateInputRequired(&genjobs.CreationPending{State:&state})},nil
  }
  if p.HostInput.State==nil||*p.HostInput.State!="permission" { return nil,errors.New("creation state changed") }
 }`, 1)
	source += nativeTaskCreationPauseTests
	writeGeneratedPackageTest(t, root, "gen/jobs/toolsets/reports/task_exchange_test.go", source)
	runGeneratedPackageTest(t, root, "./gen/jobs/...")
}

func TestGeneratedNativeTaskSharedRoles(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.NativeTaskExchangeSharedRoles())
	root := writeCompleteGeneratedModule(t, files)
	source := strings.Replace(nativeTaskConversionTests, "endpoints.Create,endpoints.Read", "endpoints.Create,endpoints.CreateShared,endpoints.Read", 1)
	start := strings.Index(source, "func (s *jobService) Create(")
	end := strings.Index(source[start:], "\nfunc (") + start
	creator := source[start:end]
	source += strings.Replace(strings.Replace(creator, ") Create(", ") CreateShared(", 1), "*genjobs.CreatePayload", "*genjobs.CreateSharedPayload", 1)
	for _, name := range []string{"TestNativeTaskDispatchUsesExistingMethods", "TestRegistryNativeTaskDispatchUsesSameContracts"} {
		start := strings.Index(source, "func "+name+"(")
		end := strings.Index(source[start:], "\nfunc ")
		if end < 0 {
			end = len(source) - start
		}
		method := source[start : start+end]
		method = strings.Replace(method, name, name+"ForSharedCreator", 1)
		method = strings.ReplaceAll(method, "reports.create", "reports.create_shared")
		method = strings.ReplaceAll(method, "genreports.CreateResult", "genreports.CreateSharedResult")
		method = strings.ReplaceAll(method, "Tool:genreports.Create,", "Tool:genreports.CreateShared,")
		method = strings.ReplaceAll(method, "case *genjobs.CreatePayload:", "case *genjobs.CreateSharedPayload:")
		source += "\n" + method
	}
	writeGeneratedPackageTest(t, root, "gen/jobs/toolsets/reports/task_exchange_test.go", source)
	runGeneratedPackageTest(t, root, "./gen/jobs/...")
}

// TestGeneratedNativeTaskViews keeps the ordinary native tool result contract
// when a service returns its separate HTTP view selection alongside the result.
func TestGeneratedNativeTaskViews(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.NativeTaskExchangeViews())
	root := writeCompleteGeneratedModule(t, files)
	source := nativeTaskConversionTests
	for _, name := range []string{"Create", "Read"} {
		start := strings.Index(source, "func (s *jobService) "+name+"(")
		end := strings.Index(source[start:], "\nfunc (") + start
		method := source[start:end]
		method = strings.Replace(method, "(*genjobs.JobObservation,error)", "(*genjobs.JobObservation,string,error)", 1)
		method = strings.ReplaceAll(method, "return nil,errors.New(", "return nil,\"\",errors.New(")
		method = strings.ReplaceAll(method, "),nil", "),\"alternate\",nil")
		source = source[:start] + method + source[end:]
	}
	source = strings.Replace(source, "genexecutor.WithInterceptors(genexecutor.ToolInterceptorFunc(", `genexecutor.WithCreate(func(ctx context.Context,input any)(any,error){result,_,err:=service.Create(ctx,input.(*genjobs.CreatePayload));return result,err}),genexecutor.WithCreateTaskRead(func(ctx context.Context,input any)(any,error){result,_,err:=service.Read(ctx,input.(*genjobs.ReadPayload));return result,err}),genexecutor.WithInterceptors(genexecutor.ToolInterceptorFunc(`, 1)
	writeGeneratedPackageTest(t, root, "gen/jobs/toolsets/reports/task_exchange_test.go", source)
	runGeneratedPackageTest(t, root, "./gen/jobs/...")
}

const nativeTaskConversionTests = `package reports_test

import (
 "context"
 "encoding/json"
 "encoding/base64"
 "errors"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genjobs "generated.local/gen/jobs"
 genexecutor "generated.local/gen/jobs/agents/worker/reports"
 gentooloperations "goa.design/goa-ai/registry/gen/tooloperations"
 "goa.design/goa-ai/runtime/agent/api"
 "goa.design/goa-ai/runtime/agent/runtime"
 "goa.design/goa-ai/runtime/toolregistry"
 genreports "generated.local/gen/jobs/toolsets/reports"
 "goa.design/goa-ai/runtime/mcp"
 "goa.design/goa-ai/runtime/agent/rawjson"
)

func observation(state genjobs.JobState) *genjobs.JobObservation {
 return &genjobs.JobObservation{Task:&genjobs.JobMetadata{TaskID:" / job α",CreatedAt:"created",LastUpdatedAt:"updated"},Outcome:state}
}

func TestCreatedJobIsAlwaysReadSeparately(t *testing.T) {
 value:=observation(genjobs.NewJobStateComplete("finished immediately"))
 pending,err:=genreports.CreatedCreateTask(value)
 require.NoError(t,err)
 id,_,ok:=pending.AsTaskWait()
 require.True(t,ok)
 assert.Equal(t," / job α",id)
 result,next,err:=genreports.ReadCreateTask(value,id)
 require.NoError(t,err)
 assert.Equal(t,"finished immediately",string(result))
 assert.Nil(t,next)
 _,_,err=genreports.ReadCreateTask(value,"another job")
 require.ErrorContains(t,err,"changed its identifier")
}

func TestTaskRoleInputsKeepContextAndSavedIdentity(t *testing.T) {
 source:=&genjobs.CreatePayload{Query:"report",OwnerContext:genjobs.OwnerID("owner")}
 read:=genreports.ToCreateTaskReadPayload(source)
 assert.Equal(t,source.OwnerContext,read.OwnerIdentity)
 assert.Equal(t,"standard",read.Selection)
 read.TaskID="interceptor value"
 require.NoError(t,genreports.FillCreateTaskReadInput(read,"saved job"))
 assert.Equal(t,"saved job",read.TaskID)
 cancel:=genreports.ToCreateTaskCancelPayload(source)
 assert.Equal(t,source.OwnerContext,cancel.OwnerIdentity)
 require.NoError(t,genreports.FillCreateTaskCancelInput(cancel,"saved job"))
 assert.Equal(t,"saved job",cancel.TaskID)
 read.OwnerIdentity=""
 require.Error(t,genreports.FillCreateTaskReadInput(read,"saved job"),"complete native payload validation follows input injection")
}

func TestExistingJobStates(t *testing.T) {
 for _,tc:=range []struct {name string; state genjobs.JobState; terminal error}{
  {"working",genjobs.NewJobStateWorking(&genjobs.Empty{}),nil},
  {"cancelled",genjobs.NewJobStateCancelled(&genjobs.Empty{}),context.Canceled},
  {"failed",genjobs.NewJobStateFailed(&genjobs.JobFailure{Code:-32001,Message:"job failed"}),nil},
 } {
  t.Run(tc.name,func(t *testing.T){
   result,pending,err:=genreports.ReadCreateTask(observation(tc.state)," / job α")
   assert.Empty(t,result)
   if tc.name=="working" {require.NoError(t,err);require.NotNil(t,pending);return}
   assert.Nil(t,pending)
   if tc.terminal!=nil {require.ErrorIs(t,err,tc.terminal);return}
   var failure *mcp.Error
   require.True(t,errors.As(err,&failure))
   assert.Equal(t,-32001,failure.Code)
   assert.Equal(t,"job failed",failure.Message)
  })
 }
}

func TestJobErrorDetailsRetainExactJSONAndPresence(t *testing.T) {
 for _,details:=range []rawjson.Message{nil,rawjson.Message("null"),rawjson.Message("{\"number\":9007199254740993}")} {
  value:=observation(genjobs.NewJobStateFailed(&genjobs.JobFailure{Code:-32001,Message:"failed",Data:details}))
  _,pending,err:=genreports.ReadCreateTask(value," / job α")
  assert.Nil(t,pending)
  var failure *mcp.Error
  require.True(t,errors.As(err,&failure))
  assert.Equal(t,[]byte(details),[]byte(failure.Data))
 }
 value:=observation(genjobs.NewJobStateFailed(&genjobs.JobFailure{Code:-32001,Message:"failed",Data:rawjson.Message("{")}))
 _,_,err:=genreports.ReadCreateTask(value," / job α")
 require.Error(t,err)
 var failure *mcp.Error
 assert.False(t,errors.As(err,&failure),"malformed details must fail at the service result boundary")
}

func TestRepeatedQuestionKindKeepsDistinctNativeKeys(t *testing.T) {
 requests:=make(map[genjobs.JobQuestionID]*genjobs.JobRequest)
 for _,id:=range []genjobs.JobQuestionID{""," / question α","another question"} {
  requests[id]=&genjobs.JobRequest{Request:genjobs.NewJobRequestKindProfile(&genjobs.ProfileQuestion{PromptText:"Choose a name"})}
 }
 value:=observation(genjobs.NewJobStateInputRequired(&genjobs.JobInput{Requests:requests}))
 _,pending,err:=genreports.ReadCreateTask(value," / job α")
 require.NoError(t,err)
 _,_,input,ok:=pending.AsTaskInput()
 require.True(t,ok)
 require.Len(t,input.Requests,3)
 answers:=make(map[string]json.RawMessage)
 for id,question:=range input.Requests {
  assert.Equal(t,"elicitation/create",question.Method)
  answers[id]=json.RawMessage("{\"action\":\"accept\",\"content\":{\"name\":\"accepted\"}}")
 }
 payload:=genreports.ToCreateTaskAnswerPayload(&genjobs.CreatePayload{Query:"report",OwnerContext:genjobs.OwnerID("owner")})
 require.NoError(t,genreports.FillCreateTaskAnswers(payload," / job α",answers))
 require.Len(t,payload.HostAnswers,3)
 for id:=range requests {
  answer,ok:=payload.HostAnswers[id].Response.AsProfile()
  require.True(t,ok)
  accepted,ok:=answer.Answer.AsAccept()
  require.True(t,ok)
  assert.Equal(t,"accepted",accepted.Content.Name)
 }
}

func TestTaskQuestionsMayBeEmptyAndAnswersPartial(t *testing.T) {
 value:=observation(genjobs.NewJobStateInputRequired(&genjobs.JobInput{Requests:map[genjobs.JobQuestionID]*genjobs.JobRequest{}}))
 _,pending,err:=genreports.ReadCreateTask(value," / job α")
 require.NoError(t,err)
 _,_,input,ok:=pending.AsTaskInput()
 require.True(t,ok)
 require.NotNil(t,input.Requests)
 assert.Empty(t,input.Requests)
 payload:=genreports.ToCreateTaskAnswerPayload(&genjobs.CreatePayload{Query:"report",OwnerContext:genjobs.OwnerID("owner")})
 require.NoError(t,genreports.FillCreateTaskAnswers(payload," / job α",map[string]json.RawMessage{}))
 assert.NotNil(t,payload.HostAnswers)
 assert.Empty(t,payload.HostAnswers)
}

type jobService struct { creates, reads, answers, cancels int; lastState genjobs.JobState }
func (s *jobService) Create(_ context.Context,p *genjobs.CreatePayload)(*genjobs.JobObservation,error) {
 if p.OwnerContext!="owner" { return nil,errors.New("creator owner changed") }
 s.creates++
 return observation(genjobs.NewJobStateWorking(&genjobs.Empty{})),nil
}
func (s *jobService) Read(_ context.Context,p *genjobs.ReadPayload)(*genjobs.JobObservation,error) {
 if p.Selection!="standard" { return nil,errors.New("read default was not applied") }
 if p.TaskID!=" / job α"||p.OwnerIdentity!="owner" { return nil,errors.New("read identity or owner changed") }
 s.reads++
 if s.lastState.Kind()=="" { return observation(genjobs.NewJobStateComplete("finished")),nil }
 return observation(s.lastState),nil
}
func (s *jobService) Answer(_ context.Context,p *genjobs.AnswerPayload)error {
 if p.TaskID!=" / job α"||p.OwnerIdentity!="owner" { return errors.New("answer identity or owner changed") }
 s.answers++
 return nil
}
func (s *jobService) Cancel(_ context.Context,p *genjobs.CancelPayload)error {
 if p.TaskID!=" / job α"||p.OwnerIdentity!="owner" { return errors.New("cancel identity or owner changed") }
 s.cancels++
 return nil
}
func savedTaskOperation(t *testing.T,kind string) *api.ExecutionContinuation {
 t.Helper()
 var branch gentooloperations.Operation
 switch kind {
 case "read":branch=gentooloperations.NewOperationTaskGet(gentooloperations.OperationBranchTaskGet(" / job α"))
 case "answer":branch=gentooloperations.NewOperationTaskUpdate(&gentooloperations.ToolOperationTaskAnswers{TaskID:" / job α",Responses:map[string][]byte{}})
 case "cancel":branch=gentooloperations.NewOperationTaskCancel(gentooloperations.OperationBranchTaskCancel(" / job α"))
 default:t.Fatalf("unknown operation %q",kind)
 }
 data,err:=gentooloperations.EncodeToolOperationExecutionContinuation(&gentooloperations.ToolOperationExecutionContinuation{Operation:branch})
 require.NoError(t,err)
 var result api.ExecutionContinuation
 require.NoError(t,json.Unmarshal(data,&result))
 return &result
}
func TestNativeTaskDispatchUsesExistingMethods(t *testing.T) {
 service:=&jobService{}
 endpoints:=genjobs.NewEndpoints(service)
 executor:=genexecutor.NewWorkerReportsExec(genexecutor.WithClient(genjobs.NewClient(endpoints.Create,endpoints.Read,endpoints.Answer,endpoints.Cancel)),genexecutor.WithInterceptors(genexecutor.ToolInterceptorFunc(func(_ context.Context,input any,_ *runtime.ToolCallMeta)error {
  switch payload:=input.(type) {
  case *genjobs.CreatePayload:assert.Equal(t,"owner",string(payload.OwnerContext))
  case *genjobs.ReadPayload:payload.TaskID="interceptor value"
  case *genjobs.AnswerPayload:payload.TaskID="interceptor value"
  case *genjobs.CancelPayload:payload.TaskID="interceptor value"
  default:t.Fatalf("unexpected injection input %T",input)
  }
  return nil
 })))
 call:=&runtime.ToolCall{Name:"reports.create",Payload:[]byte("{\"query\":\"report\",\"owner\":\"owner\"}")}
 created,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{},call)
 require.NoError(t,err)
 assert.Nil(t,created.ToolResult)
 for index,kind:=range []string{"read","answer","cancel","read"} {
  call.ExecutionSequence=uint64(index+1)
  call.ExecutionContinuation=savedTaskOperation(t,kind)
  result,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{},call)
  require.NoError(t,err)
  if kind=="read" {
   require.NotNil(t,result.ToolResult)
   require.Nil(t,result.ToolResult.Failure)
   assert.Equal(t,"finished",string(result.ToolResult.Result.(genreports.CreateResult)))
  } else { assert.Nil(t,result.ToolResult) }
 }
 assert.Equal(t,1,service.creates)
 assert.Equal(t,2,service.reads)
 assert.Equal(t,1,service.answers)
 assert.Equal(t,1,service.cancels)
}
func TestRegistryNativeTaskDispatchUsesSameContracts(t *testing.T) {
 service:=&jobService{}
 provider:=genreports.NewProvider(service)
 msg:=toolregistry.ToolCallMessage{RegistrationToken:"registration",Tool:genreports.Create,Payload:[]byte("{\"query\":\"report\",\"owner\":\"owner\"}"),Meta:&toolregistry.ToolCallMeta{RunID:"run",ToolCallID:"call"}}
 invoke:=func()toolregistry.ToolResultMessage {
  msg.ToolUseID=toolregistry.DeriveToolUseID("run","call",msg.Meta.ExecutionSequence)
  result,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
  require.NoError(t,err)
  require.Nil(t,result.Error)
  return result
 }
 created:=invoke()
 require.NotNil(t,created.PendingExecution)
 taskID,_,ok:=created.PendingExecution.AsTaskWait()
 assert.True(t,ok)
 assert.Equal(t," / job α",taskID)
 for index,kind:=range []string{"read","answer","cancel","read"} {
  msg.Meta.ExecutionSequence=uint64(index+1)
  msg.Meta.ExecutionContinuation=savedTaskOperation(t,kind)
  result:=invoke()
  if kind=="read" {
   assert.Nil(t,result.PendingExecution)
   value,err:=genreports.CreateResultCodec().FromJSON(result.Result)
   require.NoError(t,err)
   assert.Equal(t,"finished",string(value))
  } else { require.NotNil(t,result.PendingExecution) }
 }
 assert.Equal(t,1,service.creates)
 assert.Equal(t,2,service.reads)
 assert.Equal(t,1,service.answers)
 assert.Equal(t,1,service.cancels)
}

func TestTaskURLDecisionsAndOpaqueAnswerKeys(t *testing.T) {
 source:=&genjobs.CreatePayload{Query:"report",OwnerContext:"owner"}
 for _,action:=range []string{"accept","decline","cancel"} {
  value:=observation(genjobs.NewJobStateInputRequired(&genjobs.JobInput{Requests:map[genjobs.JobQuestionID]*genjobs.JobRequest{
   "approval id":{Request:genjobs.NewJobRequestKindApproval(&genjobs.ApprovalQuestion{Message:"Approve access",URL:"https://consent.example/approve"})},
  }}))
  _,pending,err:=genreports.ReadCreateTask(value," / job α")
  require.NoError(t,err)
  _,_,input,ok:=pending.AsTaskInput();require.True(t,ok)
  require.NoError(t,input.Validate(mcp.InputSupport{URL:true}))
  answers:=map[string]json.RawMessage{}
  for key:=range input.Requests {answers[key]=json.RawMessage("{\"action\":\""+action+"\"}")}
  payload:=genreports.ToCreateTaskAnswerPayload(source)
  require.NoError(t,genreports.FillCreateTaskAnswers(payload," / job α",answers))
  decision,ok:=payload.HostAnswers["approval id"].Response.AsApproval();require.True(t,ok)
  assert.Equal(t,action,string(decision.Answer.Kind()))
 }
 profile:=base64.RawURLEncoding.EncodeToString([]byte("profile"))+"."
 approval:=base64.RawURLEncoding.EncodeToString([]byte("approval"))+"."
 suffix:=base64.RawURLEncoding.EncodeToString([]byte("same id"))
 payload:=genreports.ToCreateTaskAnswerPayload(source)
 err:=genreports.FillCreateTaskAnswers(payload," / job α",map[string]json.RawMessage{
  profile+suffix:json.RawMessage("{\"action\":\"decline\"}"),
  approval+suffix:json.RawMessage("{\"action\":\"accept\"}"),
 })
 require.ErrorContains(t,err,"multiple answers")
 require.NoError(t,genreports.FillCreateTaskAnswers(payload," / job α",map[string]json.RawMessage{
  profile+suffix:json.RawMessage("{\"action\":\"decline\"}"),
  approval+base64.RawURLEncoding.EncodeToString([]byte("different id")):json.RawMessage("{\"action\":\"accept\"}"),
  "unknown":json.RawMessage("not JSON"),
  profile+suffix+"=":json.RawMessage("not JSON"),
  "unknown-kind."+suffix:json.RawMessage("not JSON"),
 }))
 require.Len(t,payload.HostAnswers,2)
}
`

const nativeTaskCreationPauseTests = `
func creationResume(t *testing.T)*api.ExecutionContinuation {
 t.Helper()
 state:="permission"
 data,err:=gentooloperations.EncodeToolOperationExecutionContinuation(&gentooloperations.ToolOperationExecutionContinuation{Operation:gentooloperations.NewOperationInput(&gentooloperations.ToolOperationInputContinuation{State:&state,Responses:map[string][]byte{}})})
 require.NoError(t,err)
 var continuation api.ExecutionContinuation
 require.NoError(t,json.Unmarshal(data,&continuation))
 return &continuation
}
func TestNativeCreationPausesBeforeJobAndReadsAfterCreation(t *testing.T) {
 service:=&jobService{askBeforeCreation:true}
 endpoints:=genjobs.NewEndpoints(service)
 executor:=genexecutor.NewWorkerReportsExec(genexecutor.WithClient(genjobs.NewClient(endpoints.Create,endpoints.Read,endpoints.Answer,endpoints.Cancel)))
 call:=&runtime.ToolCall{Name:"reports.create",Payload:[]byte("{\"query\":\"report\",\"owner\":\"owner\"}")}
 pending,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{},call)
 require.NoError(t,err)
 require.Nil(t,pending.ToolResult)
 assert.Equal(t,1,service.creates)
 call.ExecutionSequence=1
 call.ExecutionContinuation=creationResume(t)
 created,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{},call)
 require.NoError(t,err)
 require.Nil(t,created.ToolResult)
 assert.Equal(t,2,service.creates)
 call.ExecutionSequence=2
 call.ExecutionContinuation=savedTaskOperation(t,"read")
 completed,err:=executor.Execute(t.Context(),&runtime.ToolCallMeta{},call)
 require.NoError(t,err)
 require.NotNil(t,completed.ToolResult)
 require.Nil(t,completed.ToolResult.Failure)
 assert.Equal(t,"finished",string(completed.ToolResult.Result.(genreports.CreateResult)))
 assert.Equal(t,2,service.creates)
 assert.Equal(t,1,service.reads)
}
func TestRegistryCreationPausesBeforeJobAndReadsAfterCreation(t *testing.T) {
 service:=&jobService{askBeforeCreation:true}
 provider:=genreports.NewProvider(service)
 msg:=toolregistry.ToolCallMessage{Tool:genreports.Create,Payload:[]byte("{\"query\":\"report\",\"owner\":\"owner\"}"),Meta:&toolregistry.ToolCallMeta{RunID:"run",ToolCallID:"call"}}
 invoke:=func()toolregistry.ToolResultMessage {
  msg.ToolUseID=toolregistry.DeriveToolUseID("run","call",msg.Meta.ExecutionSequence)
  output,err:=provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(),msg.ToolUseID),msg)
  require.NoError(t,err)
  require.Nil(t,output.Error)
  return output
 }
 inputResult:=invoke()
 require.NotNil(t,inputResult.PendingExecution)
 input,ok:=inputResult.PendingExecution.AsInput()
 require.True(t,ok)
 assert.Equal(t,"permission",*input.RequestState)
 msg.Meta.ExecutionSequence=1
 msg.Meta.ExecutionContinuation=creationResume(t)
 created:=invoke()
 require.NotNil(t,created.PendingExecution)
 taskID,_,ok:=created.PendingExecution.AsTaskWait()
 require.True(t,ok)
 assert.Equal(t," / job α",taskID)
 msg.Meta.ExecutionSequence=2
 msg.Meta.ExecutionContinuation=savedTaskOperation(t,"read")
 completed:=invoke()
 assert.Nil(t,completed.PendingExecution)
 assert.Equal(t,2,service.creates)
 assert.Equal(t,1,service.reads)
}
`
