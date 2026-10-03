// This file exercises generated resource reply validation at the HTTP boundary,
// including empty content and replies that violate MCP's flat text-or-blob rule.
package codegen

const resourceContentGeneratedTestSource = `package client

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "net/url"
 "testing"
 genmcpresources "generated.local/gen/mcp_resources"
 goahttp "goa.design/goa/v3/http"
)

func TestResourceContentBoundary(t *testing.T) {
 for _, test := range []struct{name,content string; valid bool}{
  {"text", "\"text\":\"hello\"",true},
  {"empty text", "\"text\":\"\"",true},
  {"blob", "\"blob\":\"AQI=\"",true},
  {"empty blob", "\"blob\":\"\"",true},
  {"invalid base64", "\"blob\":\"%%%\"",false},
  {"both", "\"text\":\"\",\"blob\":\"\"",false},
  {"neither", "\"mimeType\":\"image/png\"",false},
  {"null text", "\"text\":null",false},
  {"wrong blob type", "\"blob\":123",false},
 } {
  t.Run(test.name,func(t *testing.T){
   server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
    var request struct{ID json.RawMessage};if err:=json.NewDecoder(r.Body).Decode(&request);err!=nil{t.Fatal(err)}
    w.Header().Set("Content-Type","application/json")
    reply:=[]byte("{\"jsonrpc\":\"2.0\",\"id\":"+string(request.ID)+",\"result\":{\"resultType\":\"complete\",\"ttlMs\":0,\"cacheScope\":\"private\",\"contents\":[{\"uri\":\"doc://list\","+test.content+"}]}}")
    if _,err:=w.Write(reply);err!=nil{t.Error(err)}
   }));defer server.Close()
   location,err:=url.Parse(server.URL);if err!=nil{t.Fatal(err)}
   client:=NewClient(location.Scheme,location.Host,server.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
   result,err:=client.ResourcesRead()(context.Background(),&genmcpresources.ResourcesReadPayload{URI:"doc://list"})
   if test.valid && err!=nil{t.Fatal(err)}
   if !test.valid && err==nil{t.Fatalf("accepted invalid reply: %+v",result)}
   if test.valid {
    content:=result.(*genmcpresources.ResourcesReadResult).Contents[0]
    if (content.Text==nil)==(content.Blob==nil){t.Fatalf("lost content presence: %+v",content)}
   }
  })
 }
}
`
