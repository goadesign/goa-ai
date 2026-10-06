// These tests generate an MCP client and give it the built-in machine or browser
// transport. The synthetic issuer verifies that generated caller construction
// preserves authorization and sends only a bearer token to its MCP endpoint.
package codegen

import "testing"

func TestMCPGeneratedClientUsesOAuth(t *testing.T) {
	runNativeCredentialPaths(t, oauthClientDesign, oauthClientTest)
}

const oauthClientDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _ = API("oauth_client", func(){ Description("Verify the generated MCP client's built-in authorization") })
var _ = Service("records",func(){
 Description("Read synthetic records through an authorized MCP client")
 MCP("records", "1")
 JSONRPC(func(){POST("/mcp")})
 Method("read",func(){
  Description("Return one synthetic record after resource authorization")
  Result(String)
  Tool("read", "Read one synthetic record")
 })
})
`

const oauthClientTest = `// This fixture exercises generated caller construction with real metadata
// and token HTTP requests. The bearer token itself is a synthetic opaque value.
package oauth_test
import (
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

 genclient "credential-paths.local/gen/jsonrpc/mcp_records/client"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 "golang.org/x/oauth2"
)
func TestGeneratedOAuthCaller(t *testing.T){
 for _,profile:=range []string{"machine","browser"} {t.Run(profile,func(t *testing.T){
 var origin, challenge string
 var tokens, calls atomic.Int32
 server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("Content-Type","application/json")
  var body string
  switch r.URL.Path {
  case "/.well-known/oauth-protected-resource/mcp":
   body=fmt.Sprintf("{\"resource\":%q,\"authorization_servers\":[%q]}",origin+"/mcp",origin+"/issuer")
  case "/.well-known/oauth-authorization-server/issuer":
   body=fmt.Sprintf("{\"issuer\":%q,\"token_endpoint\":%q,\"grant_types_supported\":[\"client_credentials\",\"authorization_code\"],\"token_endpoint_auth_methods_supported\":[\"client_secret_basic\",\"client_secret_post\",\"none\"],\"code_challenge_methods_supported\":[\"S256\"],\"authorization_endpoint\":%q,\"authorization_response_iss_parameter_supported\":true}",origin+"/issuer",origin+"/token",origin+"/authorize")
  case "/token":
   tokens.Add(1)
   if err:=r.ParseForm();err!=nil {t.Error(err);return}
   if r.PostForm.Get("resource")!=origin+"/mcp" || r.Header.Get("Authorization")!="" {t.Error("wrong credential delivery")}
   if profile=="machine" {
    if r.PostForm.Get("client_secret")!="registered-secret" {t.Error("missing client secret")}
   } else if r.PostForm.Get("client_secret")!="" || r.PostForm.Get("code")!="browser-code" || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier"))!=challenge {t.Error("wrong PKCE code exchange")}
   body="{\"access_token\":\"opaque-token\",\"token_type\":\"Bearer\",\"expires_in\":3600}"
  case "/mcp":
   calls.Add(1)
   if r.Header.Get("Authorization")!="Bearer opaque-token" {t.Error("bearer credential missing")}
   encoded,err:=io.ReadAll(r.Body);if err!=nil {t.Error(err);return}
   if strings.Contains(string(encoded),"registered-secret") || strings.Contains(string(encoded),"opaque-token") {t.Error("credentials entered MCP body")}
   // The shared transport restores the caller's request ID before decoding.
   var envelope struct { ID string }
   decoder:=json.NewDecoder(strings.NewReader(string(encoded)))
   if err:=decoder.Decode(&envelope);err!=nil {t.Error(err);return}
   body=fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%q,\"result\":{\"resultType\":\"complete\",\"content\":[],\"structuredContent\":\"record\"}}",envelope.ID)
  default:
   w.WriteHeader(http.StatusNotFound)
  }
  if _,err:=io.WriteString(w,body);err!=nil {t.Error(err)}
 }))
 defer server.Close()
 origin=server.URL
 info:=mcpruntime.ClientInfo{Name:"generated-host",Version:"1"}
 var transport *mcpruntime.HTTPTransport
 var err error
 if profile=="machine" {
 transport,err=mcpruntime.NewClientCredentialsHTTPTransport(mcpruntime.HTTPOptions{Endpoint:origin+"/mcp",Client:server.Client(),ClientInfo:info},mcpruntime.ClientCredentials{Issuer:origin+"/issuer",ClientID:"registered",ClientSecret:"registered-secret"})
 } else {
 transport,err=mcpruntime.NewAuthorizationCodeHTTPTransport(mcpruntime.HTTPOptions{Endpoint:origin+"/mcp",Client:server.Client(),ClientInfo:info},mcpruntime.AuthorizationCode{
  Issuer:origin+"/issuer",ClientID:"registered",RedirectURI:"https://host.example/callback",
  Authorize:func(_ context.Context, address string)(string,error){
   parsed,err:=url.Parse(address);if err!=nil {return "",err}
   values:=parsed.Query();challenge=values.Get("code_challenge")
   if values.Get("resource")!=origin+"/mcp" || values.Get("code_challenge_method")!="S256" {t.Error("wrong authorization request")}
   response:=url.Values{"code":{"browser-code"},"state":{values.Get("state")},"iss":{origin+"/issuer"}}
   return "https://host.example/callback?"+response.Encode(),nil
  },
 })
 }
 if err!=nil {t.Fatal(err)}
 address,err:=url.Parse(origin);if err!=nil {t.Fatal(err)}
 client:=genclient.NewClient(address.Scheme,address.Host,transport,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 caller,err:=genclient.NewCaller(client,info,mcpruntime.InputSupport{},mcpruntime.HTTPRetryPolicy{})
 if err!=nil {t.Fatal(err)}
 for range 2 {
  result,err:=caller.CallTool(context.Background(),mcpruntime.CallRequest{Tool:"read",Payload:[]byte("{}")})
  if err!=nil {t.Fatal(err)}
  if string(result.StructuredContent)!="\"record\"" {t.Fatalf("wrong result: %s",result.StructuredContent)}
 }
 if calls.Load()!=2 || tokens.Load()!=1 {t.Fatalf("calls=%d tokens=%d",calls.Load(),tokens.Load())}
 })}
}
`
