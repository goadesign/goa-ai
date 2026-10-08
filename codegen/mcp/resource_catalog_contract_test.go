// These checks generate an authenticated URI reader and runtime catalogs.
// Native URL fields, credentials and endpoint middleware must reach that reader;
// resource discovery never chooses which method may serve an exact URI.
package codegen

import (
	"strings"
	"testing"
)

func TestMCPExplicitResourceReader(t *testing.T) {
	runMCPPeer(t, "resource-peer.local", resourceCatalogPeerDesign, resourceCatalogPeerRuntime)
}

// TestMCPDynamicResourceCatalogs uses runtime URIs and templates with the same
// typed reader. Metadata, opaque pages and list changes keep native contracts.
func TestMCPDynamicResourceCatalogs(t *testing.T) {
	design, runtime := resourceCatalogPeer()
	runMCPPeer(t, "resource-peer.local", design, runtime)
}

// TestMCPDynamicResourceCatalogViews checks complete descriptors and pagination
// through two native result views, including Goa's projected pointer fields.
func TestMCPDynamicResourceCatalogViews(t *testing.T) {
	design, runtime := resourceCatalogPeer()
	pages := `
var resourcesPage=ResultType("application/vnd.resources-page",func(){
 TypeName("ListResourcesResult")
 Field(1,"resources",ArrayOfRequired(resourceEntry),"Visible resources",func(){Meta("struct:field:name","Entries")})
 Field(2,"nextCursor",String,"Next page",func(){Meta("struct:field:name","Following")})
 View("default",func(){Attribute("resources");Attribute("nextCursor")})
 View("page",func(){Attribute("resources");Attribute("nextCursor")})
})
var templatesPage=ResultType("application/vnd.templates-page",func(){
 TypeName("ListTemplatesResult")
 Field(1,"resourceTemplates",ArrayOfRequired(templateEntry),"Visible templates",func(){Meta("struct:field:name","Entries")})
 Field(2,"nextCursor",String,"Next page")
 View("default",func(){Attribute("resourceTemplates");Attribute("nextCursor")})
 View("page",func(){Attribute("resourceTemplates");Attribute("nextCursor")})
})
`
	design = strings.Replace(design, `var _=Service("records",func(){`, pages+`var _=Service("records",func(){`, 1)
	design = strings.Replace(design, resourcePageResult, "  Result(resourcesPage)", 1)
	design = strings.Replace(design, templatePageResult, "  Result(templatesPage)", 1)
	for _, method := range []string{"ListResources", "ListTemplates"} {
		start := strings.Index(runtime, "func(s *resourceService)"+method+"(")
		end := start + strings.Index(runtime[start:], "\n}\n") + 3
		body := runtime[start:end]
		body = strings.Replace(body, "error){", "string,error){", 1)
		body = strings.ReplaceAll(body, "return nil,errors.New", `return nil,"page",errors.New`)
		body = strings.ReplaceAll(body, "},nil", `},"page",nil`)
		runtime = runtime[:start] + body + runtime[end:]
	}
	runMCPPeer(t, "resource-peer.local", design, runtime)
}

// resourceCatalogPeer extends the URI reader fixture with both native catalogs
// and their authenticated change source, retaining the same boundary assertions.
func resourceCatalogPeer() (string, string) {
	design := strings.Replace(resourceCatalogPeerDesign, `var _=Service("records",func(){`, resourceCatalogTypes+`var _=Service("records",func(){`, 1)
	design = strings.Replace(design, ` Method("read",func(){`, resourceCatalogMethods+` Method("read",func(){`, 1)
	runtime := strings.Replace(resourceCatalogPeerRuntime, `type resourceService struct{reads,fixed int;lastURI string}`, `type resourceService struct{reads,fixed int;lastURI,sourceMode string}`, 1)
	runtime = strings.Replace(runtime, ` templates,err:=client.ResourcesTemplatesList()`, resourceCatalogAssertions+` templates,err:=client.ResourcesTemplatesList()`, 1)
	runtime = strings.Replace(runtime, ` assert.Empty(t,templates.(*genmcp.ResourceTemplatesListResult).ResourceTemplates)`, ` assert.Len(t,templates.(*genmcp.ResourceTemplatesListResult).ResourceTemplates,1)`, 1)
	runtime = strings.Replace(runtime, ` goahttp "goa.design/goa/v3/http"`, ` mcpruntime "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"`, 1)
	runtime += resourceCatalogMethodsRuntime
	return design, runtime
}

const resourceCatalogPeerDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _=API("resource-peer",func(){Description("Verify resource catalog and URI reader composition")})
var access=JWTSecurity("access",func(){Scope("resources:read","Read allowed resource contents")})
var uri=Type("ResourceURI",String,func(){Format(FormatURI);Meta("struct:pkg:path","records/shared")})
var text=Type("ResourceText",func(){
 Field(1,"uri",uri,"Exact resource address")
 Field(2,"text",String,"Resource text, including an empty string")
 Required("uri","text")
})
var blob=Type("ResourceBlob",func(){
 Field(1,"uri",uri,"Exact resource address")
 Field(2,"blob",Bytes,"Binary resource bytes")
 Required("uri","blob")
})
var item=Type("ResourceItem",func(){
 OneOf("content","Text or binary resource contents",func(){Attribute("text",text,"Text contents");Attribute("blob",blob,"Binary contents")})
 Required("content")
})
var _=Service("records",func(){
 Description("Owns synthetic resource contents selected by exact URI")
 MCP("records","1")
 JSONRPC(func(){POST("/organizations/{organization}/mcp");Param("organizationId:organization")})
 Method("fixed",func(){
  Payload(func(){Field(1,"organizationId",String,"Organization from the mapped URL");Required("organizationId")})
  Result(String);Resource("fixed","test://fixed","text/plain")
 })
 Method("read",func(){
  Security(access,func(){Scope("resources:read")})
  Payload(func(){
   Token("credential",String,"Native caller credential")
   Field(1,"organizationId",String,"Organization from the mapped URL",func(){Meta("struct:field:name","Organization")})
   Field(2,"uri",uri,"Exact resource address",func(){Meta("struct:field:name","Address")})
   Required("organizationId","credential","uri")
  })
  Result(func(){Field(1,"contents",ArrayOfRequired(item),"Ordered contents; an existing resource may be empty")})
  ResourceReader()
 })
})
`

const resourceCatalogPeerRuntime = `package resourcepeer
import (
 "context"
 "encoding/base64"
 "errors"
 "net/http"
 "net/http/httptest"
 "net/url"
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genservice "resource-peer.local/gen/records"
 genmcp "resource-peer.local/gen/mcp_records"
 genclient "resource-peer.local/gen/jsonrpc/mcp_records/client"
 genserver "resource-peer.local/gen/jsonrpc/mcp_records/server"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
)
type authenticated struct{}
type composed struct{}
type resourceService struct{reads,fixed int;lastURI string}
func(s *resourceService)JWTAuth(ctx context.Context,token string,scheme *security.JWTScheme)(context.Context,error){
 if token!="allowed"||len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="resources:read"{return ctx,errors.New("resource credential rejected")}
 return context.WithValue(ctx,authenticated{},true),nil
}
func(s *resourceService)Fixed(_ context.Context,p *genservice.FixedPayload)(string,error){
 if p.OrganizationID!="blue"{return "",errors.New("fixed URL input lost")}
 s.fixed++;return "fixed",nil
}
func(s *resourceService)Read(ctx context.Context,p *genservice.ReadPayload)(*genservice.ReadResult,error){
 if ctx.Value(authenticated{})!=true||ctx.Value(composed{})!=true||p.Organization!="blue"{return nil,errors.New("native reader context lost")}
 s.reads++;s.lastURI=string(p.Address)
 switch s.lastURI{
 case "test://records/empty":return &genservice.ReadResult{},nil
 case "test://records/text?view=exact":return &genservice.ReadResult{Contents:[]*genservice.ResourceItem{{Content:genservice.NewContentText(&genservice.ResourceText{URI:p.Address,Text:""})}}},nil
 case "test://records/blob":return &genservice.ReadResult{Contents:[]*genservice.ResourceItem{{Content:genservice.NewContentBlob(&genservice.ResourceBlob{URI:p.Address,Blob:[]byte{0,1,255}})}}},nil
 default:return nil,goa.PermanentError("invalid_params","resource not found")
 }
}
type credentialClient struct{client *http.Client;credential string}
func(c *credentialClient)Do(r *http.Request)(*http.Response,error){r.Header.Set("Authorization","Bearer "+c.credential);return c.client.Do(r)}
func TestReadWithoutTemplates(t *testing.T){
 s:=&resourceService{}
 endpoints:=genservice.NewEndpoints(s)
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,input any)(any,error){
  if ctx.Value(goa.ServiceKey)!="records"{return nil,errors.New("native service identity lost")}
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
 discovery,err:=client.ServerDiscover()(t.Context(),&genmcp.ServerDiscoverPayload{HTTPPath0:"blue"});require.NoError(t,err)
 assert.NotNil(t,discovery.(*genmcp.DiscoverResult).Capabilities.Resources)
 templates,err:=client.ResourcesTemplatesList()(t.Context(),&genmcp.ResourcesTemplatesListPayload{HTTPPath0:"blue"});require.NoError(t,err)
 assert.Empty(t,templates.(*genmcp.ResourceTemplatesListResult).ResourceTemplates)
 for _,test:=range []struct{uri,kind string}{
  {"test://records/text?view=exact","text"},{"test://records/blob","blob"},{"test://records/empty","empty"},
 }{
  t.Run(test.kind,func(t *testing.T){
   response,err:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:test.uri});require.NoError(t,err)
   result,complete:=response.(*genmcp.ResourcesReadResult).Outcome.AsComplete();require.True(t,complete)
   assert.Equal(t,test.uri,s.lastURI)
   if test.kind=="empty"{assert.Empty(t,result.Contents);return}
   require.Len(t,result.Contents,1);assert.Equal(t,test.uri,result.Contents[0].URI)
   if test.kind=="text"{require.NotNil(t,result.Contents[0].Text);assert.Empty(t,*result.Contents[0].Text);assert.Nil(t,result.Contents[0].Blob)}else{
    require.NotNil(t,result.Contents[0].Blob);assert.Equal(t,base64.StdEncoding.EncodeToString([]byte{0,1,255}),*result.Contents[0].Blob);assert.Nil(t,result.Contents[0].Text)
   }
  })
 }
 _,err=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:"test://records/missing"});require.Error(t,err);assert.Contains(t,err.Error(),"resource not found")
 reads:=s.reads
 transport.credential="denied"
 _,err=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:"test://records/empty"});require.Error(t,err);assert.Equal(t,reads,s.reads)
 response,err:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:"test://fixed"});require.NoError(t,err)
 result,complete:=response.(*genmcp.ResourcesReadResult).Outcome.AsComplete();require.True(t,complete)
 assert.Equal(t,"fixed",*result.Contents[0].Text);assert.Equal(t,1,s.fixed);assert.Equal(t,reads,s.reads)
}
`

const resourceCatalogTypes = `
var icon=Type("ResourceIcon",func(){
 Field(1,"src",String,"Icon URI",func(){Format(FormatURI);Meta("struct:field:name","Address")})
 Required("src")
})
var annotations=Type("ResourceAnnotations",func(){Field(1,"priority",Float64,"Importance of this entry",func(){Minimum(0);Maximum(1)})})
var resourceEntry=Type("ResourceEntry",func(){
 Field(1,"uri",uri,"Exact resource address",func(){Meta("struct:field:name","Address")})
 Field(2,"name",String,"Resource identifier",func(){Meta("struct:field:name","Label")})
 Field(3,"title",String,"Resource display name")
 Field(4,"size",Float64,"Raw content bytes",func(){Minimum(0)})
 Field(5,"icons",ArrayOfRequired(icon),"Resource icons")
 Field(6,"annotations",annotations,"Resource hints")
 Field(7,"_meta",Any,"Extension object",func(){Meta("struct:field:type","json.RawMessage","encoding/json")})
 Required("uri","name")
})
var templateEntry=Type("TemplateEntry",func(){
 Field(1,"uriTemplate",String,"RFC 6570 address",func(){Meta("struct:field:name","Address")})
 Field(2,"name",String,"Template identifier")
 Field(3,"title",String,"Template display name")
 Required("uriTemplate","name")
})
var selections=Type("CatalogSelections",func(){Field(1,"resourcesListChanged",Boolean,"Accepted resource catalog changes")})
var change=Type("ResourceCatalogChange",func(){})
var event=Type("CatalogEvent",func(){
 OneOf("change","Authorized selection or resource catalog change",func(){
  Attribute("acknowledged",selections,"Authorized catalog selection")
  Attribute("resources_changed",change,"Resource catalog changed")
 })
 Required("change")
})
`

const resourceCatalogMethods = `
 Method("list_resources",func(){
  Security(access,func(){Scope("resources:read")})
  Payload(func(){Token("credential",String,"Native credential");Field(1,"organizationId",String,"Organization from the mapped URL");Field(2,"cursor",String,"Page cursor");Required("credential","organizationId")})
` + resourcePageResult + `
  ResourceCatalog()
 })
 Method("list_templates",func(){
  Security(access,func(){Scope("resources:read")})
  Payload(func(){Token("credential",String,"Native credential");Field(1,"organizationId",String,"Organization from the mapped URL");Field(2,"cursor",String,"Page cursor");Required("credential","organizationId")})
` + templatePageResult + `
  ResourceTemplateCatalog()
 })
 Method("watch",func(){
  Security(access,func(){Scope("resources:read")})
  Payload(func(){Token("credential",String,"Native credential");Field(1,"organizationId",String,"Organization from the mapped URL");Field(2,"resourcesListChanged",Boolean,"Select resource catalog changes");Required("credential","organizationId")})
  StreamingResult(event)
  SubscriptionSource()
 })
`

const resourceCatalogMethodsRuntime = `
func(s *resourceService)ListResources(ctx context.Context,p *genservice.ListResourcesPayload)(*genservice.ListResourcesResult,error){
 if ctx.Value(authenticated{})!=true||ctx.Value(composed{})!=true||p.OrganizationID!="blue"{return nil,errors.New("native catalog context lost")}
 selected:="";if p.Cursor!=nil{selected=*p.Cursor}
 if selected=="empty"{return &genservice.ListResourcesResult{},nil}
 entry:=&genservice.ResourceEntry{Address:"test://records/text?view=exact",Label:"text",Title:new("Record text"),Size:new(0.0),Icons:[]*genservice.ResourceIcon{{Address:"https://example.com/icon.png"}},Annotations:&genservice.ResourceAnnotations{Priority:new(1.0)},Meta:[]byte("{\"id\":9007199254740993}")}
 var next *string
 if selected==""{next=new("page2")}else if selected=="page2"{entry.Address="test://records/blob";entry.Label="blob"}
 if selected=="metadata"{entry.Meta=[]byte("null")}
 if selected=="uri"{entry.Address="invalid"}
 entries:=[]*genservice.ResourceEntry{entry}
 if selected=="duplicate"{entries=append(entries,entry)}
 return &genservice.ListResourcesResult{Entries:entries,Following:next},nil
}
func(s *resourceService)ListTemplates(ctx context.Context,p *genservice.ListTemplatesPayload)(*genservice.ListTemplatesResult,error){
 if ctx.Value(authenticated{})!=true||ctx.Value(composed{})!=true||p.OrganizationID!="blue"{return nil,errors.New("native template context lost")}
 address:="test://records/{id}"
 if p.Cursor!=nil&&*p.Cursor=="invalid"{address="test://{id"}
 return &genservice.ListTemplatesResult{Entries:[]*genservice.TemplateEntry{{Address:address,Name:"records",Title:new("Records")}}},nil
}
func(s *resourceService)Watch(ctx context.Context,p *genservice.WatchPayload,stream genservice.WatchServerStream)error{
 if ctx.Value(authenticated{})!=true||ctx.Value(composed{})!=true||p.OrganizationID!="blue"{return errors.New("native source context lost")}
 if s.sourceMode=="before_ack"{return stream.Send(&genservice.CatalogEvent{Change:genservice.NewChangeResourcesChanged(&genservice.ResourceCatalogChange{})})}
 accepted:=p.ResourcesListChanged
 if s.sourceMode=="subset"{accepted=new(false)}
 if err:=stream.Send(&genservice.CatalogEvent{Change:genservice.NewChangeAcknowledged(&genservice.CatalogSelections{ResourcesListChanged:accepted})});err!=nil{return err}
 if accepted!=nil&&*accepted{return stream.Send(&genservice.CatalogEvent{Change:genservice.NewChangeResourcesChanged(&genservice.ResourceCatalogChange{})})}
 return nil
}
`

const resourceCatalogAssertions = `
 require.NotNil(t,discovery.(*genmcp.DiscoverResult).Capabilities.Resources.ListChanged)
 assert.Nil(t,discovery.(*genmcp.DiscoverResult).Capabilities.Resources.Subscribe)
 pageResult,err:=client.ResourcesList()(t.Context(),&genmcp.ResourcesListPayload{HTTPPath0:"blue"});require.NoError(t,err)
 page:=pageResult.(*genmcp.ResourcesListResult);require.Len(t,page.Resources,1);entry:=page.Resources[0]
 assert.Equal(t,"test://records/text?view=exact",entry.URI);assert.Equal(t,"text",entry.Name);assert.Equal(t,"Record text",*entry.Title)
 assert.Equal(t,0.0,*entry.Size);require.Len(t,entry.Icons,1);assert.Equal(t,"https://example.com/icon.png",entry.Icons[0].Src);assert.Equal(t,1.0,*entry.Annotations.Priority)
 assert.JSONEq(t,"{\"id\":9007199254740993}",string(entry.Meta));require.NotNil(t,page.NextCursor)
 pageResult,err=client.ResourcesList()(t.Context(),&genmcp.ResourcesListPayload{HTTPPath0:"blue",Cursor:page.NextCursor});require.NoError(t,err)
 page=pageResult.(*genmcp.ResourcesListResult);require.Len(t,page.Resources,1);assert.Equal(t,"blob",page.Resources[0].Name);assert.Nil(t,page.NextCursor)
 for _,mode:=range []string{"empty","duplicate","metadata","uri"}{
  pageResult,err=client.ResourcesList()(t.Context(),&genmcp.ResourcesListPayload{HTTPPath0:"blue",Cursor:&mode})
  if mode=="empty"{require.NoError(t,err);assert.Empty(t,pageResult.(*genmcp.ResourcesListResult).Resources)}else{require.Error(t,err)}
 }
 _,err=client.ResourcesTemplatesList()(t.Context(),&genmcp.ResourcesTemplatesListPayload{HTTPPath0:"blue",Cursor:new("invalid")});require.Error(t,err);assert.Contains(t,err.Error(),"invalid resource URI template")
 for _,mode:=range []string{"valid","subset","before_ack"}{
  s.sourceMode=mode
  caller,err:=mcpruntime.NewHTTPCaller(mcpruntime.HTTPOptions{Endpoint:peer.URL+"/organizations/blue/mcp",Client:transport,ClientInfo:mcpruntime.ClientInfo{Name:"resource-peer",Version:"1"}});require.NoError(t,err)
  var events []mcpruntime.SubscriptionEvent
  err=caller.Listen(t.Context(),mcpruntime.SubscriptionFilter{ResourcesListChanged:true},func(_ context.Context,event mcpruntime.SubscriptionEvent)error{events=append(events,event);return nil})
  if mode=="before_ack"{require.Error(t,err);assert.Empty(t,events)}else{require.NoError(t,err);expected:=2;if mode=="subset"{expected=1};require.Len(t,events,expected);assert.Equal(t,mode!="subset",events[0].Accepted.ResourcesListChanged);if len(events)>1{assert.Equal(t,string(events[0].RequestID),string(events[1].RequestID))}}
 }
`

const resourcePageResult = `  Result(func(){
   Field(1,"resources",ArrayOfRequired(resourceEntry),"Visible resource descriptors",func(){Meta("struct:field:name","Entries")})
   Field(2,"nextCursor",String,"Next page",func(){Meta("struct:field:name","Following")})
  })`

const templatePageResult = `  Result(func(){
   Field(1,"resourceTemplates",ArrayOfRequired(templateEntry),"Visible URI templates",func(){Meta("struct:field:name","Entries")})
   Field(2,"nextCursor",String,"Next page")
  })`
