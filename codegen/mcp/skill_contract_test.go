// These checks use generated HTTP peers to verify native Skills discovery.
// Authentication, mapped inputs, complete manifests and unlisted lookups retain
// their ordinary service owners; discovery never causes a resource read.
package codegen

import (
	"strings"
	"testing"
)

func TestMCPSkillDiscoveryHTTP(t *testing.T) {
	runMCPPeer(t, "skill-peer.local", skillPeerDesign, skillPeerRuntime)
}

func TestMCPSkillDiscoveryResultViews(t *testing.T) {
	design := skillPeerDesign
	for _, result := range []struct{ name, identifier, fields string }{
		{"SkillPage", "page", `Field(1,"skills",ArrayOfRequired(entry),"Complete visible entries",func(){Meta("struct:field:name","Entries")});Field(2,"nextCursor",String,"Next page",func(){Meta("struct:field:name","Following")})`},
		{"SkillLookupResult", "lookup", `Field(1,"skill",entry,"Complete entry",func(){Meta("struct:field:name","Entry")});Required("skill")`},
	} {
		view := `View("selected",func(){`
		if result.identifier == "page" {
			view += `Attribute("skills");Attribute("nextCursor")`
		} else {
			view += `Attribute("skill")`
		}
		declaration := `var ` + result.identifier + `=ResultType("application/vnd.skill-` + result.identifier + `",func(){TypeName("` + result.name + `");` + result.fields + `;` + view + `})})` + "\n"
		design = strings.Replace(design, `var _=Service`, declaration+`var _=Service`, 1)
		design = strings.Replace(design, `Result(func(){`+result.fields+`})`, `Result(`+result.identifier+`)`, 1)
	}
	runtime := strings.ReplaceAll(skillPeerRuntime, "genservice.ListResult", "genservice.SkillPage")
	runtime = strings.ReplaceAll(runtime, "genservice.LookupResult", "genservice.SkillLookupResult")
	start := strings.Index(runtime, "func(s *skillService)List(")
	end := strings.Index(runtime, "func(s *skillService)Read(")
	methods := runtime[start:end]
	methods = strings.ReplaceAll(methods, ",error){", ",string,error){")
	methods = strings.ReplaceAll(methods, "return nil,err", `return nil,"selected",err`)
	methods = strings.ReplaceAll(methods, "},nil", `},"selected",nil`)
	methods = strings.ReplaceAll(methods, "return nil,errors.New", `return nil,"selected",errors.New`)
	methods = strings.ReplaceAll(methods, "return nil,goa.PermanentError", `return nil,"selected",goa.PermanentError`)
	runtime = runtime[:start] + methods + runtime[end:]
	runMCPPeer(t, "skill-peer.local", design, runtime)
}

const skillPeerDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _=API("skill-peer",func(){Description("Verify typed skill discovery")})
var access=JWTSecurity("access",func(){Scope("skills:read","Read allowed skill entries")})
var file=Type("File",func(){
 Field(1,"uri",String,"Full file URI",func(){Format(FormatURI);Meta("struct:field:name","Address")})
 Field(2,"digest",String,"SHA-256 of served bytes",func(){Pattern("^sha256:[0-9a-f]{64}$")})
 Field(3,"size",Int64,"Raw content byte length",func(){Minimum(0)})
 Required("uri","digest","size")
})
var entry=Type("Entry",func(){
 Field(1,"uri",String,"Full SKILL.md URI",func(){Format(FormatURI);Meta("struct:field:name","Address")})
 Field(2,"frontmatter",Any,"All authored frontmatter fields",func(){Meta("struct:field:type","json.RawMessage","encoding/json")})
 OneOf("resources","Complete files or generated content",func(){
  TypeName("Files");Meta("oneof:json:untagged")
  Attribute("manifest",ArrayOfRequired(file),"Every skill file")
  Attribute("dynamic",String,"Content without stable digests",func(){Enum("dynamic")})
 })
 Required("uri","frontmatter","resources")
})
var text=Type("Text",func(){Field(1,"uri",String,"Full resource URI",func(){Format(FormatURI)});Field(2,"text",String,"Requested contents");Required("uri","text")})
var item=Type("Item",func(){OneOf("content","Requested file",func(){Attribute("text",text,"Text content")});Required("content")})
func nativeInput(field string){Payload(func(){
 Token("credential",String,"Native bearer credential")
 Field(1,"organizationId",String,"Organization from the URL",func(){Meta("struct:field:name","Organization")})
 Field(2,field,String,"Exact cursor or resource URI",func(){Meta("struct:field:name","Selection")})
 Required("organizationId","credential")
 if field=="uri"{Required("uri")}
})}
var _=Service("instructions",func(){
 MCP("instructions","1")
 JSONRPC(func(){POST("/organizations/{organization}/mcp");Param("organizationId:organization")})
 Method("list",func(){
  Security(access,func(){Scope("skills:read")});nativeInput("cursor")
  Result(func(){Field(1,"skills",ArrayOfRequired(entry),"Complete visible entries",func(){Meta("struct:field:name","Entries")});Field(2,"nextCursor",String,"Next page",func(){Meta("struct:field:name","Following")})})
  SkillCatalog()
 })
 Method("lookup",func(){
  Security(access,func(){Scope("skills:read")});nativeInput("uri")
  Error("invalid_params",func(){Description("The skill URI is unknown")})
  Result(func(){Field(1,"skill",entry,"Complete entry",func(){Meta("struct:field:name","Entry")});Required("skill")})
  SkillLookup()
 })
 Method("read",func(){
  Security(access,func(){Scope("skills:read")});nativeInput("uri")
  Error("invalid_params",func(){Description("The skill file URI is unknown")})
  Result(func(){Field(1,"contents",ArrayOfRequired(item),"Requested contents")});ResourceReader()
 })
})
`

const skillPeerRuntime = `package skillpeer
import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "io"
 "net/http"
 "net/http/httptest"
 "net/url"
 "strings"
 "testing"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 genservice "skill-peer.local/gen/instructions"
 genmcp "skill-peer.local/gen/mcp_instructions"
 genclient "skill-peer.local/gen/jsonrpc/mcp_instructions/client"
 genserver "skill-peer.local/gen/jsonrpc/mcp_instructions/server"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
)
type authorized struct{}
type composed struct{}
type skillService struct{lists,lookups,reads,auth int}
func(s *skillService)JWTAuth(ctx context.Context,token string,scheme *security.JWTScheme)(context.Context,error){
 s.auth++
 if token!="allowed"||len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="skills:read"{return ctx,errors.New("credential rejected")}
 return context.WithValue(ctx,authorized{},true),nil
}
func nativeContext(ctx context.Context,organization string)error{
 if ctx.Value(authorized{})!=true||ctx.Value(composed{})!=true||organization!="blue"{return errors.New("native endpoint context lost")}
 return nil
}
func skillEntry(address string)*genservice.Entry{
 return &genservice.Entry{Address:address,Frontmatter:json.RawMessage("{\"name\":\"review\",\"description\":\"Review a change\",\"future\":{\"exact\":9007199254740993}}"),Resources:genservice.NewFilesManifest([]*genservice.File{{Address:address,Digest:"sha256:"+strings.Repeat("a",64),Size:9007199254740993}})}
}
func(s *skillService)List(ctx context.Context,p *genservice.ListPayload)(*genservice.ListResult,error){
 if err:=nativeContext(ctx,p.Organization);err!=nil{return nil,err};s.lists++
 switch{
 case p.Selection==nil:next:="next";return &genservice.ListResult{Entries:[]*genservice.Entry{skillEntry("skill://visible/review/SKILL.md")},Following:&next},nil
 case *p.Selection=="next":return &genservice.ListResult{},nil
 case *p.Selection=="duplicate":entry:=skillEntry("skill://visible/review/SKILL.md");return &genservice.ListResult{Entries:[]*genservice.Entry{entry,entry}},nil
 default:return nil,errors.New("unknown cursor")
 }
}
func(s *skillService)Lookup(ctx context.Context,p *genservice.LookupPayload)(*genservice.LookupResult,error){
 if err:=nativeContext(ctx,p.Organization);err!=nil{return nil,err};s.lookups++
 if p.Selection=="skill://unknown/review/SKILL.md"{return nil,goa.PermanentError("invalid_params","unknown skill")}
 address:=p.Selection;if address=="skill://wrong/review/SKILL.md"{address="skill://other/review/SKILL.md"}
 entry:=skillEntry(address)
 if address=="skill://invalid/review/SKILL.md"{entry.Frontmatter=json.RawMessage("{\"name\":\"review\"}")}
 if address=="skill://generated/review/SKILL.md"{entry.Resources=genservice.NewFilesDynamic("dynamic")}
 return &genservice.LookupResult{Entry:entry},nil
}
func(s *skillService)Read(ctx context.Context,p *genservice.ReadPayload)(*genservice.ReadResult,error){
 if err:=nativeContext(ctx,p.Organization);err!=nil{return nil,err};s.reads++
 if p.Selection=="skill://unknown/review/missing.md"{return nil,goa.PermanentError("invalid_params","unknown file")}
 return &genservice.ReadResult{Contents:[]*genservice.Item{{Content:genservice.NewContentText(&genservice.Text{URI:p.Selection,Text:"requested"})}}},nil
}
type authenticatedClient struct{client *http.Client;credential string;response []byte}
func(c *authenticatedClient)Do(r *http.Request)(*http.Response,error){
 r.Header.Set("Authorization","Bearer "+c.credential)
 response,err:=c.client.Do(r);if err!=nil{return nil,err}
 data,err:=io.ReadAll(response.Body);closeErr:=response.Body.Close()
 if err!=nil||closeErr!=nil{return nil,errors.Join(err,closeErr)}
 c.response=data;response.Body=io.NopCloser(bytes.NewReader(data));return response,nil
}
func TestSkillHTTPComposition(t *testing.T){
 s:=&skillService{};endpoints:=genservice.NewEndpoints(s)
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,p any)(any,error){
  if ctx.Value(goa.ServiceKey)!="instructions"{return nil,errors.New("service identity lost")}
  return next(context.WithValue(ctx,composed{},true),p)
 }})
 mux:=goahttp.NewMuxer();adapter:=genmcp.NewMCPAdapter(endpoints,nil)
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil);genserver.Mount(mux,server)
 peer:=httptest.NewServer(mux);defer peer.Close();address,err:=url.Parse(peer.URL);require.NoError(t,err)
 transport:=&authenticatedClient{client:peer.Client(),credential:"allowed"}
 client:=genclient.NewClient(address.Scheme,address.Host,transport,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 discovery,err:=client.ServerDiscover()(t.Context(),&genmcp.ServerDiscoverPayload{HTTPPath0:"blue"});require.NoError(t,err)
 capabilities:=discovery.(*genmcp.DiscoverResult).Capabilities;require.NotNil(t,capabilities.Resources)
 assert.JSONEq(t,"{\"io.modelcontextprotocol/skills\":{}}",string(capabilities.Extensions))
 result,err:=client.SkillsList()(t.Context(),&genmcp.SkillsListPayload{HTTPPath0:"blue"});require.NoError(t,err)
 page:=result.(*genmcp.SkillsListResult);require.Len(t,page.Skills,1);require.NotNil(t,page.NextCursor)
 files,ok:=page.Skills[0].Resources.AsManifest();require.True(t,ok);require.Len(t,files,1);assert.Equal(t,int64(9007199254740993),files[0].Size)
 assert.Contains(t,string(page.Skills[0].Frontmatter),"9007199254740993");assert.Equal(t,"complete",page.ResultType);assert.Equal(t,"private",page.CacheScope)
 _,err=client.SkillsList()(t.Context(),&genmcp.SkillsListPayload{HTTPPath0:"blue",Cursor:page.NextCursor});require.NoError(t,err)
 assert.Contains(t,string(transport.response),"\"skills\":[]")
 known:="skill://unlisted/review/SKILL.md"
 result,err=client.SkillsGet()(t.Context(),&genmcp.SkillsGetPayload{HTTPPath0:"blue",URI:known});require.NoError(t,err)
 assert.Equal(t,known,result.(*genmcp.SkillsGetResult).Skill.URI);assert.Zero(t,s.reads)
 result,err=client.SkillsGet()(t.Context(),&genmcp.SkillsGetPayload{HTTPPath0:"blue",URI:"skill://generated/review/SKILL.md"});require.NoError(t,err)
 dynamic,ok:=result.(*genmcp.SkillsGetResult).Skill.Resources.AsDynamic();require.True(t,ok);assert.Equal(t,genmcp.SkillResourcesBranchDynamic("dynamic"),dynamic)
 _,err=client.SkillsGet()(t.Context(),&genmcp.SkillsGetPayload{HTTPPath0:"blue",URI:"skill://wrong/review/SKILL.md"});require.Error(t,err);assert.Contains(t,err.Error(),"different URI")
 _,err=client.SkillsGet()(t.Context(),&genmcp.SkillsGetPayload{HTTPPath0:"blue",URI:"skill://invalid/review/SKILL.md"});require.Error(t,err);assert.Contains(t,string(transport.response),"\"code\":-32603");assert.Zero(t,s.reads)
 _,err=client.SkillsGet()(t.Context(),&genmcp.SkillsGetPayload{HTTPPath0:"blue",URI:"skill://unknown/review/SKILL.md"});require.Error(t,err);assert.Contains(t,string(transport.response),"\"code\":-32602")
 duplicate:="duplicate";_,err=client.SkillsList()(t.Context(),&genmcp.SkillsListPayload{HTTPPath0:"blue",Cursor:&duplicate});require.Error(t,err);assert.Contains(t,err.Error(),"duplicate URI")
 calls:=s.lookups;transport.credential="denied"
 _,err=client.SkillsGet()(t.Context(),&genmcp.SkillsGetPayload{HTTPPath0:"blue",URI:known});require.Error(t,err);assert.Equal(t,calls,s.lookups)
 transport.credential="allowed"
 _,err=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:known});require.NoError(t,err);assert.Equal(t,1,s.reads)
 _,err=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:"skill://unknown/review/missing.md"});require.Error(t,err);assert.Contains(t,string(transport.response),"\"code\":-32602")
}
`
