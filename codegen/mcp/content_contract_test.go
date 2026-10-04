// This file sends rich and malformed MCP replies through generated HTTP clients.
// The fixture compiles these tests in a separate module, so imported Go types,
// nested validators, transport decoding, and field preservation are all exercised.
package codegen

const contentContractGeneratedTestSource = `package client

import (
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "net/url"
 "reflect"
 "testing"
 genmcpfmt "generated.local/gen/mcp_fmt"
 genmcpprompts "generated.local/gen/mcp_prompts"
 genpromptclient "generated.local/gen/jsonrpc/mcp_prompts/client"
 goahttp "goa.design/goa/v3/http"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
)

func TestRichContentReplies(t *testing.T) {
 for _, test:=range []struct{name,content string; valid bool}{
  {"text", "{\"type\":\"text\",\"text\":\"hello\"}",true},
  {"empty text", "{\"type\":\"text\",\"text\":\"\"}",true},
  {"image", "{\"type\":\"image\",\"data\":\"AQI=\",\"mimeType\":\"image/png\"}",true},
  {"empty image", "{\"type\":\"image\",\"data\":\"\",\"mimeType\":\"image/png\"}",true},
  {"audio", "{\"type\":\"audio\",\"data\":\"AQI=\",\"mimeType\":\"audio/wav\"}",true},
  {"empty audio", "{\"type\":\"audio\",\"data\":\"\",\"mimeType\":\"audio/wav\"}",true},
  {"link", "{\"type\":\"resource_link\",\"name\":\"guide\",\"uri\":\"doc://guide\",\"title\":\"Guide\",\"description\":\"Read this\",\"mimeType\":\"text/plain\",\"size\":42.5,\"icons\":[{\"src\":\"data:image/png;base64,AQI=\",\"mimeType\":\"image/png\",\"sizes\":[\"any\"],\"theme\":\"dark\"}],\"_meta\":{\"example.org/source\":{\"id\":1}}}",true},
  {"embedded text", "{\"type\":\"resource\",\"resource\":{\"uri\":\"doc://inline\",\"text\":\"hello\",\"_meta\":{\"example.org/source\":true}}}",true},
  {"embedded blob", "{\"type\":\"resource\",\"resource\":{\"uri\":\"blob://inline\",\"blob\":\"AQI=\",\"mimeType\":\"image/png\"}}",true},
  {"empty embedded blob", "{\"type\":\"resource\",\"resource\":{\"uri\":\"blob://inline\",\"blob\":\"\"}}",true},
  {"annotations zero", "{\"type\":\"text\",\"text\":\"hello\",\"annotations\":{\"audience\":[\"user\",\"assistant\"],\"priority\":0,\"lastModified\":\"2026-10-03T00:00:00Z\"}}",true},
  {"annotations one", "{\"type\":\"text\",\"text\":\"hello\",\"annotations\":{\"priority\":1}}",true},
  {"missing text", "{\"type\":\"text\"}",false},
  {"null text", "{\"type\":\"text\",\"text\":null}",false},
  {"wrong text", "{\"type\":\"text\",\"text\":42}",false},
  {"missing image bytes", "{\"type\":\"image\",\"mimeType\":\"image/png\"}",false},
  {"missing image format", "{\"type\":\"image\",\"data\":\"AQI=\"}",false},
  {"invalid image bytes", "{\"type\":\"image\",\"data\":\"%%%\",\"mimeType\":\"image/png\"}",false},
  {"null audio bytes", "{\"type\":\"audio\",\"data\":null,\"mimeType\":\"audio/wav\"}",false},
  {"missing audio format", "{\"type\":\"audio\",\"data\":\"AQI=\"}",false},
  {"invalid audio bytes", "{\"type\":\"audio\",\"data\":\"%%%\",\"mimeType\":\"audio/wav\"}",false},
  {"missing link name", "{\"type\":\"resource_link\",\"uri\":\"doc://guide\"}",false},
  {"missing link uri", "{\"type\":\"resource_link\",\"name\":\"guide\"}",false},
  {"relative link uri", "{\"type\":\"resource_link\",\"name\":\"guide\",\"uri\":\"relative\"}",false},
  {"bad icon theme", "{\"type\":\"resource_link\",\"name\":\"guide\",\"uri\":\"doc://guide\",\"icons\":[{\"src\":\"https://example.org/icon\",\"theme\":\"invalid\"}]}",false},
  {"missing icon uri", "{\"type\":\"resource_link\",\"name\":\"guide\",\"uri\":\"doc://guide\",\"icons\":[{\"theme\":\"dark\"}]}",false},
  {"missing embedded resource", "{\"type\":\"resource\"}",false},
  {"embedded neither", "{\"type\":\"resource\",\"resource\":{\"uri\":\"doc://inline\"}}",false},
  {"embedded both", "{\"type\":\"resource\",\"resource\":{\"uri\":\"doc://inline\",\"text\":\"\",\"blob\":\"\"}}",false},
  {"embedded bad bytes", "{\"type\":\"resource\",\"resource\":{\"uri\":\"doc://inline\",\"blob\":\"%%%\"}}",false},
  {"embedded missing uri", "{\"type\":\"resource\",\"resource\":{\"blob\":\"AQI=\"}}",false},
  {"bad metadata", "{\"type\":\"text\",\"text\":\"hello\",\"_meta\":[]}",false},
  {"null metadata", "{\"type\":\"text\",\"text\":\"hello\",\"_meta\":null}",false},
  {"bad embedded metadata", "{\"type\":\"resource\",\"resource\":{\"uri\":\"doc://inline\",\"text\":\"hello\",\"_meta\":[]}}",false},
  {"invalid audience", "{\"type\":\"text\",\"text\":\"hello\",\"annotations\":{\"audience\":[\"system\"]}}",false},
  {"priority below zero", "{\"type\":\"text\",\"text\":\"hello\",\"annotations\":{\"priority\":-0.1}}",false},
  {"priority above one", "{\"type\":\"text\",\"text\":\"hello\",\"annotations\":{\"priority\":1.1}}",false},
  {"unknown kind", "{\"type\":\"other\"}",false},
 } {
  for _, method:=range []string{"tools/call","prompts/get"} {
   t.Run(method+"/"+test.name,func(t *testing.T){
    calls:=0
    server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
     calls++
     var request struct{ID json.RawMessage};if err:=json.NewDecoder(r.Body).Decode(&request);err!=nil{t.Fatal(err)}
     result:="\"content\":["+test.content+","+test.content+"]"
     if method=="prompts/get" {result="\"messages\":[{\"role\":\"assistant\",\"content\":"+test.content+"},{\"role\":\"user\",\"content\":"+test.content+"}]"}
     w.Header().Set("Content-Type","application/json")
     if _,err:=w.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":"+string(request.ID)+",\"result\":{\"resultType\":\"complete\","+result+"}}"));err!=nil{t.Error(err)}
    }));defer server.Close()
    location,err:=url.Parse(server.URL);if err!=nil{t.Fatal(err)}
    var content []any
    if method=="tools/call" {
     client:=NewClient(location.Scheme,location.Host,server.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
     var reply any
     reply,err=client.ToolsCall()(context.Background(),&genmcpfmt.ToolsCallPayload{Name:"echo"})
     if err==nil {
      for _, item := range reply.(*genmcpfmt.ToolsCallResult).Content {content=append(content,item)}
     }
    } else {
     client:=genpromptclient.NewClient(location.Scheme,location.Host,server.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
     var reply any
     reply,err=client.PromptsGet()(context.Background(),&genmcpprompts.PromptsGetPayload{Name:"daily_report"})
     if err==nil {
      messages:=reply.(*genmcpprompts.PromptsGetResult).Messages
      if len(messages)!=2 || messages[0].Role!="assistant" || messages[1].Role!="user" {t.Fatalf("lost message order: %+v",messages)}
      for _, message := range messages {content=append(content,message.Content)}
     }
    }
    if calls!=1 {t.Fatalf("requests=%d",calls)}
    if test.valid && err!=nil{t.Fatal(err)}
    if !test.valid && err==nil{t.Fatalf("accepted invalid reply: %+v",content)}
    if test.valid {
     if len(content)!=2 {t.Fatalf("lost items: %+v",content)}
     for _, item := range content {
      expected:=reflect.New(reflect.TypeOf(item).Elem()).Interface()
      if err:=json.Unmarshal([]byte(test.content),expected);err!=nil{t.Fatal(err)}
      if !reflect.DeepEqual(item,expected){t.Fatalf("lost content: got=%+v want=%+v",item,expected)}
     }
    }
   })
  }
 }
}

func TestGeneratedClientsRetainHTTPAuthorization(t *testing.T) {
 for _, status:=range []int{http.StatusBadRequest,http.StatusUnauthorized,http.StatusForbidden} {
  for _, typedCaller:=range []bool{false,true} {
   calls:=0
   challenge:="Bearer resource_metadata=\"https://example.test/metadata\", scope=\"records:write\""
   server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
    calls++
    w.Header().Set("WWW-Authenticate",challenge)
    w.WriteHeader(status)
   }))
   t.Cleanup(server.Close)
   location,err:=url.Parse(server.URL);if err!=nil{t.Fatal(err)}
   client:=NewClient(location.Scheme,location.Host,server.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
   if typedCaller {
    caller,createErr:=NewCaller(client,mcpruntime.ClientInfo{Name:"tests",Version:"1"},mcpruntime.InputSupport{},mcpruntime.HTTPRetryPolicy{MaxAttempts:3,TrustToolAnnotations:true})
    if createErr!=nil {t.Fatal(createErr)}
    _,err=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"echo",Payload:json.RawMessage("{}")})
   } else {
    _,err=client.ToolsCall()(t.Context(),&genmcpfmt.ToolsCallPayload{Name:"echo"})
   }
   server.Close()
   var failure *mcpruntime.HTTPResponseError
   if !errors.As(err,&failure) {t.Fatalf("authorization response lost: %v",err)}
   if failure.StatusCode!=status || !reflect.DeepEqual(failure.WWWAuthenticate,[]string{challenge}) {t.Fatalf("challenge changed: %+v",failure)}
   var unknown *mcpruntime.OutcomeUnknownError
   if errors.As(err,&unknown) {t.Fatal("rejected request reported unknown execution")}
   if calls!=1 {t.Fatalf("rejected request dispatched %d times",calls)}
  }
 }
}
`
