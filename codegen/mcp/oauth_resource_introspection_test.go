// These tests run opaque-token verification through the complete generated MCP
// client and server fixture. Native domain credentials, scopes and endpoint work
// must compose with the same resource guard used by signed access tokens.
package codegen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPIntrospectionResourceServerUsesNativeOperations(t *testing.T) {
	design, runtime := introspectionResourceFixture(t)
	runNativeCredentialPaths(t, design, runtime)
}

// introspectionResourceFixture reuses every signed-profile operation and denial
// check. Only issuer token production and the host's verifier construction change;
// no separate adapter or service implementation is introduced for opaque tokens.
func introspectionResourceFixture(t *testing.T) (string, string) {
	t.Helper()
	design, runtime := jwtResourceFixture()
	design = strings.Replace(design, "issuer-signed access tokens", "issuer-verified access tokens", 1)
	for _, imported := range []string{
		` "crypto/ed25519"`, ` "crypto/rand"`, ` "time"`,
		` "github.com/go-jose/go-jose/v4"`, ` "github.com/go-jose/go-jose/v4/jwt"`,
	} {
		runtime = strings.Replace(runtime, imported+"\n", "", 1)
	}
	runtime = strings.Replace(runtime,
		` public,private,err:=ed25519.GenerateKey(rand.Reader);require.NoError(t,err)
 var resourceToken string`,
		` resourceToken:="opaque.resource-token"
 var introspections atomic.Int32`, 1)
	runtime = strings.Replace(runtime, `  switch r.URL.Path{`, `  switch r.URL.Path{
  case "/issuer/introspect":
   introspections.Add(1)
   user,password,ok:=r.BasicAuth();assert.True(t,ok)
   assert.Equal(t,"resource-registration",user);assert.Equal(t,"introspection-secret",password)
   assert.Empty(t,r.URL.RawQuery)
   if err:=r.ParseForm();err!=nil{t.Error(err);return}
   assert.Len(t,r.PostForm,2);assert.Equal(t,"access_token",r.PostForm.Get("token_type_hint"))
   token:=r.PostForm.Get("token")
   scope:="catalog:read operation:read"
   if token=="opaque.catalog-token"{scope="catalog:read"}
   if token!=resourceToken&&token!="opaque.catalog-token"{body="{\"active\":false}"}else{
    body=fmt.Sprintf("{\"active\":true,\"aud\":%q,\"iss\":%q,\"sub\":\"synthetic-subject\",\"client_id\":\"registered\",\"scope\":%q}",origin+"/mcp",origin+"/issuer",scope)
   }`, 1)
	start := strings.Index(runtime, " signer,err:=jose.NewSigner")
	end := strings.Index(runtime, "\n s.owner=owner;")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	runtime = runtime[:start] + ` s.resourceToken=resourceToken
 owner,err:=mcp.NewIntrospectionResourceServer(mcp.IntrospectionResource{
  Issuer:origin+"/issuer",Resource:origin+"/mcp",Endpoint:origin+"/issuer/introspect",
  ClientID:"resource-registration",ClientSecret:"introspection-secret",Client:peer.Client(),
 });require.NoError(t,err)` + runtime[end:]
	start = strings.Index(runtime, " catalogToken,err:=jwt.Signed")
	end = strings.Index(runtime, "\n send:=func")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	runtime = runtime[:start] + ` catalogToken:="opaque.catalog-token"` + runtime[end:]
	runtime = strings.Replace(runtime,
		` assert.EqualValues(t,14,middlewareCalls.Load())
 // A missing`,
		` assert.EqualValues(t,14,middlewareCalls.Load())
 assert.EqualValues(t,28,introspections.Load())
 // A missing`, 1)
	runtime = strings.Replace(runtime,
		` assert.EqualValues(t,14,s.auth.Load());assert.EqualValues(t,14,s.resourceAuth.Load());assert.EqualValues(t,14,s.work.Load())`,
		` assert.EqualValues(t,14,s.auth.Load());assert.EqualValues(t,14,s.resourceAuth.Load());assert.EqualValues(t,14,s.work.Load())
 assert.EqualValues(t,32,introspections.Load())`, 1)
	runtime = strings.Replace(runtime, "TestGeneratedJWTResourceOperations", "TestGeneratedIntrospectionResourceOperations", 1)
	return design, runtime
}
