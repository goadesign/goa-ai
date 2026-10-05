// These tests generate and compile a secured resource subscription service.
// Real HTTP calls verify typed stream values, endpoint composition, ordering,
// URI selection and cancellation without editing generated code.
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

func TestMCPResourceSubscriptionContracts(t *testing.T) {
	dir := t.TempDir()
	module := fmt.Sprintf(`module subscription-contract.local

go 1.26.0
require (
 goa.design/goa-ai v0.0.0
 goa.design/goa/v3 v3.0.0
)
replace goa.design/goa-ai => %s
replace goa.design/goa/v3 => %s
`, filepath.ToSlash(testModuleDirectory(t, "goa.design/goa-ai")), filepath.ToSlash(testModuleDirectory(t, "goa.design/goa/v3")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "design"), 0o700))
	for name, source := range map[string]string{"go.mod": module, "design/design.go": resourceSubscriptionContractDesign, "subscription_test.go": resourceSubscriptionContractTest} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	generate := exec.CommandContext(ctx, "go", "run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", "subscription-contract.local/design")
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

const resourceSubscriptionContractDesign = `package design
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var access=JWTSecurity("access",func(){Description("Verify resource access");Scope("records:watch","Observe allowed resources")})
var uri=Type("ResourceURI",String,func(){Format(FormatURI);Meta("struct:pkg:path","resource/shared")})
var acknowledged=Type("AcceptedResources",func(){
 Description("The resource URIs authorized for one listen request")
 Field(1,"resources",ArrayOf(uri),"The accepted subset of requested URIs",func(){Meta("struct:field:name","Addresses")})
})
var updated=Type("ResourceUpdate",func(){
 Description("One changed resource or authorized sub-resource")
 Field(1,"uri",uri,"The changed resource address",func(){Meta("struct:field:name","Address")})
 Required("uri")
})
var event=Type("ResourceEvent",func(){
 Description("An acknowledgment or change from the resource source")
 OneOf("change","The source event",func(){Attribute("acknowledged",acknowledged,"Authorized URI selection");Attribute("updated",updated,"Changed resource URI")})
 Required("change")
})
var observe=Interceptor("observe")
var _=API("subscription_contract",func(){Description("Verify resource subscription composition")})
var _=Service("records",func(){
 Description("Expose synthetic resources and their owned changes")
 MCP("records","1")
 ServerInterceptor(observe)
 JSONRPC(func(){POST("/mcp")})
 Method("read",func(){Description("Read the fixed synthetic resource");Result(String);Resource("record","test://records/one","text/plain")})
 Method("watch",func(){
  Description("Authorize requested resource URIs and observe their changes")
  Security(access,func(){Scope("records:watch")})
  Payload(func(){
   Token("credential",String,"The caller credential")
   Field(1,"resources",ArrayOf(uri),"Resource URI selections",func(){Meta("struct:field:name","Addresses")})
   Required("credential")
  })
  StreamingResult(event)
  ResourceSubscription()
 })
})
`

const resourceSubscriptionContractTest = `package subscriptioncontract_test
import (
 "bytes"
 "context"
 "errors"
 "io"
 "net/http"
 "net/http/httptest"
 "net/url"
 "sync/atomic"
 "testing"

 genrecords "subscription-contract.local/gen/records"
 genshared "subscription-contract.local/gen/resource/shared"
 genmcp "subscription-contract.local/gen/mcp_records"
 genserver "subscription-contract.local/gen/jsonrpc/mcp_records/server"
 genclient "subscription-contract.local/gen/jsonrpc/mcp_records/client"
 "goa.design/goa-ai/runtime/mcp"
 goahttp "goa.design/goa/v3/http"
 goa "goa.design/goa/v3/pkg"
 "goa.design/goa/v3/security"
)

type principalKey struct{}
type middlewareKey struct{}
type httpKey struct{}
type interceptors struct{calls atomic.Int64}
func(i *interceptors)Observe(ctx context.Context,info genrecords.ObserveInfo,next goa.Endpoint)(any,error){i.calls.Add(1);return next(ctx,info.RawPayload())}
type sourceService struct{mode string;auth,calls atomic.Int64; stopped chan struct{}}
func(s *sourceService)JWTAuth(ctx context.Context,token string,scheme *security.JWTScheme)(context.Context,error){
 s.auth.Add(1)
 if len(scheme.RequiredScopes)!=1||scheme.RequiredScopes[0]!="records:watch"{return ctx,errors.New("scope missing")}
 if token!="allowed"{return ctx,errors.New("credential rejected")}
 return context.WithValue(ctx,principalKey{},true),nil
}
func(s *sourceService)Read(context.Context)(string,error){return "record",nil}
func(s *sourceService)Watch(ctx context.Context,p *genrecords.WatchPayload,stream genrecords.WatchServerStream)error{
 s.calls.Add(1)
 if ctx.Value(principalKey{})!=true||ctx.Value(middlewareKey{})!=true||ctx.Value(httpKey{})!=true{return errors.New("endpoint context lost")}
 if len(p.Addresses)>1{return errors.New("unexpected URI selection")}
 if s.mode=="before_ack"{return stream.Send(&genrecords.ResourceEvent{Change:genrecords.NewChangeUpdated(&genrecords.ResourceUpdate{Address:genshared.ResourceURI("test://records/one")})})}
 if s.mode=="invalid_uri"{return stream.Send(&genrecords.ResourceEvent{Change:genrecords.NewChangeUpdated(&genrecords.ResourceUpdate{Address:genshared.ResourceURI("invalid")})})}
 if s.mode=="missing_ack"{return nil}
 accepted:=p.Addresses
 if s.mode=="unrequested"{accepted=[]genshared.ResourceURI{"test://other"}}
 if s.mode=="empty"{accepted=nil}
 ack:=&genrecords.ResourceEvent{Change:genrecords.NewChangeAcknowledged(&genrecords.AcceptedResources{Addresses:accepted})}
 if err:=stream.Send(ack);err!=nil{return err}
 if s.mode=="duplicate"{return stream.Send(ack)}
 if s.mode=="cancel"{<-ctx.Done();close(s.stopped);return ctx.Err()}
 if s.mode=="empty"{return nil}
 if err:=stream.SendWithContext(context.Background(),&genrecords.ResourceEvent{Change:genrecords.NewChangeUpdated(&genrecords.ResourceUpdate{Address:genshared.ResourceURI(string(p.Addresses[0])+"/child")})});err!=nil{return err}
 if err:=stream.Close();err!=nil{return err}
 return nil
}

type credentialTransport struct{base http.RoundTripper; credential string}
func(c credentialTransport)RoundTrip(r *http.Request)(*http.Response,error){r.Header.Set("Authorization","Bearer "+c.credential);return c.base.RoundTrip(r)}

func sourceServer(t *testing.T,mode string)(*sourceService,*httptest.Server,*atomic.Int64){
 t.Helper()
 service:=&sourceService{mode:mode,stopped:make(chan struct{})}
 observed:=&interceptors{}
 endpoints:=genrecords.NewEndpoints(service,observed)
 var middleware atomic.Int64
 endpoints.Use(func(next goa.Endpoint)goa.Endpoint{return func(ctx context.Context,input any)(any,error){
  if ctx.Value(goa.ServiceKey)!="records"||ctx.Value(goa.MethodKey)!="watch"{return nil,errors.New("authored endpoint identity lost")}
  middleware.Add(1)
  return next(context.WithValue(ctx,middlewareKey{},true),input)
 }})
 mux:=goahttp.NewMuxer()
 adapter:=genmcp.NewMCPAdapter(endpoints,nil)
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,func(ctx context.Context,w http.ResponseWriter,err error){
  if ctx.Err()!=nil{return}
  http.Error(w,"transport failure",http.StatusInternalServerError)
 })
 genserver.Mount(mux,server)
 // Middleware installed after mounting must still run before source dispatch.
 server.Use(func(next http.Handler)http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("X-Source-HTTP","configured")
  next.ServeHTTP(w,r.WithContext(context.WithValue(r.Context(),httpKey{},true)))
 })})
 t.Cleanup(func(){if observed.calls.Load()!=service.auth.Load(){t.Errorf("interceptors=%d authentication calls=%d",observed.calls.Load(),service.auth.Load())}})
 endpoint:=httptest.NewServer(mux)
 t.Cleanup(endpoint.Close)
 return service,endpoint,&middleware
}

func listener(t *testing.T,server *httptest.Server,credential string)*mcp.HTTPCaller{
 t.Helper()
 client:=&http.Client{Transport:credentialTransport{base:server.Client().Transport,credential:credential}}
 caller,err:=mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint:server.URL+"/mcp",Client:client,ClientInfo:mcp.ClientInfo{Name:"subscription-contract",Version:"1"}})
 if err!=nil{t.Fatal(err)}
 return caller
}

func TestSubscriptionSource(t *testing.T){
 service,server,middleware:=sourceServer(t,"valid")
 caller:=listener(t,server,"allowed")
 filter:=mcp.SubscriptionFilter{ToolsListChanged:true,PromptsListChanged:true,ResourcesListChanged:true,ResourceSubscriptions:[]string{"test://records/one"}}
 var events []mcp.SubscriptionEvent
 err:=caller.Listen(t.Context(),filter,func(_ context.Context,event mcp.SubscriptionEvent)error{events=append(events,event);return nil})
 if err!=nil{t.Fatal(err)}
 if len(events)!=2||events[0].Kind!=mcp.SubscriptionAcknowledged||events[1].Kind!=mcp.SubscriptionResourceUpdated{t.Fatalf("events=%+v",events)}
 if events[0].Accepted.ToolsListChanged||events[0].Accepted.PromptsListChanged||events[0].Accepted.ResourcesListChanged||len(events[0].Accepted.ResourceSubscriptions)!=1{t.Fatalf("invented catalog source: %+v",events[0])}
 if events[1].URI!="test://records/one/child"||string(events[0].RequestID)!=string(events[1].RequestID){t.Fatalf("update=%+v",events[1])}
 if service.auth.Load()!=1||service.calls.Load()!=1||middleware.Load()!=1{t.Fatalf("auth=%d calls=%d middleware=%d",service.auth.Load(),service.calls.Load(),middleware.Load())}
}

func TestSubscriptionBoundary(t *testing.T){
 for _,test:=range []struct{mode,credential string;events int;serviceCalls int64;wantError bool}{
  {"valid","denied",0,0,true},
  {"empty","allowed",1,1,false},
  {"before_ack","allowed",0,1,true},
  {"invalid_uri","allowed",0,1,true},
  {"unrequested","allowed",0,1,true},
  {"duplicate","allowed",1,1,true},
  {"missing_ack","allowed",0,1,true},
 }{
  t.Run(test.mode+"/"+test.credential,func(t *testing.T){
   service,server,middleware:=sourceServer(t,test.mode)
   caller:=listener(t,server,test.credential)
   events:=0
   err:=caller.Listen(t.Context(),mcp.SubscriptionFilter{ResourceSubscriptions:[]string{"test://records/one"}},func(context.Context,mcp.SubscriptionEvent)error{events++;return nil})
   if (err!=nil)!=test.wantError||events!=test.events||service.calls.Load()!=test.serviceCalls||service.auth.Load()!=1||middleware.Load()!=1{t.Fatalf("err=%v events=%d calls=%d auth=%d middleware=%d",err,events,service.calls.Load(),service.auth.Load(),middleware.Load())}
  })
 }
}

func TestSubscriptionCancellation(t *testing.T){
 service,server,_:=sourceServer(t,"cancel")
 caller:=listener(t,server,"allowed")
 ctx,cancel:=context.WithCancel(t.Context())
 defer cancel()
 err:=caller.Listen(ctx,mcp.SubscriptionFilter{ResourceSubscriptions:[]string{"test://records/one"}},func(_ context.Context,event mcp.SubscriptionEvent)error{if event.Kind==mcp.SubscriptionAcknowledged{cancel()};return nil})
 if !errors.Is(err,context.Canceled){t.Fatalf("cancellation=%v",err)}
 select{case <-service.stopped:case <-t.Context().Done():t.Fatal("source did not stop")}
}

func TestGeneratedSubscriptionClient(t *testing.T){
 for _,mode:=range []string{"valid","cancel","callback_error","missing_handler"}{
  t.Run(mode,func(t *testing.T){
   sourceMode:=mode
   if mode=="callback_error"||mode=="missing_handler"{sourceMode="valid"}
   service,server,_:=sourceServer(t,sourceMode)
   location,err:=url.Parse(server.URL);if err!=nil{t.Fatal(err)}
   httpClient:=&http.Client{Transport:credentialTransport{base:server.Client().Transport,credential:"allowed"}}
   client:=genclient.NewClient(location.Scheme,location.Host,httpClient,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
   ctx,cancel:=context.WithCancel(t.Context());defer cancel()
   failure:=errors.New("host cannot handle this update")
   var events []mcp.SubscriptionEvent
   if mode!="missing_handler"{
    ctx=mcp.WithSubscriptionEvents(ctx,func(_ context.Context,event mcp.SubscriptionEvent)error{
     events=append(events,event)
     if mode=="cancel"{cancel()}
     if mode=="callback_error"{return failure}
     return nil
    })
   }
   result,err:=client.SubscriptionsListen()(ctx,&genmcp.SubscriptionsListenPayload{Notifications:&genmcp.SubscriptionFilter{ResourceSubscriptions:[]string{"test://records/one"}}})
   switch mode{
   case "valid":
    if err!=nil||result==nil||len(events)!=2||events[1].URI!="test://records/one/child"{t.Fatalf("result=%+v events=%+v err=%v",result,events,err)}
   case "cancel":
    if !errors.Is(err,context.Canceled){t.Fatalf("cancellation=%v",err)}
    select{case <-service.stopped:case <-t.Context().Done():t.Fatal("source did not stop")}
   case "callback_error":
    if !errors.Is(err,failure)||len(events)!=1{t.Fatalf("events=%+v err=%v",events,err)}
   case "missing_handler":
    if err==nil||service.auth.Load()!=0||service.calls.Load()!=0{t.Fatalf("unobserved listen dispatched: err=%v auth=%d calls=%d",err,service.auth.Load(),service.calls.Load())}
   }
  })
 }
}

func TestSubscriptionDiscovery(t *testing.T){
 service,server,_:=sourceServer(t,"valid")
 location,err:=url.Parse(server.URL);if err!=nil{t.Fatal(err)}
 client:=genclient.NewClient(location.Scheme,location.Host,server.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 response,err:=client.ServerDiscover()(t.Context(),&genmcp.DiscoverPayload{});if err!=nil{t.Fatal(err)}
 capabilities:=response.(*genmcp.DiscoverResult).Capabilities
 if capabilities.Resources==nil||capabilities.Resources.Subscribe==nil||!*capabilities.Resources.Subscribe{t.Fatalf("resource subscription not advertised: %+v",capabilities)}
 if service.calls.Load()!=0||service.auth.Load()!=0{t.Fatal("discovery invoked the source")}
}
func TestMalformedSubscriptionInput(t *testing.T){
 for _,notifications:=range []string{
  "{\"resourceSubscriptions\":[\"invalid\"]}",
  "{\"resourceSubscriptions\":[null]}",
  "{\"resourceSubscriptions\":[\"test://records/one\"],\"credential\":\"injected\"}",
 }{
  t.Run(notifications,func(t *testing.T){
   service,server,middleware:=sourceServer(t,"valid")
   body:="{\"jsonrpc\":\"2.0\",\"id\":9007199254740993,\"method\":\"subscriptions/listen\",\"params\":{\"notifications\":"+notifications+",\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\""+mcp.ProtocolVersion+"\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}"
   request,err:=http.NewRequestWithContext(t.Context(),http.MethodPost,server.URL+"/mcp",bytes.NewBufferString(body));if err!=nil{t.Fatal(err)}
   request.Header.Set("Content-Type","application/json")
   request.Header.Set("Accept","application/json, text/event-stream")
   request.Header.Set("MCP-Protocol-Version",mcp.ProtocolVersion)
   request.Header.Set("Mcp-Method","subscriptions/listen")
   request.Header.Set("Authorization","Bearer allowed")
   response,err:=server.Client().Do(request);if err!=nil{t.Fatal(err)}
   result,readErr:=io.ReadAll(response.Body)
   closeErr:=response.Body.Close();if err:=errors.Join(readErr,closeErr);err!=nil{t.Fatal(err)}
   if response.StatusCode!=http.StatusBadRequest||service.auth.Load()!=0||service.calls.Load()!=0||middleware.Load()!=0{t.Fatalf("status=%d auth=%d calls=%d middleware=%d reply=%s",response.StatusCode,service.auth.Load(),service.calls.Load(),middleware.Load(),result)}
   if response.Header.Get("Content-Type")=="text/event-stream"{t.Fatal("invalid input opened a subscription")}
  })
 }
}

`
