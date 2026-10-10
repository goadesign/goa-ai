// These tests compile the native resource policy and verify signed access tokens
// through generated MCP operations. Domain query keys and original security
// callbacks remain independent, and denied requests cannot reach middleware.
package codegen

import (
	"strings"
	"testing"
)

func TestMCPJWTResourceServerUsesNativeOperations(t *testing.T) {
	design, runtime := jwtResourceFixture()
	runNativeCredentialPaths(t, design, runtime)
}

func TestMCPJWTResourceServerUsesLegacyOperations(t *testing.T) {
	design, runtime := jwtResourceFixture()
	before, calls, found := strings.Cut(runtime, " send:=func(method,name,token string)")
	if !found {
		t.Fatal("resource fixture lacks the raw HTTP requests")
	}
	bodyStart := strings.Index(calls, "  body:=")
	bodyEnd := strings.Index(calls, "  request:=")
	if bodyStart < 0 || bodyEnd < bodyStart {
		t.Fatal("resource fixture lacks the HTTP envelope")
	}
	calls = calls[:bodyStart] + `  body:=fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":%q,\"params\":{%s\"arguments\":{}}}",method,fields)
` + calls[bodyEnd:]
	calls = strings.ReplaceAll(calls, "mcp.ProtocolVersion", "mcp.LegacyProtocolVersion")
	runNativeCredentialPaths(t, design, before+" send:=func(method,name,token string)"+calls)
}

func TestMCPJWTResourceServerPreservesAPISecurity(t *testing.T) {
	design, runtime := jwtResourceFixture()
	design = strings.Replace(design, `MCP("records","1",func(){Security(resourceOAuth,func(){Scope("catalog:read")})})`, `MCP("records","1")`, 1)
	design = strings.Replace(design, `Description("Verify distinct OAuth resource and domain credentials")`, `Description("Verify distinct OAuth resource and domain credentials");Security(resourceOAuth,func(){Scope("catalog:read")})`, 1)
	runNativeCredentialPaths(t, design, runtime)
}

func TestMCPJWTResourceServerPreservesServiceSecurity(t *testing.T) {
	design, runtime := jwtResourceFixture()
	design = strings.Replace(design, `MCP("records","1",func(){Security(resourceOAuth,func(){Scope("catalog:read")})})`, `Security(resourceOAuth,func(){Scope("catalog:read")});MCP("records","1")`, 1)
	runNativeCredentialPaths(t, design, runtime)
}

func TestMCPJWTResourceServerAcceptsCompleteScopeAlternative(t *testing.T) {
	design, runtime := jwtResourceFixture()
	design = strings.Replace(design, `Scope("catalog:read","Read the basic catalog")`, `Scope("catalog:read","Read the basic catalog");Scope("catalog:admin","Administer the basic catalog")`, 1)
	design = strings.Replace(design, `MCP("records","1",func(){Security(resourceOAuth,func(){Scope("catalog:read")})})`, `MCP("records","1",func(){Security(resourceOAuth,func(){Scope("catalog:admin")});Security(resourceOAuth,func(){Scope("catalog:read")})})`, 1)
	runtime = strings.Replace(runtime, `assert.Contains(t,denied.Header().Get("WWW-Authenticate"),"catalog:read operation:read")`, `assert.Contains(t,denied.Header().Get("WWW-Authenticate"),"catalog:admin operation:read")`, 1)
	runNativeCredentialPaths(t, design, runtime)
}

// jwtResourceFixture extends the complete native query-credential fixture with
// an explicit bearer policy and per-operation scopes. The issuer signs real
// access tokens; the generated guard and original callbacks each keep ownership.
func jwtResourceFixture() (string, string) {
	design := strings.Replace(oauthQueryCredentialDesign, `var key=`, `var resourceOAuth=OAuth2Security("resource_oauth",func(){
 Description("Authorize resource access with issuer-signed access tokens")
 Scope("catalog:read","Read the basic catalog")
 Scope("operation:read","Read resources, prompts and suggestions")
 AuthorizationCodeFlow("https://issuer.example/authorize","https://issuer.example/token","")
})
var key=`, 1)
	design = strings.Replace(design, `MCP("records","1")`, `MCP("records","1",func(){Security(resourceOAuth,func(){Scope("catalog:read")})})`, 1)
	design = strings.ReplaceAll(design, `Security(key)`, `Security(key,resourceOAuth,func(){Scope("operation:read")})`)
	design = strings.ReplaceAll(design, `Payload(func(){APIKey(`, `Payload(func(){AccessToken("access",String,"Resource access token supplied by the authorized transport");Required("access");APIKey(`)
	runtime := strings.Replace(oauthQueryCredentialTest, `"context"`, `"context"
 "crypto/ed25519"
 "crypto/rand"
 "time"
 "github.com/go-jose/go-jose/v4"
 "github.com/go-jose/go-jose/v4/jwt"`, 1)
	// This payload declares access before credential. Index zero is the bearer
	// field and index one is the independent domain query key.
	runtime = strings.ReplaceAll(runtime, "HTTPCredential0:&credential", "HTTPCredential1:&credential")
	runtime = strings.Replace(runtime, `service struct{auth,work atomic.Int32}`, `service struct{auth,work,resourceAuth atomic.Int32;resourceToken string;owner *mcp.ResourceServer}`, 1)
	runtime = strings.Replace(runtime, `func(s *service)finish`, `func(s *service)OAuth2Auth(ctx context.Context,token string,scheme *security.OAuth2Scheme)(context.Context,error){
 s.resourceAuth.Add(1)
 if token!=s.resourceToken{return ctx,errors.New("original resource credential changed")}
 return s.owner.OAuth2Auth(ctx,token,scheme)
}
func(s *service)finish`, 1)
	runtime = strings.Replace(runtime, `TestGeneratedOAuthQueryCredentials`, `TestGeneratedJWTResourceOperations`, 1)
	runtime = strings.Replace(runtime, ` server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 server.Mount(mux)`, ` public,private,err:=ed25519.GenerateKey(rand.Reader);require.NoError(t,err)
 var resourceToken string`, 1)
	runtime = strings.Replace(runtime, `body="{\"access_token\":\"resource-token\",\"token_type\":\"Bearer\",\"expires_in\":3600}"`, `body=fmt.Sprintf("{\"access_token\":%q,\"token_type\":\"Bearer\",\"expires_in\":3600,\"scope\":\"catalog:read operation:read\"}",resourceToken)`, 1)
	runtime = strings.Replace(runtime, `assert.Equal(t,"Bearer resource-token",r.Header.Get("Authorization"))`, `assert.Equal(t,"Bearer "+resourceToken,r.Header.Get("Authorization"))`, 1)
	runtime = strings.Replace(runtime, ` origin=peer.URL`, ` origin=peer.URL
 signer,err:=jose.NewSigner(jose.SigningKey{Algorithm:jose.EdDSA,Key:private},(&jose.SignerOptions{}).WithType("at+jwt"));require.NoError(t,err)
 resourceToken,err=jwt.Signed(signer).Claims(jwt.Claims{Issuer:origin+"/issuer",Subject:"synthetic-subject",Audience:jwt.Audience{origin+"/mcp"},Expiry:jwt.NewNumericDate(time.Now().Add(time.Hour)),IssuedAt:jwt.NewNumericDate(time.Now()),ID:"synthetic-token"}).Claims(struct{ClientID string `+"`json:\"client_id\"`"+`;Scope string `+"`json:\"scope\"`"+`}{"registered","catalog:read operation:read"}).Serialize();require.NoError(t,err)
 s.resourceToken=resourceToken
 owner,err:=mcp.NewJWTResourceServer(mcp.JWTResource{Issuer:origin+"/issuer",Resource:origin+"/mcp",Keys:jose.JSONWebKeySet{Keys:[]jose.JSONWebKey{{Key:public}}},Algorithms:[]jose.SignatureAlgorithm{jose.EdDSA}});require.NoError(t,err)
 s.owner=owner
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil,owner)
 var middlewareCalls atomic.Int32
 server.Use(func(next http.Handler)http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){middlewareCalls.Add(1);next.ServeHTTP(w,r)})})
 server.Mount(mux)`, 1)
	runtime = strings.Replace(runtime, ` assert.EqualValues(t,14,s.work.Load())`, ` assert.EqualValues(t,14,s.work.Load())
 assert.EqualValues(t,14,s.resourceAuth.Load())
 assert.EqualValues(t,14,middlewareCalls.Load())
 // A missing or invalid token must stop before middleware and original auth.
 for _,token:=range []string{"","invalid-token"}{
  request:=httptest.NewRequest(http.MethodGet,origin+"/mcp",nil)
  if token!=""{request.Header.Set("Authorization","Bearer "+token)}
  response:=httptest.NewRecorder()
  server.ServeHTTP(response,request)
  assert.Equal(t,http.StatusUnauthorized,response.Code)
  assert.Contains(t,response.Header().Get("WWW-Authenticate"),"resource_metadata=")
 }
 assert.EqualValues(t,14,middlewareCalls.Load())
 assert.EqualValues(t,14,s.auth.Load())
 assert.EqualValues(t,14,s.resourceAuth.Load())
 assert.EqualValues(t,14,s.work.Load())`, 1)
	runtime = strings.Replace(runtime, `if ctx.Value(principalKey{})!=true{return errors.New("native domain principal missing")}`, `if ctx.Value(principalKey{})!=true{return errors.New("native domain principal missing")}
 principal,ok:=mcp.ResourcePrincipalFromContext(ctx)
 if !ok||principal.Issuer!=s.ownerIssuer||principal.Subject!="synthetic-subject"||principal.ClientID!="registered"{return errors.New("verified resource principal missing")}`, 1)
	runtime = strings.Replace(runtime, `owner *mcp.ResourceServer}`, `owner *mcp.ResourceServer;ownerIssuer string}`, 1)
	runtime = strings.Replace(runtime, ` s.owner=owner`, ` s.owner=owner;s.ownerIssuer=origin+"/issuer"`, 1)
	runtime = strings.Replace(runtime, ` assert.EqualValues(t,14,s.work.Load())
}`, ` assert.EqualValues(t,14,s.work.Load())
 // Catalog access never requires the union of every operation's permissions.
 catalogToken,err:=jwt.Signed(signer).Claims(jwt.Claims{Issuer:origin+"/issuer",Subject:"synthetic-subject",Audience:jwt.Audience{origin+"/mcp"},Expiry:jwt.NewNumericDate(time.Now().Add(time.Hour)),IssuedAt:jwt.NewNumericDate(time.Now()),ID:"catalog-token"}).Claims(struct{ClientID string `+"`json:\"client_id\"`"+`;Scope string `+"`json:\"scope\"`"+`}{"registered","catalog:read"}).Serialize();require.NoError(t,err)
 send:=func(method,name,token string)*httptest.ResponseRecorder{
  fields:=""
  if name!=""{fields=fmt.Sprintf("\"name\":%q,",name)}
  body:=fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":%q,\"params\":{%s\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":%q,\"io.modelcontextprotocol/clientCapabilities\":{}}}}",method,fields,mcp.ProtocolVersion)
  request:=httptest.NewRequest(http.MethodPost,origin+"/mcp?api_key=domain+key+%2B%2F",strings.NewReader(body))
  request.Header.Set("Content-Type","application/json");request.Header.Set("Accept","application/json, text/event-stream")
  request.Header.Set("MCP-Protocol-Version",mcp.ProtocolVersion);request.Header.Set("Mcp-Method",method)
  if name!=""{request.Header.Set("Mcp-Name",name)}
  if token!=""{request.Header.Set("Authorization","Bearer "+token)}
  response:=httptest.NewRecorder();server.ServeHTTP(response,request);return response
 }
 catalog:=send("tools/list","",catalogToken)
 require.Equal(t,http.StatusOK,catalog.Code,catalog.Body.String())
 assert.EqualValues(t,15,middlewareCalls.Load())
 for _,token:=range []string{"", "invalid-token",catalogToken}{
  denied:=send("tools/call","read",token)
  expected:=http.StatusUnauthorized
  if token==catalogToken{expected=http.StatusForbidden;assert.Contains(t,denied.Header().Get("WWW-Authenticate"),"catalog:read operation:read")}
  assert.Equal(t,expected,denied.Code,denied.Body.String())
 }
 assert.EqualValues(t,15,middlewareCalls.Load())
 assert.EqualValues(t,14,s.auth.Load());assert.EqualValues(t,14,s.resourceAuth.Load());assert.EqualValues(t,14,s.work.Load())
}`, 1)
	return design, runtime
}
