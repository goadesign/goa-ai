// These tests compile secured Goa services and call their generated HTTP
// clients and servers. Catalogs and codecs must expose only domain arguments,
// while authentication and every method receive the original typed payload.
package codegen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMCPNativeCredentialPaths(t *testing.T) {
	dir := t.TempDir()
	module := fmt.Sprintf(`module credential-paths.local

go 1.26.0

require (
 goa.design/goa-ai v0.0.0
 goa.design/goa/v3 v3.0.0
)

replace goa.design/goa-ai => %s
replace goa.design/goa/v3 => %s
`, filepath.ToSlash(testModuleDirectory(t, "goa.design/goa-ai")), filepath.ToSlash(testModuleDirectory(t, "goa.design/goa/v3")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "design"), 0o700))
	for name, source := range map[string]string{
		"go.mod":             module,
		"design/design.go":   nativeCredentialDesign,
		"credential_test.go": nativeCredentialTest,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, arguments := range [][]string{
		{"run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", "credential-paths.local/design"},
		{"test", "-mod=mod", "-race", "-p=1", "./..."},
	} {
		// #nosec G204 -- arguments are fixed generator and test commands for this synthetic module.
		command := exec.CommandContext(ctx, "go", arguments...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
	}
}

const nativeCredentialDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var jwt=JWTSecurity("jwt",func(){Scope("read","Read synthetic values")})
var oauth=OAuth2Security("oauth",func(){Scope("read","Read synthetic values");AuthorizationCodeFlow("https://auth.example/authorize","https://auth.example/token","")})
var bearer=BearerSecurity("bearer")
var basic=BasicAuthSecurity("basic")
var key=APIKeySecurity("key")
var credential=Type("Credential",String,func(){MinLength(3)})
var text=Type("Text",func(){Field(1,"text",String,"Prompt text");Required("text")})
var message=Type("Message",func(){
 Field(1,"role",String,"Message author",func(){Enum("user")})
 OneOf("content","Selected content",func(){TypeName("PromptContent");Attribute("text",text,"Text instructions")})
 Required("role","content")
})
var resourceText=Type("ResourceText",func(){Field(1,"uri",String,"Exact resource address",func(){Format(FormatURI)});Field(2,"text",String,"Resource text");Required("uri","text")})
var resourceItem=Type("ResourceItem",func(){OneOf("content","Selected resource representation",func(){TypeName("ResourceContent");Attribute("text",resourceText,"Text resource")});Required("content")})
var _=API("credential_paths",func(){Description("Verify synthetic native authentication paths")})
var _=Service("protected",func(){
 Description("Expose synthetic credential-free domain arguments")
 MCP("protected","1")
 JSONRPC(func(){POST("/mcp")})
 Method("jwt",func(){
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("private_jwt",credential,"Native JWT credential",func(){Meta("struct:field:name","AuthValue")});Field(1,"token",String,"Ordinary domain token");Required("private_jwt","token");Example(Val{"private_jwt":"example-secret","token":"domain"})})
  Result(String);Tool("jwt","Return the ordinary domain token");JSONRPC(func(){})
 })
 Method("oauth",func(){
  Security(oauth,func(){Scope("read")})
  Payload(func(){AccessToken("private_oauth",String,"Native OAuth credential");Field(1,"token",String,"Ordinary domain token");Required("private_oauth","token")})
  Result(String);Tool("oauth","Return the ordinary domain token");JSONRPC(func(){})
 })
 Method("bearer",func(){
  Security(bearer)
  Payload(func(){BearerToken("private_bearer",String,"Native Bearer credential");Field(1,"token",String,"Ordinary domain token");Required("private_bearer","token")})
  Result(String);Tool("bearer","Return the ordinary domain token");JSONRPC(func(){})
 })
 Method("basic",func(){
  Security(basic)
  Payload(func(){Username("private_user",String,"Native Basic username");Password("private_password",String,"Native Basic password");Field(1,"token",String,"Ordinary domain token");Required("private_user","private_password","token")})
  Result(String);Tool("basic","Return the ordinary domain token");JSONRPC(func(){})
 })
 Method("key",func(){
  Security(key)
  Payload(func(){APIKey("key","private_key",String,"Native API key");Field(1,"token",String,"Ordinary domain token");Required("private_key","token")})
  Result(String);Tool("key","Return the ordinary domain token");JSONRPC(func(){Header("private_key:X-Synthetic-Key")})
 })
 Method("alternative",func(){
  Security(basic);Security(jwt,func(){Scope("read")})
  Payload(func(){Username("private_user",credential,"Optional Basic username");Password("private_password",credential,"Optional Basic password");Token("private_jwt",credential,"Optional JWT credential",func(){Meta("struct:field:name","AuthValue")});Field(1,"token",String,"Ordinary domain token");Required("token")})
  Result(String);Tool("alternative","Use either native authentication alternative");JSONRPC(func(){})
 })
 Method("combined",func(){
  Security(jwt,key,func(){Scope("read")})
  Payload(func(){Token("private_jwt",credential,"Native JWT credential");APIKey("key","private_key",credential,"Native API key");Field(1,"token",String,"Ordinary domain token");Required("private_jwt","private_key","token")})
  Result(String);Tool("combined","Require both native authentication checks");JSONRPC(func(){Header("private_key:X-Synthetic-Key")})
 })
 Method("http_key",func(){
  Security(key)
  Payload(func(){APIKey("key","private_key",String,"Native HTTP API key");Field(1,"token",String,"Ordinary domain token");Required("private_key","token")})
  Result(String);Tool("http_key","Read the original HTTP binding");HTTP(func(){POST("/native-key");Header("private_key:X-HTTP-Key")})
 })
 Method("query_key",func(){
  Security(key)
  Payload(func(){APIKey("key","private_key",String,"Native query API key");Field(1,"token",String,"Ordinary domain token");Required("private_key","token")})
  Result(String);Tool("query_key","Read the original query binding");JSONRPC(func(){Param("private_key:api_key")})
 })
 Method("cookie_key",func(){
  Security(key)
  Payload(func(){APIKey("key","private_key",String,"Native cookie API key");Field(1,"token",String,"Ordinary domain token");Required("private_key","token")})
  Result(String);Tool("cookie_key","Read the original cookie binding");JSONRPC(func(){Cookie("private_key:api_key")})
 })
 Method("credential_only",func(){
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("private_jwt",credential,"Native JWT credential");Required("private_jwt")})
  Result(String);Tool("credential_only","Run without domain arguments");JSONRPC(func(){})
 })
 Method("fixed",func(){
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("private_jwt",String,"Native JWT credential");Required("private_jwt")})
  Result(String);Resource("fixed","test://fixed","text/plain");JSONRPC(func(){})
 })
 Method("prompt",func(){
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("private_jwt",String,"Native JWT credential");Field(1,"topic",String,"Requested topic");Required("private_jwt","topic")})
  Result(func(){Field(1,"messages",ArrayOfRequired(message),"Ordered prompt messages")})
  Prompt("prompt","Instructions for a topic");JSONRPC(func(){})
 })
 Method("resource",func(){
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("private_jwt",String,"Native JWT credential");Field(1,"uri",String,"Exact resource URI",func(){Format(FormatURI)});Required("private_jwt","uri")})
  Result(func(){Field(1,"contents",ArrayOfRequired(resourceItem),"Ordered resource contents")})
  ResourceTemplate("items","test://items/{id}","text/plain");JSONRPC(func(){})
 })
 Method("suggest",func(){
  Security(jwt,func(){Scope("read")})
  Payload(func(){Token("private_jwt",String,"Native JWT credential");Field(1,"value",String,"Partial argument text");Field(2,"arguments",MapOf(String,String),"Prior argument values");Required("private_jwt","value")})
  Result(func(){Field(1,"values",ArrayOf(String),"Ordered suggestions",func(){MaxLength(100)})})
  PromptCompletion("prompt","topic");ResourceCompletion("test://items/{id}","id");JSONRPC(func(){})
 })
})
var _=Service("secondary",func(){
 Description("Verify another service has independent protocol inputs")
 MCP("secondary","1")
 JSONRPC(func(){POST("/secondary")})
 Method("read",func(){
  Security(bearer)
  Payload(func(){BearerToken("credential",String,"Native Bearer credential");Field(1,"value",String,"Secondary domain value");Required("credential","value")})
  Result(String);Tool("read","Return the secondary domain value");JSONRPC(func(){})
 })
})
`

const nativeCredentialTest = `package credentialpaths_test
import (
 "context"
 "encoding/json"
 "errors"
 "io"
 "net/http"
 "net/http/httptest"
 "net/url"
 "strings"
 "sync/atomic"
 "testing"

 "github.com/stretchr/testify/assert"
 genservice "credential-paths.local/gen/protected"
 genmcp "credential-paths.local/gen/mcp_protected"
 genserver "credential-paths.local/gen/jsonrpc/mcp_protected/server"
 genclient "credential-paths.local/gen/jsonrpc/mcp_protected/client"
 genspecs "credential-paths.local/gen/protected/toolsets/protected"
 gensecondary "credential-paths.local/gen/secondary"
 gensecondarymcp "credential-paths.local/gen/mcp_secondary"
 gensecondaryserver "credential-paths.local/gen/jsonrpc/mcp_secondary/server"
 gensecondaryclient "credential-paths.local/gen/jsonrpc/mcp_secondary/client"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
 "goa.design/goa-ai/runtime/agent/tools"
)
type (
 principalKey struct{}
 transportContextKey struct{}
 service struct {auth,work,basicCalls atomic.Int64;last string}
)
func(s *service)authorize(ctx context.Context,value string)(context.Context,error){
 s.auth.Add(1)
 if ctx.Value(transportContextKey{})!=true{return ctx,errors.New("transport middleware context missing")}
 if value!="authorized"{return ctx,errors.New("credential rejected")}
 return context.WithValue(ctx,principalKey{},true),nil
}
func(s *service)JWTAuth(ctx context.Context,value string,scheme *security.JWTScheme)(context.Context,error){
 if len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="read"{return ctx,errors.New("scope changed")}
 return s.authorize(ctx,value)
}
func(s *service)OAuth2Auth(ctx context.Context,value string,scheme *security.OAuth2Scheme)(context.Context,error){
 if len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="read"{return ctx,errors.New("scope changed")}
 return s.authorize(ctx,value)
}
func(s *service)BearerAuth(ctx context.Context,value string,_ *security.BearerScheme)(context.Context,error){return s.authorize(ctx,value)}
func(s *service)BasicAuth(ctx context.Context,user,password string,_ *security.BasicScheme)(context.Context,error){
 s.basicCalls.Add(1)
 if user!="user"||password!="password"{return ctx,errors.New("Basic values changed")}
 return s.authorize(ctx,"authorized")
}
func(s *service)APIKeyAuth(ctx context.Context,value string,_ *security.APIKeyScheme)(context.Context,error){
 if value!="opaque key with spaces"{return ctx,errors.New("API key changed")}
 return s.authorize(ctx,"authorized")
}
func(s *service)finish(ctx context.Context,value string)(string,error){
 s.work.Add(1);s.last=value
 if ctx.Value(principalKey{})!=true{return "",errors.New("authenticated context missing")}
 return value,nil
}
func(s *service)JWT(ctx context.Context,p *genservice.JWTPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)Oauth(ctx context.Context,p *genservice.OauthPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)Bearer(ctx context.Context,p *genservice.BearerPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)Basic(ctx context.Context,p *genservice.BasicPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)Key(ctx context.Context,p *genservice.KeyPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)Alternative(ctx context.Context,p *genservice.AlternativePayload)(string,error){
 if p.AuthValue==nil{
  if p.PrivateUser==nil||p.PrivatePassword==nil{return "",errors.New("Basic credential missing")}
 }else if p.PrivateUser!=nil||p.PrivatePassword!=nil{return "",errors.New("inactive Basic credential was filled")}
 return s.finish(ctx,p.Token)
}
func(s *service)Combined(ctx context.Context,p *genservice.CombinedPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)HTTPKey(ctx context.Context,p *genservice.HTTPKeyPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)QueryKey(ctx context.Context,p *genservice.QueryKeyPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)CookieKey(ctx context.Context,p *genservice.CookieKeyPayload)(string,error){return s.finish(ctx,p.Token)}
func(s *service)CredentialOnly(ctx context.Context,_ *genservice.CredentialOnlyPayload)(string,error){return s.finish(ctx,"credential_only")}
func(s *service)Fixed(ctx context.Context,_ *genservice.FixedPayload)(string,error){return s.finish(ctx,"fixed")}
func(s *service)Prompt(ctx context.Context,p *genservice.PromptPayload)(*genservice.PromptResult,error){
 _,err:=s.finish(ctx,p.Topic);return &genservice.PromptResult{},err
}
func(s *service)Resource(ctx context.Context,p *genservice.ResourcePayload)(*genservice.ResourceResult,error){
 _,err:=s.finish(ctx,p.URI);return &genservice.ResourceResult{},err
}
func(s *service)Suggest(ctx context.Context,p *genservice.SuggestPayload)(*genservice.SuggestResult,error){
 _,err:=s.finish(ctx,p.Value+"/"+p.Arguments["topic"]);return &genservice.SuggestResult{Values:[]string{p.Value}},err
}
type doer struct {client *http.Client;basic bool}
func(d *doer)Do(request *http.Request)(*http.Response,error){
 if d.basic{request.SetBasicAuth("user","password")}else{request.Header.Set("Authorization","Bearer authorized")}
 request.Header.Set("X-Synthetic-Key","opaque key with spaces")
 request.Header.Set("X-HTTP-Key","opaque key with spaces")
 query:=request.URL.Query();query.Set("api_key","opaque key with spaces");request.URL.RawQuery=query.Encode()
 request.AddCookie(&http.Cookie{Name:"api_key",Value:"opaque key with spaces"})
 return d.client.Do(request)
}
func TestCredentialsRemainOutsideArguments(t *testing.T){
 s:=&service{}
 endpoints:=genservice.NewEndpoints(s)
 var middleware atomic.Int64
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,input any)(any,error){middleware.Add(1);return next(ctx,input)}})
 mux:=goahttp.NewMuxer()
 adapter:=genmcp.NewMCPAdapter(endpoints,nil)
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 var transportMiddleware atomic.Int64
 genserver.Mount(mux,server)
 server.Use(func(next http.Handler)http.Handler{return http.HandlerFunc(func(writer http.ResponseWriter,request *http.Request){
  transportMiddleware.Add(1)
  ctx:=context.WithValue(request.Context(),transportContextKey{},true)
  next.ServeHTTP(writer,request.WithContext(ctx))
 })})
 peer:=httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,request *http.Request){
  body,err:=io.ReadAll(request.Body);if err!=nil{t.Fatal(err)}
  if err:=request.Body.Close();err!=nil{t.Fatal(err)}
  for _,secret:=range []string{"httpCredential","authorized","opaque key with spaces","example-secret"}{assert.NotContains(t,string(body),secret)}
  request.Body=io.NopCloser(strings.NewReader(string(body)))
  mux.ServeHTTP(writer,request)
 }));defer peer.Close()
 transport:=&doer{client:peer.Client()}
 address,err:=url.Parse(peer.URL);if err!=nil{t.Fatal(err)}
 client:=genclient.NewClient(address.Scheme,address.Host,transport,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 catalog,err:=client.ToolsList()(t.Context(),&genmcp.ToolsListPayload{});if err!=nil{t.Fatal(err)}
 for _,tool:=range catalog.(*genmcp.ToolsListResult).Tools{
  assert.NotContains(t,string(tool.InputSchema),"private_")
  assert.NotContains(t,string(tool.InputSchema),"example-secret")
  if tool.Name!="credential_only"{assert.Contains(t,string(tool.InputSchema),"token")}
 }
 for _,spec:=range genspecs.Specs(){
  domain:=[]byte("{\"token\":\"domain\"}")
  if spec.Name==genspecs.CredentialOnly{domain=[]byte("{}")}
  if _,err:=spec.Payload.Codec.FromJSON(domain);err!=nil{t.Fatalf("%s domain codec: %v",spec.Name,err)}
  assert.NotContains(t,string(spec.Payload.ExampleJSON),"example-secret")
  expectedFields:=2
  if spec.Name==genspecs.CredentialOnly{expectedFields=1}
  if len(spec.Payload.Fields)!=expectedFields{t.Fatalf("%s fields: got %d, want %d",spec.Name,len(spec.Payload.Fields),expectedFields)}
  assert.Empty(t,spec.Payload.Fields[0].Path)
  assert.Equal(t,"object",spec.Payload.Fields[0].JSONType)
  if expectedFields==2{assert.Equal(t,[]tools.FieldPathSegment{tools.FixedField("token")},spec.Payload.Fields[1].Path)}
  for _,field:=range []string{"private_jwt","private_oauth","private_bearer","private_user","private_password","private_key"}{
   if _,err:=spec.Payload.Codec.FromJSON([]byte("{\"token\":\"domain\",\""+field+"\":\"secret\"}"));err==nil{t.Fatalf("%s advertised credential %s",spec.Name,field)}
  }
 }
 for _,name:=range []string{"jwt","oauth","bearer","basic","key","combined","http_key","query_key","cookie_key"}{
  transport.basic=name=="basic"
  result,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:name,Arguments:json.RawMessage("{\"token\":\"domain\"}")})
  if err!=nil{t.Fatalf("%s: %v",name,err)}
  if string(result.(*genmcp.ToolsCallResult).StructuredContent)!="\"domain\""{t.Fatalf("%s domain input changed",name)}
 }
 transport.basic=false
 only,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"credential_only"})
 if err!=nil{t.Fatal(err)}
 assert.Equal(t,"\"credential_only\"",string(only.(*genmcp.ToolsCallResult).StructuredContent))
 // Both alternatives retain Goa's ordered callbacks and inactive nil fields.
 for _,basic:=range []bool{true,false}{
  transport.basic=basic
  beforeAuth,beforeBasic,beforeWork:=s.auth.Load(),s.basicCalls.Load(),s.work.Load()
  result,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"alternative",Arguments:json.RawMessage("{\"token\":\"domain\"}")})
  if err!=nil{t.Fatal(err)}
  assert.Equal(t,"\"domain\"",string(result.(*genmcp.ToolsCallResult).StructuredContent))
  assert.Equal(t,beforeBasic+1,s.basicCalls.Load())
  assert.Equal(t,beforeAuth+1,s.auth.Load())
  assert.Equal(t,beforeWork+1,s.work.Load())
 }
 transport.basic=false
 result,err:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"test://fixed"});if err!=nil{t.Fatal(err)}
 assert.Equal(t,"fixed",*result.(*genmcp.ResourcesReadResult).Contents[0].Text)
 prompts,err:=client.PromptsList()(t.Context(),&genmcp.PromptsListPayload{});if err!=nil{t.Fatal(err)}
 arguments:=prompts.(*genmcp.PromptsListResult).Prompts[0].Arguments
 if len(arguments)!=1||arguments[0].Name!="topic"{t.Fatalf("prompt credential advertised: %+v",arguments)}
 _,err=client.PromptsGet()(t.Context(),&genmcp.PromptsGetPayload{Name:"prompt",Arguments:map[string]string{"topic":"subject"}});if err!=nil{t.Fatal(err)}
 assert.Equal(t,"subject",s.last)
 _,err=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"test://items/a%2Fb"});if err!=nil{t.Fatal(err)}
 assert.Equal(t,"test://items/a%2Fb",s.last)
 for _,reference:=range []struct{kind,name string}{{"ref/prompt","prompt"},{"ref/resource","test://items/{id}"}}{
  ref:=&genmcp.CompletionReference{Type:reference.kind}
  argument:="topic"
  if reference.kind=="ref/prompt"{ref.Name=&reference.name}else{ref.URI=&reference.name;argument="id"}
  _,err=client.CompletionComplete()(t.Context(),&genmcp.CompletionCompletePayload{Ref:ref,Argument:&genmcp.CompletionArgument{Name:argument,Value:"part"}})
  if err!=nil{t.Fatal(err)}
  assert.Equal(t,"part/",s.last)
 }
 assert.Equal(t,int64(17),s.work.Load())
 assert.Equal(t,s.work.Load()+1,s.auth.Load())
 assert.Equal(t,s.work.Load(),middleware.Load())
 assert.Equal(t,int64(19),transportMiddleware.Load())
 // Bearer headers with extra separating spaces or mixed-case names return the same domain result.
 for _,header:=range []string{"Bearer  authorized","bEaReR   authorized"}{
  beforeAuth,beforeWork,beforeMiddleware:=s.auth.Load(),s.work.Load(),middleware.Load()
  spaced:=genclient.NewClient(address.Scheme,address.Host,&authorizationDoer{client:peer.Client(),value:header},goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
  result,err:=spaced.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"jwt",Arguments:json.RawMessage("{\"token\":\"domain\"}")})
  if err!=nil{t.Fatal(err)}
  assert.Equal(t,"\"domain\"",string(result.(*genmcp.ToolsCallResult).StructuredContent))
  assert.Equal(t,beforeAuth+1,s.auth.Load())
  assert.Equal(t,beforeWork+1,s.work.Load())
  assert.Equal(t,beforeMiddleware+1,middleware.Load())
 }
 // Domain injection is rejected before middleware, even with a valid header.
 before:=s.work.Load()
 rejected,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"jwt",Arguments:json.RawMessage("{\"token\":\"domain\",\"private_jwt\":\"injected\"}")})
 if err!=nil{t.Fatal(err)}
 if rejected.(*genmcp.ToolsCallResult).IsError==nil||!*rejected.(*genmcp.ToolsCallResult).IsError{t.Fatal("credential injection accepted")}
 assert.Equal(t,before,s.work.Load())
 assert.Equal(t,before,middleware.Load())
 // Missing and malformed Bearer inputs fail before the configured endpoint.
 for _,header:=range []string{"","Basic bad","Bearer two tokens","Bearer\t authorized","Bearer \tauthorized","Bearer    "}{
  uncredentialed:=genclient.NewClient(address.Scheme,address.Host,&authorizationDoer{client:peer.Client(),value:header},goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
  failure,err:=uncredentialed.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"jwt",Arguments:json.RawMessage("{\"token\":\"domain\"}")})
  if err!=nil{t.Fatal(err)}
  if failure.(*genmcp.ToolsCallResult).IsError==nil||!*failure.(*genmcp.ToolsCallResult).IsError{t.Fatalf("invalid credential accepted: %q",header)}
  assert.Equal(t,before,s.work.Load())
  assert.Equal(t,before,middleware.Load())
 }
 // Invalid protocol envelopes stop before transport middleware or endpoint work.
 beforeTransport,beforeAuth:=transportMiddleware.Load(),s.auth.Load()
 for _,body:=range []string{"{invalid","{}","{\"jsonrpc\":\"2.0\",\"id\":\"bad\",\"method\":\"server/discover\",\"params\":{}}"}{
  request,err:=http.NewRequestWithContext(t.Context(),http.MethodPost,peer.URL+"/mcp",strings.NewReader(body));if err!=nil{t.Fatal(err)}
  request.Header.Set("Content-Type","application/json")
  response,err:=peer.Client().Do(request);if err!=nil{t.Fatal(err)}
  if err:=response.Body.Close();err!=nil{t.Fatal(err)}
  assert.Equal(t,http.StatusBadRequest,response.StatusCode)
  assert.Equal(t,beforeTransport,transportMiddleware.Load())
  assert.Equal(t,beforeAuth,s.auth.Load())
  assert.Equal(t,before,s.work.Load())
  assert.Equal(t,before,middleware.Load())
 }
 // Catalog parameters contain no transport field or credential value.
 encoded,err:=json.Marshal(catalog);if err!=nil{t.Fatal(err)}
 if strings.Contains(string(encoded),"httpCredential")||strings.Contains(string(encoded),"authorized"){t.Fatal("catalog contains transport authentication input")}
 }
type authorizationDoer struct{client *http.Client;value string}
func(d *authorizationDoer)Do(request *http.Request)(*http.Response,error){
 request.Header.Set("Authorization",d.value)
 return d.client.Do(request)
}
type secondaryService struct{}
func(*secondaryService)BearerAuth(ctx context.Context,value string,_ *security.BearerScheme)(context.Context,error){
 if value!="authorized"{return ctx,errors.New("secondary credential changed")}
 return context.WithValue(ctx,principalKey{},true),nil
}
func(*secondaryService)Read(ctx context.Context,p *gensecondary.ReadPayload)(string,error){
 if ctx.Value(principalKey{})!=true{return "",errors.New("secondary context missing")}
 return p.Value,nil
}
func TestServicesKeepIndependentCredentialInputs(t *testing.T){
 mux:=goahttp.NewMuxer()
 adapter:=gensecondarymcp.NewMCPAdapter(gensecondary.NewEndpoints(&secondaryService{}),nil)
 server:=gensecondaryserver.New(gensecondarymcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 gensecondaryserver.Mount(mux,server)
 peer:=httptest.NewServer(mux);defer peer.Close()
 address,err:=url.Parse(peer.URL);if err!=nil{t.Fatal(err)}
 client:=gensecondaryclient.NewClient(address.Scheme,address.Host,&doer{client:peer.Client()},goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 result,err:=client.ToolsCall()(t.Context(),&gensecondarymcp.ToolsCallPayload{Name:"read",Arguments:json.RawMessage("{\"value\":\"secondary-domain\"}")})
 if err!=nil{t.Fatal(err)}
 assert.Equal(t,"\"secondary-domain\"",string(result.(*gensecondarymcp.ToolsCallResult).StructuredContent))
}
`
