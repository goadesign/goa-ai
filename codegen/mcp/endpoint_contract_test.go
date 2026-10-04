// These tests compile a synthetic service and call its real MCP HTTP server.
// They verify that the configured Goa endpoints keep authentication, interceptors
// and middleware and that the adapter rejects an endpoint's wrong result type.
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

func TestMCPConfiguredEndpointContracts(t *testing.T) {
	dir := t.TempDir()
	module := fmt.Sprintf(`module endpoint-contract.local

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
		"go.mod":           module,
		"design/design.go": endpointContractDesign,
		"endpoint_test.go": endpointContractTest,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	generate := exec.CommandContext(ctx, "go", "run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", "endpoint-contract.local/design")
	generate.Dir = dir
	generate.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	output, err := generate.CombinedOutput()
	require.NoError(t, err, string(output))
	run := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-p=1", "./...")
	run.Dir = dir
	run.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	output, err = run.CombinedOutput()
	require.NoError(t, err, string(output))
}

const endpointContractDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var access=JWTSecurity("access",func(){
 Description("Verify the credential for this synthetic protected read")
 Scope("records:read","Read synthetic records")
})
var viewed=ResultType("application/vnd.endpoint.record",func(){
 TypeName("ViewedRecord")
 Description("A synthetic record whose default view omits a required detailed field")
 Field(1,"visible",String,"The value returned through the default view")
 Field(2,"hidden",String,"A required value available only in the detailed view")
 Required("visible","hidden")
 View("default",func(){Attribute("visible")})
 View("detailed",func(){Attribute("visible");Attribute("hidden")})
})
var nested=ResultType("application/vnd.endpoint.nested",func(){
 TypeName("NestedRecord")
 Description("Two occurrences of the same result type with different fixed views")
 Field(1,"summary",viewed,"The default record view",func(){View("default")})
 Field(2,"detail",viewed,"The detailed record view",func(){View("detailed")})
 Required("summary","detail")
 View("default",func(){Attribute("summary");Attribute("detail")})
})
var observe=Interceptor("observe")
var _=API("endpoint_contract",func(){Description("Verify original endpoint composition")})
var _=Service("secured",func(){
 Description("Expose synthetic endpoint composition contracts")
 MCP("endpoint-contract","1")
 ServerInterceptor(observe)
 JSONRPC(func(){POST("/mcp")})
 Method("read",func(){
  Description("Read a record with the declared credential and method scope")
  Security(access,func(){Scope("records:read")})
  Payload(func(){
   Token("access_token",String,"Credential checked by the Goa authentication function")
   Field(1,"key",String,"Exact domain key requested by the caller")
   Required("access_token","key")
  })
  Result(String)
  Tool("read","Read one synthetic protected record")
  JSONRPC(func(){})
 })
 Method("ping",func(){
  Description("Call a payload-free endpoint through its configured middleware")
  Result(String)
  Tool("ping","Return the synthetic endpoint status")
  JSONRPC(func(){})
 })
 Method("notify",func(){
  Description("Call a method without a result through its configured middleware")
  Payload(func(){Field(1,"key",String,"Exact domain key to record");Required("key")})
  Tool("notify","Record one synthetic domain key")
  JSONRPC(func(){})
 })
 Method("viewed",func(){
  Description("Return only the fields selected by the service view")
  Result(viewed,func(){View("default")})
  Tool("viewed","Return a synthetic record through its selected view")
  JSONRPC(func(){})
 })
 Method("detailed",func(){
  Description("Return both required fields through the fixed detailed view")
  Result(viewed,func(){View("detailed")})
  Tool("detailed","Return the synthetic detailed record")
  JSONRPC(func(){})
 })
 Method("nested",func(){
  Description("Return independent nested views of the same record type")
  Result(nested)
  Tool("nested","Return a record summary and details")
  JSONRPC(func(){})
 })
 Method("echo",func(){
  Description("Preserve a domain field whose spelling resembles a credential")
  Payload(func(){Field(1,"token",String,"A domain token with no security annotation");Required("token")})
  Result(String)
  Tool("echo","Return the supplied domain token")
  JSONRPC(func(){})
 })
})
`

const endpointContractTest = `package endpointcontract_test
import (
 "context"
 "encoding/json"
 "errors"
 "net/http/httptest"
 "sync/atomic"
 "testing"
 genservice "endpoint-contract.local/gen/secured"
 genmcp "endpoint-contract.local/gen/mcp_secured"
 genserver "endpoint-contract.local/gen/jsonrpc/mcp_secured/server"
 genspecs "endpoint-contract.local/gen/secured/toolsets/endpoint_contract"
 "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
)
type authKey struct{}
type middlewareKey struct{}
type service struct {checks,reads,pings,notices,echoes atomic.Int64}
func(s *service)JWTAuth(ctx context.Context,token string,scheme *security.JWTScheme)(context.Context,error){
 s.checks.Add(1)
 if len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="records:read"{return ctx,errors.New("declared method scope missing")}
 if token!="authorized"{return ctx,errors.New("credential rejected")}
 return context.WithValue(ctx,authKey{},"principal"),nil
}
func(s *service)Read(ctx context.Context,p *genservice.ReadPayload)(string,error){
 s.reads.Add(1)
 if ctx.Value(authKey{})!="principal"||ctx.Value(middlewareKey{})!="configured"{return "",errors.New("endpoint context lost")}
 return p.Key,nil
}
func(s *service)Ping(ctx context.Context)(string,error){s.pings.Add(1);if ctx.Value(middlewareKey{})!="configured"{return "",errors.New("middleware missing")};return "ready",nil}
func(s *service)Notify(ctx context.Context,p *genservice.NotifyPayload)error{s.notices.Add(1);if ctx.Value(middlewareKey{})!="configured"||p.Key!="record"{return errors.New("domain input or middleware lost")};return nil}
func(s *service)Viewed(ctx context.Context)(*genservice.ViewedRecord,error){if ctx.Value(middlewareKey{})!="configured"{return nil,errors.New("middleware missing")};return &genservice.ViewedRecord{Visible:"shown",Hidden:"must-not-leak"},nil}
func(s *service)Detailed(ctx context.Context)(*genservice.ViewedRecord,error){if ctx.Value(middlewareKey{})!="configured"{return nil,errors.New("middleware missing")};return &genservice.ViewedRecord{Visible:"shown",Hidden:"detail"},nil}
func(s *service)Echo(ctx context.Context,p *genservice.EchoPayload)(string,error){s.echoes.Add(1);if ctx.Value(middlewareKey{})!="configured"{return "",errors.New("middleware missing")};return p.Token,nil}
func(s *service)Nested(context.Context)(*genservice.NestedRecord,error){
 return &genservice.NestedRecord{
  Summary:&genservice.ViewedRecord{Visible:"summary",Hidden:"must-not-leak"},
  Detail:&genservice.ViewedRecord{Visible:"detail",Hidden:"retained"},
 },nil
}
type interceptors struct {calls atomic.Int64}
func(i *interceptors)Observe(ctx context.Context,info genservice.ObserveInfo,next goa.Endpoint)(any,error){i.calls.Add(1);return next(ctx,info.RawPayload())}
func TestConfiguredEndpoints(t *testing.T){
 s:=&service{}
 i:=&interceptors{}
 endpoints:=genservice.NewEndpoints(s,i)
 var middlewareCalls atomic.Int64
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,input any)(any,error){middlewareCalls.Add(1);if ctx.Value(goa.ServiceKey)!="secured"{return nil,errors.New("original service identity lost")};method,ok:=ctx.Value(goa.MethodKey).(string);if !ok{ return nil,errors.New("original method identity missing")};switch method{case "read","ping","notify","echo","viewed","detailed","nested":default:return nil,errors.New("original method identity lost")};return next(context.WithValue(ctx,middlewareKey{},"configured"),input)}})
 mux:=goahttp.NewMuxer()
 var mapped atomic.Int64
 adapter:=genmcp.NewMCPAdapter(endpoints,&genmcp.MCPAdapterOptions{ErrorMapper:func(err error)error{mapped.Add(1);return err}})
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 httpServer:=httptest.NewServer(mux);defer httpServer.Close()
 caller:=mcp.NewHTTPTransport(httpServer.Client(),mcp.ClientInfo{Name:"endpoint-contract",Version:"1"},nil,mcp.InputSupport{},mcp.HTTPRetryPolicy{})
 for _,test:=range []struct{name,args,want string;failure bool}{
  {"read","{\"access_token\":\"rejected\",\"key\":\"record\"}","",true},
  {"read","{\"access_token\":\"authorized\",\"key\":\"record\"}","\"record\"",false},
  {"ping","{}","\"ready\"",false},
  {"notify","{\"key\":\"record\"}","",false},
  {"echo","{\"token\":\"domain-value\"}","\"domain-value\"",false},
  {"viewed","{}","{\"visible\":\"shown\"}",false},
 {"detailed","{}","{\"visible\":\"shown\",\"hidden\":\"detail\"}",false},
 {"nested","{}","{\"summary\":{\"visible\":\"summary\"},\"detail\":{\"visible\":\"detail\",\"hidden\":\"retained\"}}",false},
 }{
  result,err:=caller.CallTool(t.Context(),httpServer.URL+"/mcp",mcp.CallRequest{Tool:test.name,Payload:json.RawMessage(test.args)})
  var failure *mcp.ToolExecutionError
  failed:=errors.As(err,&failure)
  if failed!=test.failure||!test.failure&&err!=nil||string(result.StructuredContent)!=test.want{t.Fatalf("%s result=%+v err=%v",test.name,result,err)}
 }
 if s.checks.Load()!=2||s.reads.Load()!=1||s.pings.Load()!=1||s.notices.Load()!=1||s.echoes.Load()!=1||i.calls.Load()!=8||middlewareCalls.Load()!=8{t.Fatalf("checks=%d reads=%d pings=%d notices=%d echoes=%d interceptors=%d middleware=%d",s.checks.Load(),s.reads.Load(),s.pings.Load(),s.notices.Load(),s.echoes.Load(),i.calls.Load(),middlewareCalls.Load())}
 // The generated agent decoder uses the same required fields as each selected view.
 foundViews := 0
 for _, spec := range genspecs.Specs() {
  var valid string
  var invalid []string
  switch spec.Name {
  case "endpoint-contract.viewed":
   valid = "{\"visible\":\"shown\"}"
   invalid = []string{"{}", "{\"hidden\":\"detail\"}", "{\"visible\":\"shown\",\"hidden\":\"must-not-leak\"}"}
  case "endpoint-contract.detailed":
   valid = "{\"visible\":\"shown\",\"hidden\":\"detail\"}"
   invalid = []string{"{}", "{\"visible\":\"shown\"}", "{\"hidden\":\"detail\"}"}
  case "endpoint-contract.nested":
   valid = "{\"summary\":{\"visible\":\"summary\"},\"detail\":{\"visible\":\"detail\",\"hidden\":\"retained\"}}"
   invalid = []string{
    "{\"summary\":{\"visible\":\"summary\"},\"detail\":{\"visible\":\"detail\"}}",
    "{\"summary\":{\"visible\":\"summary\",\"hidden\":\"must-not-leak\"},\"detail\":{\"visible\":\"detail\",\"hidden\":\"retained\"}}",
   }
  default:
   continue
  }
  foundViews++
  value,err:=spec.Result.Codec.FromJSON([]byte(valid))
  if err!=nil{t.Fatalf("%s decode selected view: %v",spec.Name,err)}
  encoded,err:=spec.Result.Codec.ToJSON(value)
  if err!=nil||string(encoded)!=valid{t.Fatalf("%s encode selected view=%s err=%v",spec.Name,encoded,err)}
  for _,input:=range invalid{
   if _,err:=spec.Result.Codec.FromJSON([]byte(input));err==nil{t.Fatalf("%s accepted invalid selected view: %s",spec.Name,input)}
  }
 }
 if foundViews!=3 {t.Fatalf("generated fixed-view result specs: %d",foundViews)}
 // Invalid arguments must not enter endpoint middleware or authentication.
 result,err:=caller.CallTool(t.Context(),httpServer.URL+"/mcp",mcp.CallRequest{Tool:"read",Payload:json.RawMessage("{\"access_token\":\"authorized\"}")})
 var invalid *mcp.ToolExecutionError
 if !errors.As(err,&invalid)||s.checks.Load()!=2||middlewareCalls.Load()!=8{t.Fatalf("invalid arguments result=%+v err=%v",result,err)}
 // The direct client validates both responses against the server's advertised schemas.
 direct,err:=mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint:httpServer.URL+"/mcp",Client:httpServer.Client(),ClientInfo:mcp.ClientInfo{Name:"selected-view-client",Version:"1"}})
 if err!=nil{t.Fatal(err)}
 for _,name:=range []string{"viewed","detailed","nested"}{
  if _,err:=direct.CallTool(t.Context(),mcp.CallRequest{Tool:name,Payload:json.RawMessage("{}")});err!=nil{t.Fatalf("%s advertised result contract: %v",name,err)}
 }
 // A custom endpoint's wrong Go type must fail before result conversion.
 endpoints.Ping=func(context.Context,any)(any,error){return 42,nil}
 _,err=caller.CallTool(t.Context(),httpServer.URL+"/mcp",mcp.CallRequest{Tool:"ping",Payload:json.RawMessage("{}")})
 var protocolError *mcp.Error
 if !errors.As(err,&protocolError)||protocolError.Code!=mcp.JSONRPCInternalError||mapped.Load()!=1{t.Fatalf("wrong endpoint result err=%v mapper calls=%d",err,mapped.Load())}
}
`
