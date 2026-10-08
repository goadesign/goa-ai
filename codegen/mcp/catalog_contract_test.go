// These checks generate a small authenticated catalog server and use its normal
// JSON-RPC HTTP client. Cursor aliases, native URL values, endpoint middleware and
// generated schemas must survive each page without adding catalog-owned access.
package codegen

import (
	"strings"
	"testing"
)

func TestMCPCatalogEndpointContracts(t *testing.T) {
	runMCPPeer(t, "catalog-peer.local", catalogPeerDesign, catalogPeerRuntime)
}

// TestMCPCatalogResultViews verifies native endpoint-selected views retain page
// aliases and cursors through the same generated catalog and source methods.
func TestMCPCatalogResultViews(t *testing.T) {
	design := strings.Replace(catalogPeerDesign, `Type("ToolPage",func(){`, `ResultType("application/vnd.catalog-tools",func(){TypeName("ToolPage");`, 1)
	design = strings.Replace(design, `Type("PromptPage",func(){`, `ResultType("application/vnd.catalog-prompts",func(){TypeName("PromptPage");`, 1)
	for _, collection := range []string{"tools", "prompts"} {
		before := `Field(2,"nextCursor",cursor,"Opaque cursor for the next page",func(){Meta("struct:field:name","Following")})
})`
		after := `Field(2,"nextCursor",cursor,"Opaque cursor for the next page",func(){Meta("struct:field:name","Following")})
 View("default",func(){Attribute("` + collection + `");Attribute("nextCursor")})
 View("page",func(){Attribute("` + collection + `");Attribute("nextCursor")})
})`
		design = strings.Replace(design, before, after, 1)
	}
	runtime := strings.Replace(catalogPeerRuntime, `)(*genservice.ToolPage,error){`, `)(*genservice.ToolPage,string,error){`, 1)
	runtime = strings.Replace(runtime, `)(*genservice.PromptPage,error){`, `)(*genservice.PromptPage,string,error){`, 1)
	runtime = strings.Replace(runtime, `&genservice.ToolPage{Entries:names,Following:next},err`, `&genservice.ToolPage{Entries:names,Following:next},"default",err`, 1)
	runtime = strings.Replace(runtime, `&genservice.PromptPage{Entries:names,Following:next},err`, `&genservice.PromptPage{Entries:names,Following:next},"default",err`, 1)
	runMCPPeer(t, "catalog-peer.local", design, runtime)
}

const catalogPeerDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _=API("catalog-peer",func(){Description("Verify typed catalog endpoint composition")})
var access=JWTSecurity("access",func(){Scope("catalog:read","Read allowed catalog pages")})
var cursor=Type("PageCursor",String,func(){Meta("struct:pkg:path","catalog/shared")})
var name=Type("DeclaredName",String,func(){Meta("struct:pkg:path","catalog/shared")})
var tools=Type("ToolPage",func(){
 Field(1,"tools",ArrayOf(name),"Visible declared tool names",func(){Meta("struct:field:name","Entries")})
 Field(2,"nextCursor",cursor,"Opaque cursor for the next page",func(){Meta("struct:field:name","Following")})
})
var prompts=Type("PromptPage",func(){
 Field(1,"prompts",ArrayOf(name),"Visible declared prompt names",func(){Meta("struct:field:name","Entries")})
 Field(2,"nextCursor",cursor,"Opaque cursor for the next page",func(){Meta("struct:field:name","Following")})
})
func pageInput(){Payload(func(){
 Token("credential",String,"Native credential")
 Field(1,"organizationId",String,"Organization supplied by the mapped URL",func(){Meta("struct:field:name","Organization")})
 Field(2,"cursor",cursor,"Cursor returned by the previous page",func(){Meta("struct:field:name","Page")})
 Required("organizationId","credential")
})}
var flag=Type("CatalogFlag",Boolean)
var selections=Type("CatalogSelections",func(){
 Field(1,"toolsListChanged",flag,"Select tool catalog changes",func(){Meta("struct:field:name","Tools")})
 Field(2,"promptsListChanged",flag,"Select prompt catalog changes",func(){Meta("struct:field:name","Prompts")})
})
var empty=Type("EmptyChange",func(){})
var event=Type("CatalogEvent",func(){
 OneOf("change","One accepted selection or catalog change",func(){
  Attribute("acknowledged",selections,"Authorized catalog selections")
  Attribute("tools_changed",empty,"Tool catalog changed")
  Attribute("prompts_changed",empty,"Prompt catalog changed")
 })
 Required("change")
})
var _=Service("catalog",func(){
 Description("Owns authenticated pages of declared operations")
 MCP("catalog","1")
 JSONRPC(func(){POST("/organizations/{organization}/mcp");Param("organizationId:organization")})
 Method("first",func(){
  Payload(func(){Field(1,"organizationId",String,"Organization supplied by the mapped URL");Field(2,"query",String,"Query text");Required("organizationId","query")})
  Result(String);Tool("first","Read the first record")
 })
 Method("second",func(){
  Payload(func(){Field(1,"organizationId",String,"Organization supplied by the mapped URL");Required("organizationId")})
  Result(String);Tool("second","Read the second record")
 })
 StaticPrompt("review","Review a record","user","Review this record")
 StaticPrompt("summary","Summarize a record","user","Summarize this record")
 Method("list_tools",func(){Security(access,func(){Scope("catalog:read")});pageInput();Result(tools);ToolCatalog()})
 Method("list_prompts",func(){Security(access,func(){Scope("catalog:read")});pageInput();Result(prompts);PromptCatalog()})
 Method("watch",func(){
  Security(access,func(){Scope("catalog:read")})
  Payload(func(){
   Extend(selections)
   Token("credential",String,"Native credential")
   Field(3,"organizationId",String,"Organization supplied by the mapped URL")
   Required("organizationId","credential")
  })
  StreamingResult(event);SubscriptionSource()
 })
})
`

const catalogPeerRuntime = `package catalogpeer
import (
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "net/url"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genservice "catalog-peer.local/gen/catalog"
 genshared "catalog-peer.local/gen/catalog/shared"
 genmcp "catalog-peer.local/gen/mcp_catalog"
 genclient "catalog-peer.local/gen/jsonrpc/mcp_catalog/client"
 genserver "catalog-peer.local/gen/jsonrpc/mcp_catalog/server"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
)
type authenticated struct{}
type composed struct{}
type catalogService struct{calls,auth int;lastCursor string;sourceMode string}
func(s *catalogService)JWTAuth(ctx context.Context,token string,scheme *security.JWTScheme)(context.Context,error){
 s.auth++
 if token!="allowed"||len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="catalog:read"{return ctx,errors.New("catalog credential rejected")}
 return context.WithValue(ctx,authenticated{},true),nil
}
func(s *catalogService)First(_ context.Context,p *genservice.FirstPayload)(string,error){return p.Query,nil}
func(s *catalogService)Second(context.Context,*genservice.SecondPayload)(string,error){return "second",nil}
func(s *catalogService)page(ctx context.Context,organization string,cursor *genshared.PageCursor)([]genshared.DeclaredName,*genshared.PageCursor,error){
 if ctx.Value(authenticated{})!=true||ctx.Value(composed{})!=true||organization!="blue"{return nil,nil,errors.New("native endpoint context lost")}
 s.calls++
 s.lastCursor="";if cursor!=nil{s.lastCursor=string(*cursor)}
 switch s.lastCursor{
 case "": next:=genshared.PageCursor("page2");return []genshared.DeclaredName{"second"},&next,nil
 case "page2":return []genshared.DeclaredName{"first"},nil,nil
 case "empty":return nil,nil,nil
 case "duplicate":return []genshared.DeclaredName{"first","first"},nil,nil
 case "unknown":return []genshared.DeclaredName{"invented"},nil,nil
 default:return nil,nil,errors.New("cursor rejected")
 }
}
func(s *catalogService)ListTools(ctx context.Context,p *genservice.ListToolsPayload)(*genservice.ToolPage,error){
 names,next,err:=s.page(ctx,p.Organization,p.Page)
 return &genservice.ToolPage{Entries:names,Following:next},err
}
func(s *catalogService)ListPrompts(ctx context.Context,p *genservice.ListPromptsPayload)(*genservice.PromptPage,error){
 names,next,err:=s.page(ctx,p.Organization,p.Page)
 for i,name:=range names{if name=="first"{names[i]="review"}else if name=="second"{names[i]="summary"}}
 return &genservice.PromptPage{Entries:names,Following:next},err
}
func(s *catalogService)Watch(ctx context.Context,p *genservice.WatchPayload,stream genservice.WatchServerStream)error{
 if ctx.Value(authenticated{})!=true||ctx.Value(composed{})!=true||p.OrganizationID!="blue"{return errors.New("source endpoint context lost")}
 s.calls++
 tools,prompts:=p.Tools,p.Prompts
 if s.sourceMode=="before_ack"{return stream.Send(&genservice.CatalogEvent{Change:genservice.NewChangeToolsChanged(&genservice.EmptyChange{})})}
 if s.sourceMode=="subset"{prompts=nil}
 if s.sourceMode=="unrequested"{value:=genservice.CatalogFlag(true);prompts=&value}
 ack:=&genservice.CatalogEvent{Change:genservice.NewChangeAcknowledged(&genservice.CatalogSelections{Tools:tools,Prompts:prompts})}
 if err:=stream.Send(ack);err!=nil{return err}
 if s.sourceMode=="duplicate"{return stream.Send(ack)}
 if tools!=nil&&bool(*tools){if err:=stream.Send(&genservice.CatalogEvent{Change:genservice.NewChangeToolsChanged(&genservice.EmptyChange{})});err!=nil{return err}}
 if prompts!=nil&&bool(*prompts){if err:=stream.Send(&genservice.CatalogEvent{Change:genservice.NewChangePromptsChanged(&genservice.EmptyChange{})});err!=nil{return err}}
 return nil
}
type credentialClient struct{client *http.Client;credential string}
func(c *credentialClient)Do(request *http.Request)(*http.Response,error){request.Header.Set("Authorization","Bearer "+c.credential);return c.client.Do(request)}
type credentialTransport struct{base http.RoundTripper}
func(c credentialTransport)RoundTrip(r *http.Request)(*http.Response,error){r.Header.Set("Authorization","Bearer allowed");return c.base.RoundTrip(r)}
func TestAuthenticatedCatalogPages(t *testing.T){
 s:=&catalogService{}
 endpoints:=genservice.NewEndpoints(s)
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,input any)(any,error){
  if ctx.Value(goa.ServiceKey)!="catalog"{return nil,errors.New("service identity lost")}
  return next(context.WithValue(ctx,composed{},true),input)
 }})
 mux:=goahttp.NewMuxer()
 adapter:=genmcp.NewMCPAdapter(endpoints,nil)
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 peer:=httptest.NewServer(mux);defer peer.Close()
 address,err:=url.Parse(peer.URL);require.NoError(t,err)
 transport:=&credentialClient{client:peer.Client(),credential:"allowed"}
 client:=genclient.NewClient(address.Scheme,address.Host,transport,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 result,err:=client.ToolsList()(t.Context(),&genmcp.ToolsListPayload{HTTPPath0:"blue"});require.NoError(t,err)
 page:=result.(*genmcp.ToolsListResult)
 require.Len(t,page.Tools,1);assert.Equal(t,"second",page.Tools[0].Name);require.NotNil(t,page.NextCursor);assert.Equal(t,"page2",*page.NextCursor)
 result,err=client.ToolsList()(t.Context(),&genmcp.ToolsListPayload{HTTPPath0:"blue",Cursor:page.NextCursor});require.NoError(t,err)
 page=result.(*genmcp.ToolsListResult);require.Len(t,page.Tools,1);assert.Equal(t,"first",page.Tools[0].Name);assert.Nil(t,page.NextCursor)
 assert.Contains(t,string(page.Tools[0].InputSchema),"query");assert.NotContains(t,string(page.Tools[0].InputSchema),"organizationId");assert.Equal(t,"page2",s.lastCursor)
 for _,mode:=range []string{"empty","duplicate","unknown"}{
  t.Run(mode,func(t *testing.T){
   result,err:=client.ToolsList()(t.Context(),&genmcp.ToolsListPayload{HTTPPath0:"blue",Cursor:&mode})
   if mode=="empty"{require.NoError(t,err);assert.Empty(t,result.(*genmcp.ToolsListResult).Tools)}else{require.Error(t,err);if mode=="duplicate"{assert.Contains(t,err.Error(),"duplicate")}else{assert.Contains(t,err.Error(),"undeclared")}}
  })
 }
 result,err=client.PromptsList()(t.Context(),&genmcp.PromptsListPayload{HTTPPath0:"blue"});require.NoError(t,err)
 promptPage:=result.(*genmcp.PromptsListResult);require.Len(t,promptPage.Prompts,1);assert.Equal(t,"summary",promptPage.Prompts[0].Name);assert.Equal(t,"Summarize a record",*promptPage.Prompts[0].Description)
 result,err=client.PromptsList()(t.Context(),&genmcp.PromptsListPayload{HTTPPath0:"blue",Cursor:promptPage.NextCursor});require.NoError(t,err)
 promptPage=result.(*genmcp.PromptsListResult);require.Len(t,promptPage.Prompts,1);assert.Equal(t,"review",promptPage.Prompts[0].Name);assert.Nil(t,promptPage.NextCursor)
 // A tool absent from the first catalog page still uses its original invocation contract.
 _,err=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{HTTPPath0:"blue",Name:"first",Arguments:json.RawMessage(` + "`" + `{"query":"record"}` + "`" + `)});require.NoError(t,err)
 calls:=s.calls
 transport.credential="denied"
 _,err=client.ToolsList()(t.Context(),&genmcp.ToolsListPayload{HTTPPath0:"blue"});require.Error(t,err);assert.Equal(t,calls,s.calls)
 assert.Equal(t,s.calls+1,s.auth)
 transport.credential="allowed"
 discovery,err:=client.ServerDiscover()(t.Context(),&genmcp.ServerDiscoverPayload{HTTPPath0:"blue"});require.NoError(t,err)
 capabilities:=discovery.(*genmcp.DiscoverResult).Capabilities
 require.NotNil(t,capabilities.Tools.ListChanged);assert.True(t,*capabilities.Tools.ListChanged)
 require.NotNil(t,capabilities.Prompts.ListChanged);assert.True(t,*capabilities.Prompts.ListChanged)
 for _,test:=range []struct{mode string;prompts bool;events int;failure bool}{
  {"valid",true,3,false},{"subset",true,2,false},{"empty",false,2,false},
  {"before_ack",true,0,true},{"duplicate",true,1,true},{"unrequested",false,0,true},
 }{
  t.Run("source/"+test.mode,func(t *testing.T){
   s.sourceMode=test.mode
   caller,err:=mcpruntime.NewHTTPCaller(mcpruntime.HTTPOptions{Endpoint:peer.URL+"/organizations/blue/mcp",Client:&http.Client{Transport:credentialTransport{base:peer.Client().Transport}},ClientInfo:mcpruntime.ClientInfo{Name:"catalog-peer",Version:"1"}});require.NoError(t,err)
   filter:=mcpruntime.SubscriptionFilter{ToolsListChanged:true,PromptsListChanged:test.prompts}
   var events []mcpruntime.SubscriptionEvent
   err=caller.Listen(t.Context(),filter,func(_ context.Context,event mcpruntime.SubscriptionEvent)error{events=append(events,event);return nil})
   if test.failure{require.Error(t,err)}else{require.NoError(t,err)}
   require.Len(t,events,test.events)
   if len(events)>0{assert.True(t,events[0].Accepted.ToolsListChanged);assert.Equal(t,test.prompts&&test.mode!="subset",events[0].Accepted.PromptsListChanged)}
   for _,event:=range events{assert.Equal(t,string(events[0].RequestID),string(event.RequestID))}
  })
 }
}
`
