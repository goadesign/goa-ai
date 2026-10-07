// These tests compile native MCP clients and servers with query credentials.
// Real metadata and grant requests prove that OAuth identifies the configured
// resource, while original service authentication receives its separate key.
package codegen

import "testing"

func TestMCPGeneratedOAuthQueryCredentials(t *testing.T) {
	runNativeCredentialPaths(t, oauthQueryCredentialDesign, oauthQueryCredentialTest)
}

const oauthQueryCredentialDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var key=APIKeySecurity("domain_key")
var text=Type("Text",func(){Field(1,"text",String,"Prompt text");Required("text")})
var message=Type("Message",func(){
 Field(1,"role",String,"Message author",func(){Enum("user")})
 OneOf("content","Selected content",func(){TypeName("PromptContent");Attribute("text",text,"Text instructions")})
 Required("role","content")
})
var resourceText=Type("ResourceText",func(){Field(1,"uri",String,"Exact resource address",func(){Format(FormatURI)});Field(2,"text",String,"Resource text");Required("uri","text")})
var resourceItem=Type("ResourceItem",func(){OneOf("content","Selected resource representation",func(){TypeName("ResourceContent");Attribute("text",resourceText,"Text resource")});Required("content")})
var uri=Type("ResourceURI",String,func(){Format(FormatURI)})
var acknowledged=Type("Acknowledged",func(){Field(1,"resources",ArrayOf(uri),"Accepted resource addresses")})
var updated=Type("Updated",func(){Field(1,"uri",String,"Changed resource address",func(){Format(FormatURI)});Required("uri")})
var event=Type("ResourceEvent",func(){
 OneOf("change","Acknowledgment or resource change",func(){Attribute("acknowledged",acknowledged,"Accepted selection");Attribute("updated",updated,"Changed resource")})
 Required("change")
})
var _=API("oauth_query",func(){Description("Verify distinct OAuth resource and domain credentials")})
var _=Service("records",func(){
 Description("Read synthetic domain values using a separate query credential")
 MCP("records","1")
 JSONRPC(func(){POST("/mcp")})
 Method("read",func(){
  Description("Return a synthetic record after native domain authentication")
  Security(key)
  Payload(func(){APIKey("domain_key","credential",String,"Domain query credential");Required("credential")})
  JSONRPC(func(){Param("credential:api_key")})
  Result(String);Tool("read","Read one synthetic record")
 })
 Method("fixed",func(){
  Description("Read the fixed synthetic resource after domain authentication")
  Security(key)
  Payload(func(){APIKey("domain_key","credential",String,"Domain query credential");Required("credential")})
  JSONRPC(func(){Param("credential:api_key")})
  Result(String);Resource("fixed","test://fixed","text/plain")
 })
 Method("resource",func(){
  Description("Read a selected synthetic resource after domain authentication")
  Security(key)
  Payload(func(){APIKey("domain_key","credential",String,"Domain query credential");Field(1,"uri",String,"Requested resource",func(){Format(FormatURI)});Required("credential","uri")})
  JSONRPC(func(){Param("credential:api_key")})
  Result(func(){Field(1,"contents",ArrayOfRequired(resourceItem),"Resource content")})
  ResourceTemplate("items","test://items/{id}","text/plain")
 })
 Method("prompt",func(){
  Description("Build a prompt for the selected topic after domain authentication")
  Security(key)
  Payload(func(){APIKey("domain_key","credential",String,"Domain query credential");Field(1,"topic",String,"Requested topic");Required("credential","topic")})
  JSONRPC(func(){Param("credential:api_key")})
  Result(func(){Field(1,"messages",ArrayOfRequired(message),"Prompt messages")})
  Prompt("prompt","Instructions for the topic")
 })
 Method("suggest",func(){
  Description("Complete a partially entered topic or resource selection")
  Security(key)
  Payload(func(){APIKey("domain_key","credential",String,"Domain query credential");Field(1,"value",String,"Partial value");Field(2,"arguments",MapOf(String,String),"Previously entered argument values");Required("credential","value")})
  JSONRPC(func(){Param("credential:api_key")})
  Result(func(){Field(1,"values",ArrayOf(String),"Suggestions",func(){MaxLength(100)})})
  PromptCompletion("prompt","topic");ResourceCompletion("test://items/{id}","id")
 })
 Method("watch",func(){
  Description("Authorize resource selections and send their changes")
  Security(key)
  Payload(func(){APIKey("domain_key","credential",String,"Domain query credential");Field(1,"resources",ArrayOf(uri),"Requested resource addresses");Required("credential")})
  JSONRPC(func(){Param("credential:api_key");ServerSentEvents()})
  StreamingResult(event);ResourceSubscription()
 })
})
`

// #nosec G101 -- These credentials belong only to the synthetic local issuer and service.
const oauthQueryCredentialTest = `package querycredentials_test
import (
 "context"
 "errors"
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
 genrecords "credential-paths.local/gen/records"
 genmcp "credential-paths.local/gen/mcp_records"
 genserver "credential-paths.local/gen/jsonrpc/mcp_records/server"
 genclient "credential-paths.local/gen/jsonrpc/mcp_records/client"
 "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 "goa.design/goa/v3/security"
)
type (
 principalKey struct{}
 service struct{auth,work atomic.Int32}
)
func(s *service)APIKeyAuth(ctx context.Context,key string,_ *security.APIKeyScheme)(context.Context,error){
 s.auth.Add(1)
 if key!="domain key +/"{return ctx,errors.New("domain query credential changed")}
 return context.WithValue(ctx,principalKey{},true),nil
}
func(s *service)finish(ctx context.Context)error{
 s.work.Add(1)
 if ctx.Value(principalKey{})!=true{return errors.New("native domain principal missing")}
 return nil
}
func(s *service)Read(ctx context.Context,_ *genrecords.ReadPayload)(string,error){return "record",s.finish(ctx)}
func(s *service)Fixed(ctx context.Context,_ *genrecords.FixedPayload)(string,error){return "fixed",s.finish(ctx)}
func(s *service)Resource(ctx context.Context,p *genrecords.ResourcePayload)(*genrecords.ResourceResult,error){
 if p.URI!="test://items/one"{return nil,errors.New("resource URI changed")}
 return &genrecords.ResourceResult{},s.finish(ctx)
}
func(s *service)Prompt(ctx context.Context,p *genrecords.PromptPayload)(*genrecords.PromptResult,error){
 if p.Topic!="subject"{return nil,errors.New("prompt argument changed")}
 return &genrecords.PromptResult{},s.finish(ctx)
}
func(s *service)Suggest(ctx context.Context,p *genrecords.SuggestPayload)(*genrecords.SuggestResult,error){
 return &genrecords.SuggestResult{Values:[]string{p.Value}},s.finish(ctx)
}
func(s *service)Watch(ctx context.Context,p *genrecords.WatchPayload,stream genrecords.WatchServerStream)error{
 if err:=s.finish(ctx);err!=nil{return err}
 if err:=stream.Send(&genrecords.ResourceEvent{Change:genrecords.NewChangeAcknowledged(&genrecords.Acknowledged{Resources:p.Resources})});err!=nil{return err}
 if err:=stream.Send(&genrecords.ResourceEvent{Change:genrecords.NewChangeUpdated(&genrecords.Updated{URI:"test://fixed"})});err!=nil{return err}
 return stream.Close()
}
func TestGeneratedOAuthQueryCredentials(t *testing.T){
 s:=&service{}
 mux:=goahttp.NewMuxer()
 adapter:=genmcp.NewMCPAdapter(genrecords.NewEndpoints(s),nil)
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 server.Mount(mux)
 var origin string
 var tokens,calls atomic.Int32
 peer:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  var body string
  switch r.URL.Path{
  case "/.well-known/oauth-protected-resource/mcp":
   assert.Empty(t,r.URL.RawQuery)
   body=fmt.Sprintf("{\"resource\":%q,\"authorization_servers\":[%q]}",origin+"/mcp",origin+"/issuer")
  case "/.well-known/oauth-authorization-server/issuer":
   assert.Empty(t,r.URL.RawQuery)
   body=fmt.Sprintf("{\"issuer\":%q,\"token_endpoint\":%q,\"grant_types_supported\":[\"client_credentials\"],\"token_endpoint_auth_methods_supported\":[\"client_secret_basic\",\"client_secret_post\"]}",origin+"/issuer",origin+"/token")
  case "/token":
   tokens.Add(1)
   if err:=r.ParseForm();err!=nil{t.Error(err);return}
   assert.Equal(t,origin+"/mcp",r.PostForm.Get("resource"))
   assert.Equal(t,"registered-secret",r.PostForm.Get("client_secret"))
   assert.Empty(t,r.PostForm.Get("api_key"))
   assert.NotContains(t,r.PostForm.Encode(),"domain+key")
   body="{\"access_token\":\"resource-token\",\"token_type\":\"Bearer\",\"expires_in\":3600}"
  case "/mcp":
   calls.Add(1)
   assert.Equal(t,"Bearer resource-token",r.Header.Get("Authorization"))
   assert.Equal(t,"domain key +/",r.URL.Query().Get("api_key"))
   encoded,err:=io.ReadAll(r.Body);if err!=nil{t.Error(err);return}
   if err:=r.Body.Close();err!=nil{t.Error(err);return}
   for _,secret:=range []string{"domain key", "resource-token", "registered-secret", "httpCredential", "api_key"}{assert.NotContains(t,string(encoded),secret)}
   r.Body=io.NopCloser(strings.NewReader(string(encoded)))
   mux.ServeHTTP(w,r);return
  default:w.WriteHeader(http.StatusNotFound);return
  }
  w.Header().Set("Content-Type","application/json")
  if _,err:=io.WriteString(w,body);err!=nil{t.Error(err)}
 }));defer peer.Close()
 origin=peer.URL
 info:=mcp.ClientInfo{Name:"generated-host",Version:"1"}
 registration,err:=mcp.NewSecretClientRegistration(origin+"/issuer","registered","registered-secret");require.NoError(t,err)
 transport,err:=mcp.NewClientCredentialsHTTPTransport(mcp.HTTPOptions{Endpoint:origin+"/mcp",Client:peer.Client(),ClientInfo:info},mcp.ClientCredentials{Store: mcp.NewMemoryAuthorizationStore(), Registration:registration})
 require.NoError(t,err)
 address,err:=url.Parse(origin);require.NoError(t,err)
 client:=genclient.NewClient(address.Scheme,address.Host,transport,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 credential:="domain key +/"
 for range 2{
  result,err:=client.ToolsCall()(t.Context(),&genmcp.ToolsCallPayload{Name:"read",HTTPCredential0:&credential});require.NoError(t,err)
  completedReply1, isCompleteReply1 := result.(*genmcp.ToolsCallResult).Outcome.AsComplete(); if !isCompleteReply1 {t.Fatalf("expected completed MCP result: %+v", result)}
  assert.Equal(t,"\"record\"",string(completedReply1.StructuredContent))
  for _,uri:=range []string{"test://fixed","test://items/one"}{
   _,err=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:uri,HTTPCredential0:&credential});require.NoError(t,err)
  }
  _,err=client.PromptsGet()(t.Context(),&genmcp.PromptsGetPayload{Name:"prompt",Arguments:map[string]string{"topic":"subject"},HTTPCredential0:&credential});require.NoError(t,err)
  for _,kind:=range []string{"ref/prompt","ref/resource"}{
   ref:=&genmcp.CompletionReference{Type:kind}
   name,uri,argument:="prompt","test://items/{id}","topic"
   if kind=="ref/prompt"{ref.Name=&name}else{ref.URI=&uri;argument="id"}
   _,err=client.CompletionComplete()(t.Context(),&genmcp.CompletionCompletePayload{Ref:ref,Argument:&genmcp.CompletionArgument{Name:argument,Value:"part"},HTTPCredential0:&credential});require.NoError(t,err)
  }
  events:=0
  ctx:=mcp.WithSubscriptionEvents(t.Context(),func(_ context.Context,event mcp.SubscriptionEvent)error{events++;return nil})
  _,err=client.SubscriptionsListen()(ctx,&genmcp.SubscriptionsListenPayload{Notifications:&genmcp.SubscriptionFilter{ResourceSubscriptions:[]string{"test://fixed"}},HTTPCredential0:&credential});require.NoError(t,err)
  assert.Equal(t,2,events)
 }
 assert.EqualValues(t,1,tokens.Load())
 assert.EqualValues(t,14,calls.Load())
 assert.EqualValues(t,14,s.auth.Load())
 assert.EqualValues(t,14,s.work.Load())
}
`
