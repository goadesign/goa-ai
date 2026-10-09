// These tests generate a synthetic job owner and send its complete task
// lifecycle through actual HTTP transports. Native credentials and mapped URL
// fields must reach each configured endpoint without entering model arguments.
package codegen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMCPGeneratedTaskLifecycle(t *testing.T) {
	runMCPPeer(t, "task-peer.local", taskOwnerDesign, taskOwnerRuntime)
}

func TestMCPGeneratedTaskRequiredAliasDefault(t *testing.T) {
	design := strings.Replace(taskOwnerDesign, `var empty=`, `var selection=Type("ReadSelection",String,func(){Default("standard");Enum("standard")})
var empty=`, 1)
	design = strings.Replace(design, `Field(3,"selection",String,"Read profile selected by this method",func(){Default("standard");Enum("standard")})`, `Field(3,"selection",selection,"Read profile selected by this method")`, 1)
	design = strings.Replace(design, `Required("credential","ownerId","taskId")`, `Required("credential","ownerId","taskId","selection")`, 1)
	runMCPPeer(t, "task-peer.local", design, taskOwnerRuntime)
}

func TestMCPGeneratedTaskViews(t *testing.T) {
	design, runtime := taskPeerViews(taskOwnerDesign, taskOwnerRuntime)
	runMCPPeer(t, "task-peer.local", design, runtime)
}

// taskPeerViews gives the same synthetic creator and read owner an execution
// view. Both ordinary completion and pushed completion must keep that selection.
func taskPeerViews(design, runtime string) (string, string) {
	design = strings.Replace(design, `var observation=Type("Observation",func(){`, `var observation=ResultType("application/vnd.task.observation",func(){TypeName("Observation")`, 1)
	design = strings.Replace(design, `Required("task","outcome")`, `Required("task","outcome");View("default",func(){Attribute("task");Attribute("outcome")});View("alternate",func(){Attribute("task");Attribute("outcome")})`, 1)
	for _, name := range []string{"Create", "Read"} {
		start := strings.Index(runtime, "func(s *jobOwner)"+name+"(")
		end := strings.Index(runtime[start:], "\nfunc(") + start
		method := runtime[start:end]
		if !strings.Contains(method, "(*genjobs.Observation,error)") {
			continue
		}
		method = strings.Replace(method, "(*genjobs.Observation,error)", "(*genjobs.Observation,string,error)", 1)
		method = strings.ReplaceAll(method, "return nil,err", `return nil,"",err`)
		method = strings.Replace(method, "return s.observation(),nil", `return s.observation(),"alternate",nil`, 1)
		runtime = runtime[:start] + method + runtime[end:]
	}
	runtime = strings.Replace(runtime, `assert.JSONEq(t,"\"done\"",string(finished.StructuredContent))`, `assert.JSONEq(t,"{\"type\":\"alternate\",\"value\":\"done\"}",string(finished.StructuredContent))`, 1)
	runtime = strings.Replace(runtime, `assert.JSONEq(t,"\"pushed\"",string(pushed.StructuredContent))`, `assert.JSONEq(t,"{\"type\":\"alternate\",\"value\":\"pushed\"}",string(pushed.StructuredContent))`, 1)
	return design, runtime
}

// TestMCPGeneratedTaskCreationInput keeps ordinary input before creation and
// task observations after creation on their separately owned methods.
func TestMCPGeneratedTaskCreationInput(t *testing.T) {
	design, runtime := taskPeerCreationInput(taskOwnerDesign, taskOwnerRuntime)
	runMCPPeer(t, "task-peer.local", design, runtime)
}

// taskPeerCreationInput adds an input round before durable job creation.
// Later observations still use the original job read method.
func taskPeerCreationInput(design, runtime string) (string, string) {
	design = strings.Replace(design, `var _=Service("jobs",func(){`, `
var creationInput=Type("CreationInput",func(){Field(1,"state",String,"Host state supplied before creation")})
var creationPending=Type("CreationPending",func(){Field(1,"state",String,"State returned before creation")})
var creationOutcome=Type("CreationOutcome",func(){
 OneOf("outcome","Required host input or created job",func(){TypeName("CreationState");Attribute("input_required",creationPending,"Host input before creation");Attribute("complete",observation,"Durably created job")});Required("outcome")
})
var _=Service("jobs",func(){`, 1)
	design = strings.Replace(design, `Required("credential","ownerId","query")`, `Field(3,"continuation",creationInput,"Input supplied before durable creation",func(){Meta("struct:field:name","HostInput")});Required("credential","ownerId","query")`, 1)
	design = strings.Replace(design, `Result(observation);TaskExchange`, `Result(creationOutcome);InputExchange("continuation","outcome");TaskExchange`, 1)
	start := strings.Index(runtime, "func(s *jobOwner)Create(")
	end := strings.Index(runtime[start:], "\nfunc(") + start
	method := runtime[start:end]
	method = strings.Replace(method, "(*genjobs.Observation,error)", "(*genjobs.CreationOutcome,error)", 1)
	method = strings.Replace(method, `s.starts++;return s.observation(),nil`, `
 if p.HostInput==nil { state:="permission";return &genjobs.CreationOutcome{Outcome:genjobs.NewCreationStateInputRequired(&genjobs.CreationPending{State:&state})},nil }
 if p.HostInput.State==nil||*p.HostInput.State!="permission" {return nil,errors.New("creation input changed")}
 s.starts++;return &genjobs.CreationOutcome{Outcome:genjobs.NewCreationStateComplete(s.observation())},nil`, 1)
	runtime = runtime[:start] + method + runtime[end:]
	call := `created,err:=transport.CallTool(mcpruntime.WithTaskSupport(t.Context()),endpoint,mcpruntime.CallRequest{Tool:"create",Payload:json.RawMessage("{\"query\":\"report\"}")})`
	resumed := `first,err:=transport.CallTool(mcpruntime.WithTaskSupport(t.Context()),endpoint,mcpruntime.CallRequest{Tool:"create",Payload:json.RawMessage("{\"query\":\"report\"}")})
 require.NoError(t,err);require.NotNil(t,first.InputRequired);require.NotNil(t,first.InputRequired.RequestState);assert.Nil(t,first.Task);assert.Zero(t,service.starts)
 created,err:=transport.CallTool(mcpruntime.WithTaskSupport(t.Context()),endpoint,mcpruntime.CallRequest{Tool:"create",Payload:json.RawMessage("{\"query\":\"report\"}"),Continuation:&mcpruntime.CallContinuation{RequestState:first.InputRequired.RequestState}})`
	runtime = strings.Replace(runtime, call, resumed, 1)
	return design, runtime
}

// TestMCPGeneratedTaskSubscriptions sends native job change selections through
// the common source and reads full snapshots through authenticated endpoints.
func TestMCPGeneratedTaskSubscriptions(t *testing.T) { runTaskSubscriptionPeer(t, false, false, false) }

func TestMCPGeneratedTaskSubscriptionViews(t *testing.T) {
	runTaskSubscriptionPeer(t, true, false, false)
}

func TestMCPGeneratedTaskSubscriptionAliases(t *testing.T) {
	runTaskSubscriptionPeer(t, false, true, false)
}

func TestMCPGeneratedMixedSubscriptions(t *testing.T) {
	runTaskSubscriptionPeer(t, false, false, true)
}

// runTaskSubscriptionPeer checks one source under the declared result view and
// tool names, sharing the same native job owner and protocol conversion.
func runTaskSubscriptionPeer(t *testing.T, viewed, aliases, mixed bool) {
	t.Helper()
	design := strings.Replace(taskOwnerDesign, `var _=Service("jobs",func(){`, `
var taskSelections=Type("TaskSelections",func(){Field(1,"create",ArrayOf(String),"Native jobs created by create")})
var selections=Type("Selections",func(){Field(1,"tasks",taskSelections,"Requested or accepted native jobs")})
var taskChanges=Type("TaskChanges",func(){Field(1,"tasks",taskSelections,"Changed native jobs");Required("tasks")})
var changes=Type("Changes",func(){OneOf("change","Accepted selections or changed jobs",func(){TypeName("SourceChange");Attribute("acknowledged",selections,"Authorized job selections");Attribute("tasks_updated",taskChanges,"Jobs whose observation changed")});Required("change")})
var _=Service("jobs",func(){`, 1)
	design = strings.Replace(design, `MCP("jobs","1")`, `MCP("jobs","1")
 Method("watch",func(){
  Description("Authorizes native job selections and reports changed job identities.")
  Security(jwt,func(){Scope("jobs")})
  Payload(func(){Token("credential",String,"Native bearer credential");Field(1,"ownerId",owner,"Authorized owner");Field(2,"tasks",taskSelections,"Requested native jobs");Required("credential","ownerId")})
  StreamingResult(changes);SubscriptionSource()
 })`, 1)
	runtime := taskOwnerRuntime + `
func(s *jobOwner)Watch(ctx context.Context,p *genjobs.WatchPayload,stream genjobs.WatchServerStream)error {
 if err:=authorize(ctx,p.Credential,p.OwnerID,nativeID);err!=nil{return err}
 if p.Tasks==nil||len(p.Tasks.Create)!=1||p.Tasks.Create[0]!=nativeID {return errors.New("source received an opaque or invented job identity")}
 selected:=&genjobs.TaskSelections{Create:[]string{nativeID}}
 if err:=stream.Send(&genjobs.Changes{Change:genjobs.NewSourceChangeAcknowledged(&genjobs.Selections{Tasks:selected})});err!=nil{return err}
 for _,state:=range []genjobs.JobState{
  genjobs.NewJobStateWorking(&genjobs.Empty{}),
  genjobs.NewJobStateInputRequired(&genjobs.JobInput{Requests:map[string]*genjobs.JobRequest{}}),
  genjobs.NewJobStateComplete("pushed"),
  genjobs.NewJobStateFailed(&genjobs.JobFailure{Code:-32603,Message:"pushed failure",Data:rawjson.Message("{\"integer\":9007199254740993}")}),
  genjobs.NewJobStateCancelled(&genjobs.Empty{}),
 } {
  s.mu.Lock();s.state=state;s.mu.Unlock()
  if err:=stream.Send(&genjobs.Changes{Change:genjobs.NewSourceChangeTasksUpdated(&genjobs.TaskChanges{Tasks:selected})});err!=nil{return err}
 }
 return nil
}
`
	runtime = strings.Replace(runtime, `before:=service.reads`, `
 var notices []mcpruntime.SubscriptionEvent
 readCount:=service.reads
 require.NoError(t,transport.Listen(t.Context(),endpoint,mcpruntime.SubscriptionFilter{TaskIDs:[]string{id,"unknown.Zg"}},func(_ context.Context,event mcpruntime.SubscriptionEvent)error{notices=append(notices,event);return nil}))
 require.Len(t,notices,6);assert.Equal(t,[]string{id},notices[0].Accepted.TaskIDs)
 for index,status:=range []mcpruntime.TaskStatus{mcpruntime.TaskWorking,mcpruntime.TaskInputRequired,mcpruntime.TaskCompleted,mcpruntime.TaskFailed,mcpruntime.TaskCancelled} { require.NotNil(t,notices[index+1].Task);assert.Equal(t,status,notices[index+1].Task.Info().Status);assert.Equal(t,id,notices[index+1].Task.Info().TaskID) }
 pushed,ok:=notices[3].Task.AsCompleted();require.True(t,ok);assert.JSONEq(t,"\"pushed\"",string(pushed.StructuredContent));assert.Equal(t,readCount+5,service.reads);assert.Equal(t,1,service.starts)
 before:=service.reads`, 1)
	if aliases {
		design = strings.Replace(design, `Tool("create","Create a durable synthetic job")`, `Tool("create","Create a durable synthetic job");Tool("create_alias","Create the same native job through another tool name")`, 1)
		runtime = strings.Replace(runtime, `var notices []mcpruntime.SubscriptionEvent`, `other,err:=transport.CallTool(mcpruntime.WithTaskSupport(t.Context()),endpoint,mcpruntime.CallRequest{Tool:"create_alias",Payload:json.RawMessage("{\"query\":\"report\"}")});require.NoError(t,err);require.NotNil(t,other.Task);aliasID:=other.Task.TaskID;assert.NotEqual(t,id,aliasID)
 var notices []mcpruntime.SubscriptionEvent`, 1)
		runtime = strings.Replace(runtime, `TaskIDs:[]string{id,"unknown.Zg"}`, `TaskIDs:[]string{id,aliasID,"unknown.Zg"}`, 1)
		runtime = strings.Replace(runtime, `require.Len(t,notices,6);assert.Equal(t,[]string{id},notices[0].Accepted.TaskIDs)`, `require.Len(t,notices,11);assert.Equal(t,[]string{id,aliasID},notices[0].Accepted.TaskIDs)`, 1)
		runtime = strings.Replace(runtime, `require.NotNil(t,notices[index+1].Task);assert.Equal(t,status,notices[index+1].Task.Info().Status);assert.Equal(t,id,notices[index+1].Task.Info().TaskID)`, `for offset,identity:=range []string{id,aliasID}{event:=notices[1+index*2+offset];require.NotNil(t,event.Task);assert.Equal(t,status,event.Task.Info().Status);assert.Equal(t,identity,event.Task.Info().TaskID)}`, 1)
		runtime = strings.Replace(runtime, `notices[3].Task.AsCompleted()`, `notices[5].Task.AsCompleted()`, 1)
		runtime = strings.Replace(runtime, `assert.Equal(t,readCount+5,service.reads);assert.Equal(t,1,service.starts)`, `assert.Equal(t,readCount+10,service.reads);assert.Equal(t,2,service.starts)`, 1)
	}
	if !viewed && !aliases {
		runtime = strings.Replace(runtime, `badID bool}`, `badID bool; sourceMode string}`, 1)
		runtime = strings.Replace(runtime, `selected:=&genjobs.TaskSelections{Create:[]string{nativeID}}`, `selected:=&genjobs.TaskSelections{Create:[]string{nativeID}}
 s.mu.Lock();mode:=s.sourceMode;s.mu.Unlock()
 if mode=="early update" {return stream.Send(&genjobs.Changes{Change:genjobs.NewSourceChangeTasksUpdated(&genjobs.TaskChanges{Tasks:selected})})}
 if mode=="unrequested acknowledgment" {selected.Create=[]string{"another native job"}}`, 1)
		runtime = strings.Replace(runtime, `for _,state:=range []genjobs.JobState{`, `if mode=="unaccepted update" {return stream.Send(&genjobs.Changes{Change:genjobs.NewSourceChangeTasksUpdated(&genjobs.TaskChanges{Tasks:&genjobs.TaskSelections{Create:[]string{"another native job"}}})})}
 for _,state:=range []genjobs.JobState{`, 1)
		runtime = strings.Replace(runtime, `before:=service.reads`, `
 for _,mode:=range []string{"early update","unrequested acknowledgment","unaccepted update"} {
  t.Run(mode,func(t *testing.T){
   service.mu.Lock();service.sourceMode=mode;reads:=service.reads;service.mu.Unlock()
   err:=transport.Listen(t.Context(),endpoint,mcpruntime.SubscriptionFilter{TaskIDs:[]string{id}},func(context.Context,mcpruntime.SubscriptionEvent)error{return nil})
   require.Error(t,err);assert.Equal(t,reads,service.reads)
  })
 }
 service.mu.Lock();service.sourceMode="";service.mu.Unlock()
 before:=service.reads`, 1)
	}
	if mixed {
		design = strings.Replace(design, `var selections=`, `var uri=Type("ResourceURI",String,func(){Format(FormatURI)})
var updated=Type("ResourceUpdate",func(){Field(1,"uri",uri,"Changed resource URI");Required("uri")})
var selections=`, 1)
		design = strings.Replace(design, `Field(1,"tasks",taskSelections,"Requested or accepted native jobs")`, `Field(1,"tasks",taskSelections,"Requested or accepted native jobs");Field(2,"resources",ArrayOf(uri),"Requested or accepted resources")`, 1)
		design = strings.Replace(design, `Attribute("tasks_updated",taskChanges,"Jobs whose observation changed")`, `Attribute("tasks_updated",taskChanges,"Jobs whose observation changed");Attribute("updated",updated,"Changed resource URI")`, 1)
		design = strings.Replace(design, `Field(2,"tasks",taskSelections,"Requested native jobs")`, `Field(2,"tasks",taskSelections,"Requested native jobs");Field(3,"resources",ArrayOf(uri),"Requested resources")`, 1)
		design = strings.Replace(design, `MCP("jobs","1")`, `MCP("jobs","1")
 Method("resource",func(){Description("Reads a synthetic resource");Result(String);Resource("record","test://records/one","text/plain")})`, 1)
		runtime += `
func(s *jobOwner)Resource(context.Context)(string,error){return "record",nil}
`
		runtime = strings.Replace(runtime, `Selections{Tasks:selected}`, `Selections{Tasks:selected,Resources:p.Resources}`, 1)
		runtime = strings.Replace(runtime, `for _,state:=range []genjobs.JobState{`, `for _,uri:=range p.Resources {if err:=stream.Send(&genjobs.Changes{Change:genjobs.NewSourceChangeUpdated(&genjobs.ResourceUpdate{URI:uri})});err!=nil{return err}}
 for _,state:=range []genjobs.JobState{`, 1)
		runtime = strings.Replace(runtime, `TaskIDs:[]string{id,"unknown.Zg"}`, `TaskIDs:[]string{id,"unknown.Zg"},ResourceSubscriptions:[]string{"test://records/one"}`, 1)
		runtime = strings.Replace(runtime, `require.Len(t,notices,6);assert.Equal(t,[]string{id},notices[0].Accepted.TaskIDs)`, `require.Len(t,notices,7);assert.Equal(t,[]string{id},notices[0].Accepted.TaskIDs);assert.Equal(t,[]string{"test://records/one"},notices[0].Accepted.ResourceSubscriptions);assert.Equal(t,mcpruntime.SubscriptionResourceUpdated,notices[1].Kind);assert.Equal(t,"test://records/one",notices[1].URI);notices=append(notices[:1],notices[2:]...)`, 1)
	}
	if viewed {
		design, runtime = taskPeerViews(design, runtime)
	}
	runMCPPeer(t, "task-peer.local", design, runtime)
}

// runMCPPeer uses the normal generator and limits compilation to a small
// synthetic module, recording both generation and verification duration.
func runMCPPeer(t *testing.T, moduleName, design, runtime string, additionalFiles ...map[string]string) {
	t.Helper()
	dir := t.TempDir()
	module := fmt.Sprintf(`module %s

go 1.27.0
require (
 github.com/modelcontextprotocol/go-sdk v1.8.0
 goa.design/goa-ai v0.0.0
 goa.design/goa/v3 v3.0.0
)
replace goa.design/goa-ai => %s
replace goa.design/goa/v3 => %s
`, moduleName, filepath.ToSlash(testModuleDirectory(t, "goa.design/goa-ai")), filepath.ToSlash(testModuleDirectory(t, "goa.design/goa/v3")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "design"), 0o700))
	for name, source := range map[string]string{"go.mod": module, "design/design.go": design, "peer_test.go": runtime} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600))
	}
	for _, files := range additionalFiles {
		for name, source := range files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600))
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, args := range [][]string{
		{"run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", moduleName + "/design"},
		{"test", "-mod=mod", "-race", "-p=1", "./..."},
	} {
		started := time.Now()
		// #nosec G204 -- fixed commands generate and test this synthetic module.
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -p=1")
		output, err := command.CombinedOutput()
		t.Logf("go %s took %s", args[0], time.Since(started).Round(time.Millisecond))
		require.NoError(t, err, string(output))
	}
}
