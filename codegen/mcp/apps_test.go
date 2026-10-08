// These checks encode Apps declarations into the current nested metadata shape.
// Ordinary tool metadata stays absent, and app-only helpers need no UI resource.
package codegen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

func TestMCPAppsMetadata(t *testing.T) {
	for _, test := range []struct {
		name string
		tool mcpexpr.ToolExpr
		want string
	}{
		{name: "ordinary"},
		{name: "UI", tool: mcpexpr.ToolExpr{UIResourceURI: "ui://records/panel"}, want: `{"ui":{"resourceUri":"ui://records/panel"}}`},
		{name: "app helper", tool: mcpexpr.ToolExpr{Visibility: mcpexpr.AppVisibility}, want: `{"ui":{"visibility":["app"]}}`},
		{name: "model only", tool: mcpexpr.ToolExpr{Visibility: mcpexpr.ModelVisibility}, want: `{"ui":{"visibility":["model"]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := toolUIMetadata(&test.tool)
			require.NoError(t, err)
			if test.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.JSONEq(t, test.want, got)
			assert.NotContains(t, got, `"ui/resourceUri"`)
		})
	}
}

func TestMCPAppsAndTasksDiscovery(t *testing.T) {
	metadata, err := serverExtensionMetadata(&AdapterData{
		Tasks: []*taskAdapter{{}},
		Tools: []*ToolAdapter{{UIMetadata: `{"ui":{"visibility":["app"]}}`}},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"io.modelcontextprotocol/tasks":{},"io.modelcontextprotocol/ui":{"mimeTypes":["text/html;profile=mcp-app"]}}`, metadata)
}

func TestToolMetadataRejectsReservedJSONNames(t *testing.T) {
	for _, test := range []struct {
		name, field string
		meta        expr.MetaExpr
	}{
		{name: "design name", field: "io.modelcontextprotocol/serverInfo"},
		{name: "renamed JSON field", field: "identity", meta: expr.MetaExpr{"struct:tag:json:name": {"io.modelcontextprotocol/serverInfo"}}},
		{name: "complete JSON tag", field: "identity", meta: expr.MetaExpr{"struct:tag:json": {"io.modelcontextprotocol/serverInfo,omitempty"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, methods := testService("records", "read")
			methods["read"].Result = &expr.AttributeExpr{Type: &expr.Object{
				{Name: "details", Attribute: &expr.AttributeExpr{Type: &expr.Object{{Name: test.field, Attribute: &expr.AttributeExpr{Type: expr.String, Meta: test.meta}}}}},
			}}
			tool := &mcpexpr.ToolExpr{Name: "read", Description: "Read a record", Method: methods["read"], MetadataField: "details"}
			_, err := newAdapterGenerator(testSchemaAPI(), service, &mcpexpr.MCPExpr{Tools: []*mcpexpr.ToolExpr{tool}}).buildToolResultAdapter(tool)
			assert.ErrorContains(t, err, "framework-owned serverInfo JSON field")
		})
	}
}

func TestMCPGeneratedAppsCatalogAndModelCaller(t *testing.T) {
	runMCPPeer(t, "apps-peer.local", appsPeerDesign, appsPeerRuntime)
}

func TestMCPGeneratedAppsMetadataViews(t *testing.T) {
	result := `Result(func(){Field(1,"summary",String,"Model-visible summary");Field(2,"hostData",privateInfo,"Private app data",func(){Meta("struct:field:name","PrivateInfo")});Required("summary")})`
	views := `
var showResult=ResultType("application/vnd.shown-record",func(){
 TypeName("ShowResult")
 Field(1,"summary",String,"Model-visible summary")
 Field(2,"hostData",privateInfo,"Private app data",func(){Meta("struct:field:name","PrivateInfo")})
 Required("summary")
 View("default",func(){Attribute("summary");Attribute("hostData")})
 View("detailed",func(){Attribute("summary");Attribute("hostData")})
 View("summary",func(){Attribute("summary")})
})
`
	design := strings.Replace(appsPeerDesign, `var _=Service("records",func(){`, views+`var _=Service("records",func(){`, 1)
	design = strings.Replace(design, result, `Result(showResult)`, 1)
	runtime := strings.Replace(appsPeerRuntime, `omitMetadata,invalidMetadata bool`, `omitMetadata,invalidMetadata bool;view string`, 1)
	runtime = strings.Replace(runtime, `Show(context.Context)(*genservice.ShowResult,error)`, `Show(context.Context)(*genservice.ShowResult,string,error)`, 1)
	runtime = strings.Replace(runtime, `return result,nil`, `view:="detailed";if s.view!=""{view=s.view};return result,view,nil`, 1)
	runtime = strings.ReplaceAll(runtime, `"{\"summary\":\"record\"}"`, `"{\"type\":\"detailed\",\"value\":{\"summary\":\"record\"}}"`)
	runtime = strings.Replace(runtime, ` service.omitMetadata=false;service.invalidMetadata=true`, `
 service.omitMetadata=false;service.view="summary"
 hidden,hiddenErr:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"show"});require.NoError(t,hiddenErr)
 hiddenComplete,hiddenOK:=hidden.(*genmcp.ToolsCallResult).Outcome.AsComplete();require.True(t,hiddenOK)
 assert.JSONEq(t,"{\"io.modelcontextprotocol/serverInfo\":{\"name\":\"records\",\"version\":\"1\"}}",string(hiddenComplete.Meta))
 assert.JSONEq(t,"{\"type\":\"summary\",\"value\":{\"summary\":\"record\"}}",string(hiddenComplete.StructuredContent))
 service.view="detailed"
 service.omitMetadata=false;service.invalidMetadata=true`, 1)
	runMCPPeer(t, "apps-peer.local", design, runtime)
}

const appsPeerDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _=API("apps-peer",func(){Description("Verify authored Apps through generated HTTP contracts")})
var privateInfo=Type("RecordPrivate",func(){
 Field(1,"secret",String,"Data delivered only to the host and app",func(){Enum("private")})
 Field(2,"sequence",Int64,"Exact record sequence")
 Required("secret","sequence")
})
var _=Service("records",func(){
 Description("Owns a synthetic record panel and its actions")
 MCP("records","1")
 JSONRPC(func(){POST("/mcp")})
 Method("panel",func(){Result(String);Resource("panel","ui://records/panel","text/html;profile=mcp-app")})
 Method("show",func(){
  Result(func(){Field(1,"summary",String,"Model-visible summary");Field(2,"hostData",privateInfo,"Private app data",func(){Meta("struct:field:name","PrivateInfo")});Required("summary")})
  Tool("show","Show a record",func(){ToolUI("ui://records/panel");ToolMetadata("hostData")})
 })
 Method("refresh",func(){Result(String);Tool("refresh","Refresh the panel",func(){ToolVisibility("app")})})
 Method("query",func(){Result(String);Tool("query","Query records",func(){ToolVisibility("model")})})
})
`

const appsPeerRuntime = `package appspeer
import (
 "context"
 "net/http/httptest"
 "net/url"
 "testing"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genservice "apps-peer.local/gen/records"
 genmcp "apps-peer.local/gen/mcp_records"
 genclient "apps-peer.local/gen/jsonrpc/mcp_records/client"
 genserver "apps-peer.local/gen/jsonrpc/mcp_records/server"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
)
type recordsService struct { refreshes int;omitMetadata,invalidMetadata bool }
func(s *recordsService) Panel(context.Context)(string,error){return "<!doctype html><p>Record panel</p>",nil}
func(s *recordsService) Show(context.Context)(*genservice.ShowResult,error){
 result:=&genservice.ShowResult{Summary:"record"}
 if !s.omitMetadata{
  secret:="private";if s.invalidMetadata{secret="invalid"}
  result.PrivateInfo=&genservice.RecordPrivate{Secret:secret,Sequence:9007199254740993}
 }
 return result,nil
}
func(s *recordsService) Refresh(context.Context)(string,error){s.refreshes++;return "refreshed",nil}
func(s *recordsService) Query(context.Context)(string,error){return "query",nil}
func TestAppsPeer(t *testing.T){
 service:=&recordsService{}
 adapter:=genmcp.NewMCPAdapter(genservice.NewEndpoints(service),nil)
 mux:=goahttp.NewMuxer()
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil,"/mcp")
 genserver.Mount(mux,server)
 peer:=httptest.NewServer(mux);defer peer.Close()
 address,err:=url.Parse(peer.URL);require.NoError(t,err)
 client:=genclient.NewClient(address.Scheme,address.Host,peer.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 discovered,err:=client.ServerDiscover()(t.Context(),&genmcp.DiscoverPayload{});require.NoError(t,err)
 assert.JSONEq(t,"{\"io.modelcontextprotocol/ui\":{\"mimeTypes\":[\"text/html;profile=mcp-app\"]}}",string(discovered.(*genmcp.DiscoverResult).Capabilities.Extensions))
 response,err:=client.ToolsList()(t.Context(),&genmcp.ToolsListPayload{});require.NoError(t,err)
 tools:=response.(*genmcp.ToolsListResult).Tools
 require.Len(t,tools,3)
 for _,tool:=range tools {
  switch tool.Name {
  case "show":assert.JSONEq(t,"{\"ui\":{\"resourceUri\":\"ui://records/panel\"}}",string(tool.Meta))
  case "refresh":assert.JSONEq(t,"{\"ui\":{\"visibility\":[\"app\"]}}",string(tool.Meta))
  case "query":assert.JSONEq(t,"{\"ui\":{\"visibility\":[\"model\"]}}",string(tool.Meta))
  default:t.Errorf("unexpected tool %q",tool.Name)
  }
  assert.NotContains(t,string(tool.OutputSchema),"hostData")
  assert.NotContains(t,string(tool.OutputSchema),"secret")
 }
 resource,err:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"ui://records/panel"});require.NoError(t,err)
 html,ok:=resource.(*genmcp.ResourcesReadResult).Outcome.AsComplete();require.True(t,ok)
 require.Len(t,html.Contents,1)
 assert.Equal(t,"text/html;profile=mcp-app",*html.Contents[0].MimeType)
 assert.Equal(t,"<!doctype html><p>Record panel</p>",*html.Contents[0].Text)
 caller,err:=genclient.NewCaller(client,mcpruntime.ClientInfo{Name:"apps-host",Version:"1"},mcpruntime.InputSupport{},mcpruntime.HTTPRetryPolicy{});require.NoError(t,err)
 _,err=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"refresh"});require.ErrorContains(t,err,"app-only");assert.Zero(t,service.refreshes)
 queried,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"query"});require.NoError(t,err);assert.JSONEq(t,"\"query\"",string(queried.StructuredContent))
 // The app host uses the generated protocol client after checking its own permissions.
 refreshed,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"refresh"});require.NoError(t,err)
 completed,ok:=refreshed.(*genmcp.ToolsCallResult).Outcome.AsComplete();require.True(t,ok)
 assert.JSONEq(t,"\"refreshed\"",string(completed.StructuredContent));assert.Equal(t,1,service.refreshes)
 shown,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"show"});require.NoError(t,err)
 shownComplete,ok:=shown.(*genmcp.ToolsCallResult).Outcome.AsComplete();require.True(t,ok)
 assert.JSONEq(t,"{\"summary\":\"record\"}",string(shownComplete.StructuredContent))
 assert.JSONEq(t,"{\"secret\":\"private\",\"sequence\":9007199254740993,\"io.modelcontextprotocol/serverInfo\":{\"name\":\"records\",\"version\":\"1\"}}",string(shownComplete.Meta))
 modelResult,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"show"});require.NoError(t,err)
 assert.JSONEq(t,"{\"summary\":\"record\"}",string(modelResult.StructuredContent))
 service.omitMetadata=true
 omitted,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"show"});require.NoError(t,err)
 omittedComplete,ok:=omitted.(*genmcp.ToolsCallResult).Outcome.AsComplete();require.True(t,ok)
 assert.JSONEq(t,"{\"io.modelcontextprotocol/serverInfo\":{\"name\":\"records\",\"version\":\"1\"}}",string(omittedComplete.Meta))
 service.omitMetadata=false;service.invalidMetadata=true
 _,err=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"show"});require.Error(t,err)
}
`
