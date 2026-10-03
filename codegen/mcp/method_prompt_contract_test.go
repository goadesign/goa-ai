// Package codegen compiles a prompt service in a separate module and tests its
// generated HTTP endpoints. The service owns only typed arguments and content;
// the generated MCP adapter owns the wire shape and protocol validation.
package codegen

const methodPromptGeneratedTestSource = `package client

import (
 "context"
 "encoding/json"
 "errors"
 "bytes"
 "io"
 "math"
 "net/http"
 "net/http/httptest"
 "net/url"
 "reflect"
 "strings"
 "testing"
 genprompts "generated.local/gen/method_prompts"
 genmcpprompts "generated.local/gen/mcp_method_prompts"
 genserver "generated.local/gen/jsonrpc/mcp_method_prompts/server"
 goahttp "goa.design/goa/v3/http"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
)

type promptService struct {
 calls int
 code,style string
 result *genprompts.AuthoredPromptResult
 failure error
 completionCalls int
 completionPayload *genprompts.CompleteStylePayload
 suggestions *genprompts.AuthoredSuggestions
}
func (s *promptService) CompleteStyle(_ context.Context,p *genprompts.CompleteStylePayload)(*genprompts.AuthoredSuggestions,error) {
 s.completionCalls++;s.completionPayload=p
 return s.suggestions,s.failure
}
func (s *promptService) Review(_ context.Context,p *genprompts.ReviewPayload)(*genprompts.AuthoredPromptResult,error) {
 s.calls++;s.code=string(p.Code);s.style=p.Style
 return s.result,s.failure
}
func (s *promptService) Empty(context.Context)(*genprompts.AuthoredPromptResult,error) {
 s.calls++;return &genprompts.AuthoredPromptResult{},s.failure
}

// decodeAuthoredPrompt loads synthetic values using the authored Go field names.
// MCP's protocol names are asserted separately on the returned client values.
func decodeAuthoredPrompt(source string,result *genprompts.AuthoredPromptResult) error {
 source=strings.ReplaceAll(source,"\"role\":","\"author\":")
 source=strings.ReplaceAll(source,"\"content\":","\"selected\":")
 return json.Unmarshal([]byte(source),result)
}

func TestMethodBackedPrompts(t *testing.T) {
 service:=&promptService{}
 mux:=goahttp.NewMuxer()
 adapter:=genmcpprompts.NewMCPAdapter(service,nil)
 server:=genserver.New(genmcpprompts.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 endpoint:=httptest.NewServer(mux);defer endpoint.Close()
 location,err:=url.Parse(endpoint.URL);if err!=nil {t.Fatal(err)}
 client:=NewClient(location.Scheme,location.Host,endpoint.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 discovered,err:=client.ServerDiscover()(t.Context(),&genmcpprompts.DiscoverPayload{});if err!=nil{t.Fatal(err)}
 if discovered.(*genmcpprompts.DiscoverResult).Capabilities.Prompts==nil {t.Fatal("prompt capability missing")}
 listed,err:=client.PromptsList()(t.Context(),&genmcpprompts.PromptsListPayload{});if err!=nil{t.Fatal(err)}
 catalog:=listed.(*genmcpprompts.PromptsListResult)
 if len(catalog.Prompts)!=4||service.calls!=0 {t.Fatalf("catalog=%+v calls=%d",catalog,service.calls)}
 var review *genmcpprompts.PromptInfo
 for _,prompt:=range catalog.Prompts {if prompt.Name=="review"{review=prompt}}
 if review==nil||len(review.Arguments)!=2||review.Arguments[0].Name!="code"||review.Arguments[0].Required==nil||!*review.Arguments[0].Required||review.Arguments[1].Required==nil||*review.Arguments[1].Required {t.Fatalf("review=%+v",review)}

 for _,test:=range []struct{name,source,want string}{
  {"text", "{\"type\":\"text\",\"value\":{\"text\":\"Review this\",\"annotations\":{\"audience\":[\"user\",\"assistant\"],\"priority\":1},\"_meta\":{\"example.org/source\":true}}}","{\"type\":\"text\",\"text\":\"Review this\",\"annotations\":{\"audience\":[\"user\",\"assistant\"],\"priority\":1},\"_meta\":{\"example.org/source\":true}}"},
  {"empty text", "{\"type\":\"text\",\"value\":{\"text\":\"\"}}","{\"type\":\"text\",\"text\":\"\"}"},
  {"image", "{\"type\":\"image\",\"value\":{\"data\":\"AQI=\",\"mimeType\":\"image/png\"}}","{\"type\":\"image\",\"data\":\"AQI=\",\"mimeType\":\"image/png\"}"},
  {"empty image", "{\"type\":\"image\",\"value\":{\"mimeType\":\"image/png\"}}","{\"type\":\"image\",\"data\":\"\",\"mimeType\":\"image/png\"}"},
  {"audio", "{\"type\":\"audio\",\"value\":{\"data\":\"AQI=\",\"mimeType\":\"audio/wav\"}}","{\"type\":\"audio\",\"data\":\"AQI=\",\"mimeType\":\"audio/wav\"}"},
  {"link", "{\"type\":\"resource_link\",\"value\":{\"uri\":\"doc://guide\",\"name\":\"guide\",\"title\":\"Guide\",\"size\":42.5,\"icons\":[{\"src\":\"https://example.org/icon\",\"theme\":\"dark\",\"sizes\":[\"any\"]}]}}","{\"type\":\"resource_link\",\"uri\":\"doc://guide\",\"name\":\"guide\",\"title\":\"Guide\",\"size\":42.5,\"icons\":[{\"src\":\"https://example.org/icon\",\"theme\":\"dark\",\"sizes\":[\"any\"]}]}"},
  {"embedded text", "{\"type\":\"resource\",\"value\":{\"resource\":{\"type\":\"text\",\"value\":{\"uri\":\"doc://inline\",\"text\":\"Text\",\"_meta\":{\"example.org/source\":1}}}}}","{\"type\":\"resource\",\"resource\":{\"uri\":\"doc://inline\",\"text\":\"Text\",\"_meta\":{\"example.org/source\":1}}}"},
  {"embedded blob", "{\"type\":\"resource\",\"value\":{\"resource\":{\"type\":\"blob\",\"value\":{\"uri\":\"blob://inline\",\"blob\":\"AQI=\"}}}}","{\"type\":\"resource\",\"resource\":{\"uri\":\"blob://inline\",\"blob\":\"AQI=\"}}"},
  {"empty blob", "{\"type\":\"resource\",\"value\":{\"resource\":{\"type\":\"blob\",\"value\":{\"uri\":\"blob://inline\"}}}}","{\"type\":\"resource\",\"resource\":{\"uri\":\"blob://inline\",\"blob\":\"\"}}"},
 } {
  t.Run(test.name,func(t *testing.T){
   service.result=&genprompts.AuthoredPromptResult{}
   authored:="{\"description\":\"Review\",\"messages\":[{\"role\":\"user\",\"content\":"+test.source+"},{\"role\":\"assistant\",\"content\":{\"type\":\"text\",\"value\":{\"text\":\"Second message\",\"annotations\":{\"priority\":1}}}}]}"
   if err:=decodeAuthoredPrompt(authored,service.result);err!=nil{t.Fatal(err)}
   before:=service.calls
   got,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review",Arguments:map[string]string{"code":"x"}});if err!=nil{t.Fatal(err)}
   result:=got.(*genmcpprompts.PromptsGetResult)
   if service.calls!=before+1||service.code!="x"||service.style!="brief"||len(result.Messages)!=2||result.Description==nil||*result.Description!="Review"||result.Messages[0].Role!="user"||result.Messages[1].Role!="assistant" {t.Fatalf("result=%+v service=%+v",result,service)}
   var expected genmcpprompts.ContentItem
   if err:=json.Unmarshal([]byte(test.want),&expected);err!=nil{t.Fatal(err)}
   if !reflect.DeepEqual(result.Messages[0].Content,&expected){t.Fatalf("got=%+v want=%+v",result.Messages[0].Content,&expected)}
  })
 }
 service.result=&genprompts.AuthoredPromptResult{}
 copy,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review-copy",Arguments:map[string]string{"code":"copy"}})
 if err!=nil||copy==nil||service.code!="copy"{t.Fatalf("copy=%v err=%v service=%+v",copy,err,service)}
 explicit,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review",Arguments:map[string]string{"code":"x","style":"detailed"}})
 if err!=nil||explicit==nil||service.style!="detailed"{t.Fatalf("explicit=%v err=%v style=%s",explicit,err,service.style)}
 service.result=nil
 _,err=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review",Arguments:map[string]string{"code":"x"}})
 var missingResult *mcpruntime.Error
 if !errors.As(err,&missingResult)||missingResult.Code!=-32603{t.Fatalf("nil result err=%v",err)}
 for _,arguments:=range []map[string]string{nil,{"code":""},{"code":"x","unknown":"x"},{"code":"x","style":"invalid"}} {
  before:=service.calls
  _,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review",Arguments:arguments})
  if err==nil||service.calls!=before {t.Fatalf("arguments=%v err=%v calls=%d",arguments,err,service.calls)}
 }
 empty,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"empty"});if err!=nil{t.Fatal(err)}
 if len(empty.(*genmcpprompts.PromptsGetResult).Messages)!=0{t.Fatal("empty prompt gained messages")}
 fixed,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"fixed"});if err!=nil{t.Fatal(err)}
 if *fixed.(*genmcpprompts.PromptsGetResult).Messages[0].Content.Text!="Fixed instructions" {t.Fatal("static prompt changed")}
 for _,name:=range []string{"empty","fixed","missing"} {
  before:=service.calls
  _,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:name,Arguments:map[string]string{"x":"y"}})
  if err==nil||service.calls!=before{t.Fatalf("name=%s err=%v calls=%d",name,err,service.calls)}
 }
 for _,source:=range []string{
  "{\"messages\":[null]}",
  "{\"messages\":[{\"role\":\"system\",\"content\":{\"type\":\"text\",\"value\":{\"text\":\"x\"}}}]}",
  "{\"messages\":[{\"role\":\"user\"}]}",
  "{\"messages\":[{\"role\":\"user\",\"content\":{\"type\":\"text\",\"value\":{\"text\":\"x\",\"_meta\":[]}}}]}",
  "{\"messages\":[{\"role\":\"user\",\"content\":{\"type\":\"text\",\"value\":{\"text\":\"x\",\"annotations\":{\"priority\":1.1}}}}]}",
  "{\"messages\":[{\"role\":\"user\",\"content\":{\"type\":\"resource_link\",\"value\":{\"uri\":\"relative\",\"name\":\"x\"}}}]}",
 } {
  service.result=&genprompts.AuthoredPromptResult{}
  if err:=decodeAuthoredPrompt(source,service.result);err!=nil{t.Fatal(err)}
  _,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review",Arguments:map[string]string{"code":"x"}})
  var failure *mcpruntime.Error
  if !errors.As(err,&failure)||failure.Code!=-32603{t.Fatalf("invalid service result=%s err=%v",source,err)}
 }
 for _,number:=range []float64{math.NaN(),math.Inf(1),math.Inf(-1)} {
  for _,kind:=range []string{"size","priority"} {
   service.result=&genprompts.AuthoredPromptResult{}
   source:="{\"messages\":[{\"role\":\"user\",\"content\":{\"type\":\"resource_link\",\"value\":{\"uri\":\"doc://finite\",\"name\":\"finite\",\"size\":1}}}]}"
   if kind=="priority" {source="{\"messages\":[{\"role\":\"user\",\"content\":{\"type\":\"text\",\"value\":{\"text\":\"x\",\"annotations\":{\"priority\":1}}}}]}"}
   if err:=decodeAuthoredPrompt(source,service.result);err!=nil{t.Fatal(err)}
   if kind=="size" {link,_:=service.result.Messages[0].Selected.AsResourceLink();link.Size=&number} else {text,_:=service.result.Messages[0].Selected.AsText();text.Annotations.Priority=&number}
   _,err:=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review",Arguments:map[string]string{"code":"x"}})
   var failure *mcpruntime.Error
   if !errors.As(err,&failure)||failure.Code!=-32603{t.Fatalf("%s=%v err=%v",kind,number,err)}
  }
 }
 for _,arguments:=range []string{"null", "[]", "{\"code\":7}", "{\"code\":null}"} {
  name:="review";if arguments=="null"{name="empty"}
  body:=[]byte("{\"jsonrpc\":\"2.0\",\"id\":\"raw-prompt\",\"method\":\"prompts/get\",\"params\":{\"name\":\""+name+"\",\"arguments\":"+arguments+",\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"2026-07-28\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}")
  request,err:=http.NewRequestWithContext(t.Context(),"POST",endpoint.URL+"/method-prompts",bytes.NewReader(body));if err!=nil{t.Fatal(err)}
  request.Header.Set("Content-Type","application/json");request.Header.Set("MCP-Protocol-Version","2026-07-28");request.Header.Set("Mcp-Method","prompts/get");request.Header.Set("Mcp-Name",name)
  before:=service.calls
  response,err:=endpoint.Client().Do(request);if err!=nil{t.Fatal(err)}
  encoded,err:=io.ReadAll(response.Body);if err!=nil{t.Fatal(err)};if err:=response.Body.Close();err!=nil{t.Fatal(err)}
  var reply struct{Error *struct{Code int}};if err:=json.Unmarshal(encoded,&reply);err!=nil{t.Fatal(err)}
  if reply.Error==nil||reply.Error.Code!=-32602||service.calls!=before{t.Fatalf("arguments=%s reply=%s calls=%d",arguments,encoded,service.calls)}
 }
 service.failure=errors.New("operation failed")
 _,err=client.PromptsGet()(t.Context(),&genmcpprompts.PromptsGetPayload{Name:"review",Arguments:map[string]string{"code":"x","style":"detailed"}})
 if err==nil{t.Fatal("service error became a successful prompt")}
}

func TestPromptArgumentCompletion(t *testing.T) {
 service:=&promptService{suggestions:&genprompts.AuthoredSuggestions{Values:[]string{"detailed","brief"},Matches:new(int64(250)),HasMore:new(true)}}
 adapter:=genmcpprompts.NewMCPAdapter(service,nil)
 mux:=goahttp.NewMuxer()
 server:=genserver.New(genmcpprompts.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 endpoint:=httptest.NewServer(mux);defer endpoint.Close()
 location,err:=url.Parse(endpoint.URL);if err!=nil{t.Fatal(err)}
 client:=NewClient(location.Scheme,location.Host,endpoint.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 discovered,err:=client.ServerDiscover()(t.Context(),&genmcpprompts.DiscoverPayload{});if err!=nil{t.Fatal(err)}
 if discovered.(*genmcpprompts.DiscoverResult).Capabilities.Completions==nil{t.Fatal("missing completions capability")}
 request:=&genmcpprompts.CompletionCompletePayload{
  Ref:&genmcpprompts.CompletionReference{Type:"ref/prompt",Name:new("review")},
  Argument:&genmcpprompts.CompletionArgument{Name:"style",Value:"d"},
  Context:&genmcpprompts.CompletionContext{Arguments:map[string]string{"code":"source"}},
 }
 response,err:=client.CompletionComplete()(t.Context(),request);if err!=nil{t.Fatal(err)}
 suggestions:=response.(*genmcpprompts.CompletionCompleteResult).Completion
 if !reflect.DeepEqual(suggestions.Values,[]string{"detailed","brief"})||suggestions.Total==nil||*suggestions.Total!=250||suggestions.HasMore==nil||!*suggestions.HasMore{t.Fatalf("suggestions=%+v",suggestions)}
 if service.completionCalls!=1||string(service.completionPayload.Partial)!="d"||len(service.completionPayload.Prior)!=1{t.Fatalf("calls=%d payload=%+v",service.completionCalls,service.completionPayload)}
 for name,value:=range service.completionPayload.Prior {if string(name)!="code"||string(value)!="source"{t.Fatalf("context=%v",service.completionPayload.Prior)}}
 for _,count:=range []int{0,99,100,101} {
  service.suggestions=&genprompts.AuthoredSuggestions{Values:make([]string,count),Matches:new(int64(250))}
  for index:=range service.suggestions.Values{service.suggestions.Values[index]="match"}
  response,err:=client.CompletionComplete()(t.Context(),request)
  if count<=100 {
   if err!=nil{t.Fatalf("count=%d err=%v",count,err)}
   if len(response.(*genmcpprompts.CompletionCompleteResult).Completion.Values)!=count{t.Fatalf("count=%d response=%v",count,response)}
  } else {
   var failure *mcpruntime.Error
   if !errors.As(err,&failure)||failure.Code!=-32603{t.Fatalf("oversized suggestions=%v",err)}
  }
 }
 // A later response has a fresh array allowance; totals are not capped at 100.
 service.suggestions=&genprompts.AuthoredSuggestions{Values:make([]string,100),Matches:new(int64(1000))}
 if _,err:=client.CompletionComplete()(t.Context(),request);err!=nil{t.Fatal(err)}
 service.suggestions=nil
 _,err=client.CompletionComplete()(t.Context(),request)
 var failure *mcpruntime.Error
 if !errors.As(err,&failure)||failure.Code!=-32603{t.Fatalf("nil result=%v",err)}
 for _,test:=range []struct{name,argument,value string}{
  {"missing","style","d"},{"review","missing","d"},{"review","style",strings.Repeat("x",21)},
 } {
  request.Ref.Name=&test.name;request.Argument.Name=test.argument;request.Argument.Value=test.value
  before:=service.completionCalls
  _,err:=client.CompletionComplete()(t.Context(),request)
  var failure *mcpruntime.Error
  if !errors.As(err,&failure)||failure.Code!=-32602||service.completionCalls!=before{t.Fatalf("invalid selection=%+v err=%v calls=%d",test,err,service.completionCalls)}
 }
 request.Ref.Name=new("review");request.Argument.Name="code";request.Argument.Value=""
 response,err=client.CompletionComplete()(t.Context(),request);if err!=nil{t.Fatal(err)}
 if len(response.(*genmcpprompts.CompletionCompleteResult).Completion.Values)!=0{t.Fatal("unbound argument must have empty suggestions")}
}

func TestCompletionPeerValidation(t *testing.T) {
 for _,test:=range []struct{name,completion string;valid bool}{
  {"empty","{\"values\":[]}",true},
  {"empty string","{\"values\":[\"\"],\"total\":250,\"hasMore\":false}",true},
  {"null values","{\"values\":null}",false},
  {"null entry","{\"values\":[null]}",false},
  {"numeric entry","{\"values\":[7]}",false},
  {"missing values","{}",false},
  {"fractional total","{\"values\":[],\"total\":1.5}",false},
  {"too many values","{\"values\":["+strings.Repeat("\"x\",",100)+"\"x\"]}",false},
 } {
  t.Run(test.name,func(t *testing.T){
   calls:=0
   server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
    calls++
    var request struct{ID json.RawMessage}
    if err:=json.NewDecoder(r.Body).Decode(&request);err!=nil{t.Fatal(err)}
    w.Header().Set("Content-Type","application/json")
    if _,err:=w.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":"+string(request.ID)+",\"result\":{\"resultType\":\"complete\",\"completion\":"+test.completion+"}}"));err!=nil{t.Error(err)}
   }));defer server.Close()
   location,err:=url.Parse(server.URL);if err!=nil{t.Fatal(err)}
   client:=NewClient(location.Scheme,location.Host,server.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
   _,err=client.CompletionComplete()(t.Context(),&genmcpprompts.CompletionCompletePayload{Ref:&genmcpprompts.CompletionReference{Type:"ref/prompt",Name:new("review")},Argument:&genmcpprompts.CompletionArgument{Name:"style",Value:""}})
   if calls!=1 || (err==nil)!=test.valid{t.Fatalf("calls=%d valid=%v err=%v",calls,test.valid,err)}
  })
 }
}

func TestCompletionRawRequestValidation(t *testing.T) {
 service:=&promptService{suggestions:&genprompts.AuthoredSuggestions{}}
 adapter:=genmcpprompts.NewMCPAdapter(service,nil)
 mux:=goahttp.NewMuxer();server:=genserver.New(genmcpprompts.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil);genserver.Mount(mux,server)
 endpoint:=httptest.NewServer(mux);defer endpoint.Close()
 for _,context:=range []string{"null","[]","{\"arguments\":null}","{\"arguments\":{\"code\":null}}","{\"arguments\":{\"code\":5}}","{\"arguments\":{\"missing\":\"x\"}}"} {
  body:=[]byte("{\"jsonrpc\":\"2.0\",\"id\":\"raw-completion\",\"method\":\"completion/complete\",\"params\":{\"ref\":{\"type\":\"ref/prompt\",\"name\":\"review\"},\"argument\":{\"name\":\"style\",\"value\":\"\"},\"context\":"+context+",\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"2026-07-28\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}")
  request,err:=http.NewRequest(http.MethodPost,endpoint.URL+"/method-prompts",bytes.NewReader(body));if err!=nil{t.Fatal(err)}
  request.Header.Set("Content-Type","application/json");request.Header.Set("Accept","application/json, text/event-stream");request.Header.Set("MCP-Protocol-Version","2026-07-28");request.Header.Set("Mcp-Method","completion/complete")
  response,err:=endpoint.Client().Do(request);if err!=nil{t.Fatal(err)}
  data,err:=io.ReadAll(response.Body);if err!=nil{t.Fatal(err)};if err:=response.Body.Close();err!=nil{t.Fatal(err)}
  var reply struct{Error struct{Code int}}
  if err:=json.Unmarshal(data,&reply);err!=nil{t.Fatal(err)}
  if reply.Error.Code!=-32602||service.completionCalls!=0{t.Fatalf("context=%s reply=%s calls=%d",context,data,service.completionCalls)}
 }
}
`
