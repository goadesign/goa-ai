// These tests generate scoped MCP services and exercise their actual HTTP
// clients and servers. Authored URL mappings retain domain types and stay out
// of tool and prompt arguments across every generated dispatch operation.
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPMappedRouteInputs(t *testing.T) {
	for _, apiMapping := range []bool{false, true} {
		name := "service mapping"
		design := mappedRouteDesign
		if apiMapping {
			name = "API mapping"
			design = strings.Replace(design, `POST("/organizations/{name}/rpc");Param("organization_id:name")`, `POST("/organizations/{name}/rpc")`, 1)
			design = strings.Replace(design, `Path("/api")`, `Path("/api");Param("organization_id:name")`, 1)
		}
		t.Run(name, func(t *testing.T) {
			runMappedRouteInputs(t, design)
		})
	}
}

// TestMCPMappedRouteRejectsObjectCollections checks the authored design boundary,
// so unsupported URL values fail before type planning or generated compilation.
func TestMCPMappedRouteRejectsObjectCollections(t *testing.T) {
	design := strings.Replace(mappedRouteDesign, `var numbers=Type("OrganizationNumbers",ArrayOf(Int)`, `var item=Type("Item",func(){Field(1,"id",String,"Item identifier");Required("id")})
var numbers=Type("OrganizationNumbers",ArrayOf(item)`, 1)
	directory := writeMappedRouteDesign(t, design)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// #nosec G204 -- this command generates the synthetic local design.
	command := exec.CommandContext(ctx, "go", "run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", "route-inputs.local/design")
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -p=1")
	output, err := command.CombinedOutput()
	require.Error(t, err, string(output))
	assert.Contains(t, string(output), `elements of array path parameter "organization_id" must be primitive`)
}

// runMappedRouteInputs compiles the authored service and tests its generated
// transports, so inherited mappings must survive every operation's dispatch.
func runMappedRouteInputs(t *testing.T, design string) {
	t.Helper()
	directory := writeMappedRouteDesign(t, design)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, arguments := range [][]string{
		{"run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", "route-inputs.local/design"},
		{"test", "-mod=mod", "-race", "-p=1", "./..."},
	} {
		// #nosec G204 -- each command generates or tests this synthetic local module.
		command := exec.CommandContext(ctx, "go", arguments...)
		command.Dir = directory
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -p=1")
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
	}
}

// writeMappedRouteDesign supplies an isolated module and its authored design.
// Tests generate real clients and servers from these files before checking them.
func writeMappedRouteDesign(t *testing.T, design string) string {
	t.Helper()
	directory := t.TempDir()
	module := fmt.Sprintf(`module route-inputs.local

go 1.26.0
require (
 goa.design/goa-ai v0.0.0
 goa.design/goa/v3 v3.0.0
)
replace goa.design/goa-ai => %s
replace goa.design/goa/v3 => %s
`, filepath.ToSlash(testModuleDirectory(t, "goa.design/goa-ai")), filepath.ToSlash(testModuleDirectory(t, "goa.design/goa/v3")))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "design"), 0o700))
	for name, source := range map[string]string{
		"go.mod":           module,
		"design/design.go": design,
		"route_test.go":    mappedRouteRuntimeTest,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600))
	}
	return directory
}

const mappedRouteDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var organization=Type("Organization",String,func(){Pattern("^[a-z]+$");Meta("struct:pkg:path","route/shared")})
var numbers=Type("OrganizationNumbers",ArrayOf(Int),func(){MinLength(1);Meta("struct:pkg:path","route/shared")})
var text=Type("Text",func(){Field(1,"text",String,"Prompt text");Required("text")})
var message=Type("Message",func(){
 Field(1,"role",String,"Message author",func(){Enum("user")})
 OneOf("content","Message content",func(){TypeName("PromptContent");Attribute("text",text,"Text instructions")})
 Required("role","content")
})
var uri=Type("ResourceURI",String,func(){Format(FormatURI)})
var accepted=Type("AcceptedResources",func(){Field(1,"resources",ArrayOf(uri),"Accepted resource addresses")})
var changed=Type("ChangedResource",func(){Field(1,"uri",String,"Changed resource address",func(){Format(FormatURI)});Required("uri")})
var event=Type("ResourceEvent",func(){
 OneOf("change","Observed change",func(){Attribute("acknowledged",accepted,"Accepted addresses");Attribute("updated",changed,"Changed address")})
 Required("change")
})
var _=API("route_inputs",func(){Description("Verify synthetic URL scope");HTTP(func(){Path("/api")})})
var _=Service("scoped",func(){
 Description("Keep URL addressing separate from domain arguments")
 MCP("scoped","1")
 JSONRPC(func(){Path("/prefix");POST("/organizations/{name}/rpc");Param("organization_id:name")})
 Method("text",func(){
  Payload(func(){
   Field(1,"organization_id",organization,"Organization from the URL",func(){Meta("struct:field:name","Organization")})
   Field(2,"name",String,"Independent domain name")
   Required("organization_id","name")
  })
  Result(String);Tool("text","Return the organization and domain name");JSONRPC(func(){})
 })
 Method("free",func(){Result(String);Tool("free","Run without a URL-owned payload field")})
 Method("number",func(){
  Payload(func(){Field(1,"organization_id",Int64,"Positive organization number",func(){Minimum(1)});Required("organization_id")})
  Result(String);Tool("number","Return the typed organization number")
 })
 Method("list",func(){
  Payload(func(){Field(1,"organization_id",numbers,"Organization numbers");Required("organization_id")})
  Result(String);Tool("list","Return organization numbers")
 })
 Method("optional",func(){
  Payload(func(){Field(1,"organization_id",organization,"Optional domain field filled by the URL")})
  Result(String);Tool("optional","Return the supplied organization")
 })
 Method("fixed",func(){
  Payload(func(){Field(1,"organization_id",organization,"Organization from the URL");Required("organization_id")})
  Result(String);Resource("fixed","test://fixed","text/plain")
 })
 Method("prompt",func(){
  Payload(func(){Field(1,"organization_id",organization,"Organization from the URL");Field(2,"topic",String,"Requested topic");Required("organization_id","topic")})
  Result(func(){Field(1,"messages",ArrayOfRequired(message),"Prompt messages")})
  Prompt("prompt","Instructions for a topic")
 })
 Method("suggest",func(){
  Payload(func(){Field(1,"organization_id",organization,"Organization from the URL");Field(2,"value",String,"Partial text");Field(3,"arguments",MapOf(String,String),"Prior arguments");Required("organization_id","value")})
  Result(func(){Field(1,"values",ArrayOf(String),"Suggestions",func(){MaxLength(100)})})
  PromptCompletion("prompt","topic")
 })
 Method("watch",func(){
  Payload(func(){Field(1,"organization_id",organization,"Organization from the URL");Field(2,"resources",ArrayOf(uri),"Selected addresses");Required("organization_id")})
  StreamingResult(event);ResourceSubscription()
 })
})
`

const mappedRouteRuntimeTest = `package routeinputs_test
import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "net/http/httptest"
 "net/url"
 "strings"
 "sync/atomic"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genservice "route-inputs.local/gen/scoped"
 genshared "route-inputs.local/gen/route/shared"
 genmcp "route-inputs.local/gen/mcp_scoped"
 genserver "route-inputs.local/gen/jsonrpc/mcp_scoped/server"
 genclient "route-inputs.local/gen/jsonrpc/mcp_scoped/client"
 "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
)

type recordingDoer struct{client *http.Client;path,body string}
func(d *recordingDoer)Do(request *http.Request)(*http.Response,error){
 body,err:=io.ReadAll(request.Body);if err!=nil{return nil,err}
 if err:=request.Body.Close();err!=nil{return nil,err}
 d.path,d.body=request.URL.EscapedPath(),string(body)
 request.Body=io.NopCloser(bytes.NewReader(body))
 return d.client.Do(request)
}

func TestMappedValuesReachEveryEndpoint(t *testing.T){
 var calls atomic.Int64
 endpoints:=&genservice.Endpoints{
  Text:func(_ context.Context,raw any)(any,error){calls.Add(1);p:=raw.(*genservice.TextPayload);return string(p.Organization)+"/"+p.Name,nil},
  Free:func(_ context.Context,raw any)(any,error){calls.Add(1);assert.Nil(t,raw);return "free",nil},
  Number:func(_ context.Context,raw any)(any,error){calls.Add(1);return fmt.Sprint(raw.(*genservice.NumberPayload).OrganizationID),nil},
  List:func(_ context.Context,raw any)(any,error){calls.Add(1);return fmt.Sprint(raw.(*genservice.ListPayload).OrganizationID),nil},
  Optional:func(_ context.Context,raw any)(any,error){calls.Add(1);return string(*raw.(*genservice.OptionalPayload).OrganizationID),nil},
  Fixed:func(_ context.Context,raw any)(any,error){calls.Add(1);return string(raw.(*genservice.FixedPayload).OrganizationID),nil},
  Prompt:func(_ context.Context,raw any)(any,error){calls.Add(1);p:=raw.(*genservice.PromptPayload);assert.Equal(t,genshared.Organization("blue"),p.OrganizationID);assert.Equal(t,"subject",p.Topic);return &genservice.PromptResult{},nil},
  Suggest:func(_ context.Context,raw any)(any,error){calls.Add(1);p:=raw.(*genservice.SuggestPayload);return &genservice.SuggestResult{Values:[]string{string(p.OrganizationID)+"/"+p.Value}},nil},
  Watch:func(_ context.Context,raw any)(any,error){calls.Add(1);p:=raw.(*genservice.WatchEndpointInput);assert.Equal(t,genshared.Organization("blue"),p.Payload.OrganizationID);return nil,p.Stream.Send(&genservice.ResourceEvent{Change:genservice.NewChangeAcknowledged(&genservice.AcceptedResources{Resources:p.Payload.Resources})})},
 }
 adapter:=genmcp.NewMCPAdapter(endpoints,nil)
 mux:=goahttp.NewMuxer()
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 server.Mount(mux)
 peer:=httptest.NewServer(mux);defer peer.Close()
 address,err:=url.Parse(peer.URL);require.NoError(t,err)
 doer:=&recordingDoer{client:peer.Client()}
 client:=genclient.NewClient(address.Scheme,address.Host,doer,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 catalog,err:=client.ToolsList()(t.Context(),&genmcp.ToolsListPayload{HTTPPath0:"blue"});require.NoError(t,err)
 for _,tool:=range catalog.(*genmcp.ToolsListResult).Tools{assert.NotContains(t,string(tool.InputSchema),"organization_id")}
 assert.Equal(t,"/api/prefix/organizations/blue/rpc",doer.path)
 assert.NotContains(t,doer.body,"httpPath")
 for _,tc:=range []struct{name,path,arguments,want string}{
  {"text","blue","{\"name\":\"domain\"}","blue/domain"},
  {"free","blue","{}","free"},
  {"number","42","{}","42"},
  {"list","1,2","{}","[1 2]"},
  {"optional","blue","{}","blue"},
 }{
  t.Run(tc.name,func(t *testing.T){
   before:=calls.Load()
   result,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:tc.name,Arguments:json.RawMessage(tc.arguments),HTTPPath0:tc.path})
   require.NoError(t,err)
   completedReply1, isCompleteReply1 := result.(*genmcp.ToolsCallResult).Outcome.AsComplete(); if !isCompleteReply1 {t.Fatalf("expected completed MCP result: %+v", result)}
   value:=completedReply1
   require.Nil(t,value.IsError,string(value.StructuredContent))
   assert.Equal(t,"\""+tc.want+"\"",string(value.StructuredContent))
   assert.Equal(t,before+1,calls.Load())
   assert.Equal(t,"/api/prefix/organizations/"+tc.path+"/rpc",doer.path)
   assert.NotContains(t,doer.body,"organization_id")
   assert.NotContains(t,doer.body,"httpPath")
  })
 }
 for _,tc:=range []struct{name,path,arguments string}{
  {"text","BAD","{\"name\":\"domain\"}"},
  {"number","wrong","{}"},
  {"number","0","{}"},
  {"list","1,wrong","{}"},
  {"text","blue","{\"name\":\"domain\",\"organization_id\":\"injected\"}"},
 }{
  before:=calls.Load()
  result,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:tc.name,Arguments:json.RawMessage(tc.arguments),HTTPPath0:tc.path})
  require.NoError(t,err)
  completedReply2, isCompleteReply2 := result.(*genmcp.ToolsCallResult).Outcome.AsComplete(); if !isCompleteReply2 {t.Fatalf("expected completed MCP result: %+v", result)}
  failure:=completedReply2
  require.NotNil(t,failure.IsError)
  assert.True(t,*failure.IsError)
  assert.Equal(t,before,calls.Load())
 }
 fixed,err:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"test://fixed",HTTPPath0:"blue"});require.NoError(t,err)
 completedReply3, isCompleteReply3 := fixed.(*genmcp.ResourcesReadResult).Outcome.AsComplete(); if !isCompleteReply3 {t.Fatalf("expected completed MCP result: %+v", fixed)}
 assert.Equal(t,"blue",*completedReply3.Contents[0].Text)
 prompts,err:=client.PromptsList()(t.Context(),&genmcp.PromptsListPayload{HTTPPath0:"blue"});require.NoError(t,err)
 arguments:=prompts.(*genmcp.PromptsListResult).Prompts[0].Arguments
 require.Len(t,arguments,1);assert.Equal(t,"topic",arguments[0].Name)
 _,err=client.PromptsGet()(t.Context(),&genmcp.PromptsGetPayload{Name:"prompt",Arguments:map[string]string{"topic":"subject"},HTTPPath0:"blue"});require.NoError(t,err)
 promptName:="prompt"
 suggestions,err:=client.CompletionComplete()(t.Context(),&genmcp.CompletionCompletePayload{Ref:&genmcp.CompletionReference{Type:"ref/prompt",Name:&promptName},Argument:&genmcp.CompletionArgument{Name:"topic",Value:"part"},HTTPPath0:"blue"});require.NoError(t,err)
 assert.Equal(t,[]string{"blue/part"},suggestions.(*genmcp.CompletionCompleteResult).Completion.Values)
 var events []mcp.SubscriptionEvent
 ctx:=mcp.WithSubscriptionEvents(t.Context(),func(_ context.Context,event mcp.SubscriptionEvent)error{events=append(events,event);return nil})
 _,err=client.SubscriptionsListen()(ctx,&genmcp.SubscriptionsListenPayload{Notifications:&genmcp.SubscriptionFilter{ResourceSubscriptions:[]string{"test://fixed"}},HTTPPath0:"blue"});require.NoError(t,err)
 require.Len(t,events,1);assert.Equal(t,mcp.SubscriptionAcknowledged,events[0].Kind)
 caller,err:=genclient.NewCaller(client,mcp.ClientInfo{Name:"route-contract",Version:"1"},mcp.InputSupport{},mcp.HTTPRetryPolicy{},"blue");require.NoError(t,err)
 result,err:=caller.CallTool(t.Context(),mcp.CallRequest{Tool:"text",Payload:json.RawMessage("{\"name\":\"native\"}")});require.NoError(t,err)
 assert.Equal(t,"\"blue/native\"",string(result.StructuredContent))
 // A native caller's URL supplies scope; the protocol body retains only intent.
 assert.True(t,strings.HasSuffix(doer.path,"/blue/rpc"))
}
`
