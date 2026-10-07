// These tests generate an MCP client and give it a built-in registered grant
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
 "crypto/rand"
 "crypto/rsa"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "net/http/httptest"
 "net/url"
 "strings"
 "sync/atomic"
 "testing"
 "time"

 "github.com/go-jose/go-jose/v4"
 "github.com/go-jose/go-jose/v4/jwt"

 genclient "credential-paths.local/gen/jsonrpc/mcp_records/client"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 "golang.org/x/oauth2"
)
func TestGeneratedOAuthCaller(t *testing.T){
 for _,profile:=range []string{"machine","browser","assertion","enterprise"} {t.Run(profile,func(t *testing.T){
 var origin, challenge, signedAssertion string
 var signingKey *rsa.PrivateKey
 var signer jose.Signer
 if profile=="assertion" {
  var err error
  signingKey,err=rsa.GenerateKey(rand.Reader,2048);if err!=nil {t.Fatal(err)}
  signer,err=jose.NewSigner(jose.SigningKey{Algorithm:jose.RS256,Key:signingKey},nil);if err!=nil {t.Fatal(err)}
 }
 var tokens, calls atomic.Int32
 server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("Content-Type","application/json")
  var body string
  switch r.URL.Path {
  case "/.well-known/oauth-protected-resource/mcp":
   body=fmt.Sprintf("{\"resource\":%q,\"authorization_servers\":[%q]}",origin+"/mcp",origin+"/issuer")
  case "/.well-known/oauth-authorization-server/issuer":
   body=fmt.Sprintf("{\"issuer\":%q,\"token_endpoint\":%q,\"grant_types_supported\":[\"client_credentials\",\"authorization_code\"],\"token_endpoint_auth_methods_supported\":[\"client_secret_basic\",\"client_secret_post\",\"none\",\"private_key_jwt\"],\"token_endpoint_auth_signing_alg_values_supported\":[\"RS256\"],\"code_challenge_methods_supported\":[\"S256\"],\"authorization_endpoint\":%q,\"authorization_response_iss_parameter_supported\":true}",origin+"/issuer",origin+"/token",origin+"/authorize")
  case "/.well-known/oauth-authorization-server/identity":
   body=fmt.Sprintf("{\"issuer\":%q,\"token_endpoint\":%q,\"token_endpoint_auth_methods_supported\":[\"none\"]}",origin+"/identity",origin+"/identity/token")
  case "/identity/token":
   if err:=r.ParseForm();err!=nil {t.Error(err);return}
   if r.Header.Get("Authorization")!="" || r.PostForm.Get("client_id")!="identity-application" || r.PostForm.Get("subject_token")!="host-identity" || r.PostForm.Get("subject_token_type")!="urn:ietf:params:oauth:token-type:id_token" || r.PostForm.Get("requested_token_type")!="urn:ietf:params:oauth:token-type:id-jag" || r.PostForm.Get("audience")!=origin+"/issuer" || r.PostForm.Get("resource")!=origin+"/mcp" {t.Error("wrong independent identity exchange")}
   body="{\"issued_token_type\":\"urn:ietf:params:oauth:token-type:id-jag\",\"access_token\":\"a.b.c\",\"token_type\":\"N_A\",\"expires_in\":60}"
  case "/token":
   tokens.Add(1)
   if err:=r.ParseForm();err!=nil {t.Error(err);return}
   if r.PostForm.Get("resource")!=origin+"/mcp" || r.Header.Get("Authorization")!="" {t.Error("wrong credential delivery")}
   if profile=="machine" {
    if r.PostForm.Get("client_secret")!="registered-secret" {t.Error("missing client secret")}
   } else if profile=="browser" {
    if r.PostForm.Get("client_secret")!="" || r.PostForm.Get("code")!="browser-code" || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier"))!=challenge {t.Error("wrong PKCE code exchange")}
   } else if profile=="enterprise" {
    if r.PostForm.Get("client_secret")!="registered-secret" || r.PostForm.Get("grant_type")!="urn:ietf:params:oauth:grant-type:jwt-bearer" || r.PostForm.Get("assertion")!="a.b.c" || r.PostForm.Get("subject_token")!="" {t.Error("wrong identity redemption")}
   } else {
    signedAssertion=r.PostForm.Get("client_assertion")
    token,err:=jwt.ParseSigned(signedAssertion,[]jose.SignatureAlgorithm{jose.RS256})
    if err!=nil {t.Error(err);w.WriteHeader(http.StatusUnauthorized);return}
    var claims jwt.Claims
    if err:=token.Claims(&signingKey.PublicKey,&claims);err!=nil {t.Error(err);w.WriteHeader(http.StatusUnauthorized);return}
    if claims.Subject!="registered" || claims.Issuer!="registered-signer" || len(claims.Audience)!=1 || claims.Audience[0]!="registered-audience" || claims.Expiry==nil || !time.Now().Before(claims.Expiry.Time()) || claims.ID=="" || r.PostForm.Get("client_id")!="" || r.PostForm.Get("client_secret")!="" || r.PostForm.Get("grant_type")!="client_credentials" || r.PostForm.Get("client_assertion_type")!="urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
     t.Error("wrong registered assertion grant");w.WriteHeader(http.StatusUnauthorized);return
    }
   }
   body="{\"access_token\":\"opaque-token\",\"token_type\":\"Bearer\",\"expires_in\":3600}"
  case "/mcp":
   calls.Add(1)
   if r.Header.Get("Authorization")!="Bearer opaque-token" {t.Error("bearer credential missing")}
   encoded,err:=io.ReadAll(r.Body);if err!=nil {t.Error(err);return}
   if strings.Contains(string(encoded),"registered-secret") || strings.Contains(string(encoded),"opaque-token") {t.Error("credentials entered MCP body")}
   if signedAssertion!="" && strings.Contains(string(encoded),signedAssertion) {t.Error("assertion entered MCP body")}
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
 registration,registrationErr:=mcpruntime.NewSecretClientRegistration(origin+"/issuer","registered","registered-secret");if registrationErr!=nil {t.Fatal(registrationErr)}
 transport,err=mcpruntime.NewClientCredentialsHTTPTransport(mcpruntime.HTTPOptions{Endpoint:origin+"/mcp",Client:server.Client(),ClientInfo:info},mcpruntime.ClientCredentials{Store: mcpruntime.NewMemoryAuthorizationStore(), Registration:registration})
 } else if profile=="browser" {
 registration,registrationErr:=mcpruntime.NewPublicClientRegistration(origin+"/issuer","registered");if registrationErr!=nil {t.Fatal(registrationErr)}
 transport,err=mcpruntime.NewAuthorizationCodeHTTPTransport(mcpruntime.HTTPOptions{Endpoint:origin+"/mcp",Client:server.Client(),ClientInfo:info},mcpruntime.AuthorizationCode{Store: mcpruntime.NewMemoryAuthorizationStore(),
  Registration:registration,RedirectURI:"https://host.example/callback",
  Authorize:func(_ context.Context, address string)(string,error){
   parsed,err:=url.Parse(address);if err!=nil {return "",err}
   values:=parsed.Query();challenge=values.Get("code_challenge")
   if values.Get("resource")!=origin+"/mcp" || values.Get("code_challenge_method")!="S256" {t.Error("wrong authorization request")}
   response:=url.Values{"code":{"browser-code"},"state":{values.Get("state")},"iss":{origin+"/issuer"}}
   return "https://host.example/callback?"+response.Encode(),nil
  },
 })
 } else if profile=="enterprise" {
 identityRegistration,registrationErr:=mcpruntime.NewPublicClientRegistration(origin+"/identity","identity-application");if registrationErr!=nil{t.Fatal(registrationErr)}
 identity,identityErr:=mcpruntime.NewIDTokenEnterpriseIdentity(identityRegistration,server.Client(),mcpruntime.NewMemoryAuthorizationStore(),func(context.Context)(string,error){return "host-identity",nil});if identityErr!=nil{t.Fatal(identityErr)}
 registration,registrationErr:=mcpruntime.NewSecretClientRegistration(origin+"/issuer","registered","registered-secret");if registrationErr!=nil{t.Fatal(registrationErr)}
 transport,err=mcpruntime.NewEnterpriseHTTPTransport(mcpruntime.HTTPOptions{Endpoint:origin+"/mcp",Client:server.Client(),ClientInfo:info},mcpruntime.EnterpriseAuthorization{Identity:identity,Registration:registration})
 } else {
 registration,registrationErr:=mcpruntime.NewSignedClientRegistration(mcpruntime.ClientAssertion{
  Issuer:origin+"/issuer",ClientID:"registered",AssertionIssuer:"registered-signer",Audience:"registered-audience",Lifetime:time.Minute,Signer:signer,
 });if registrationErr!=nil {t.Fatal(registrationErr)}
 transport,err=mcpruntime.NewClientCredentialsHTTPTransport(mcpruntime.HTTPOptions{Endpoint:origin+"/mcp",Client:server.Client(),ClientInfo:info},mcpruntime.ClientCredentials{Store: mcpruntime.NewMemoryAuthorizationStore(), Registration:registration})
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
