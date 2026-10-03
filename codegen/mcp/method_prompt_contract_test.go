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
`
