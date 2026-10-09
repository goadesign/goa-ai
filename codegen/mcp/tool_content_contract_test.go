// This generated-service test verifies content through the real HTTP adapter,
// clients and Goa result views. It shares the prompt service's typed content.
package codegen

const toolContentGeneratedTestSource = `
func (s *promptService) Rich(ctx context.Context)(*genprompts.RichToolResult,error) {
 if ctx.Value(configuredEndpointKey{})!=true{return nil,errors.New("configured rich middleware missing")}
 s.calls++;return s.richResult,s.failure
}
func (s *promptService) ContentOnly(ctx context.Context)(*genprompts.ContentOnlyResult,error) {
 if ctx.Value(configuredEndpointKey{})!=true{return nil,errors.New("configured content middleware missing")}
 s.calls++;return s.contentResult,s.failure
}

func TestAuthoredToolContent(t *testing.T) {
 service:=&promptService{richView:"detailed"}
 mux:=goahttp.NewMuxer()
 adapter:=genmcpprompts.NewMCPAdapter(configuredPromptEndpoints(t,service),nil)
 server:=genserver.New(genmcpprompts.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 endpoint:=httptest.NewServer(mux);defer endpoint.Close()
 location,err:=url.Parse(endpoint.URL);if err!=nil{t.Fatal(err)}
 client:=NewClient(location.Scheme,location.Host,endpoint.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 caller,err:=NewCaller(client,mcpruntime.ClientInfo{Name:"rich-contract",Version:"1"},mcpruntime.InputSupport{},mcpruntime.HTTPRetryPolicy{})
 if err!=nil{t.Fatal(err)}
 catalog,err:=client.ToolsList()(t.Context(),&genmcpprompts.ToolsListPayload{});if err!=nil{t.Fatal(err)}
 for _,tool:=range catalog.(*genmcpprompts.ToolsListResult).Tools {
  if tool.Name=="rich" && (strings.Contains(string(tool.OutputSchema),"attachments")||!strings.Contains(string(tool.OutputSchema),"summary")){t.Fatalf("rich schema=%s",tool.OutputSchema)}
  if tool.Name=="content-only" && len(tool.OutputSchema)!=0{t.Fatalf("content-only schema=%s",tool.OutputSchema)}
 }
 source:="{\"summary\":\"domain\",\"attachments\":["+
  "{\"type\":\"text\",\"value\":{\"text\":\"private presentation\",\"annotations\":{\"audience\":[\"user\"]},\"_meta\":{\"number\":9007199254740993}}},"+
  "{\"type\":\"image\",\"value\":{\"data\":\"AQID\",\"mimeType\":\"image/png\"}},"+
  "{\"type\":\"audio\",\"value\":{\"data\":\"BAUG\",\"mimeType\":\"audio/wav\"}},"+
  "{\"type\":\"resource_link\",\"value\":{\"name\":\"guide\",\"uri\":\"doc://guide\",\"icons\":[{\"src\":\"https://example.com/icon\"}]}},"+
  "{\"type\":\"resource\",\"value\":{\"resource\":{\"type\":\"blob\",\"value\":{\"uri\":\"doc://blob\",\"blob\":\"BwgJ\"}}}}]}"
 service.richResult=&genprompts.RichToolResult{}
 var authored struct { Summary string; Attachments []json.RawMessage }
 if err:=json.Unmarshal([]byte(source),&authored);err!=nil{t.Fatal(err)}
 wrapped:=make([]map[string]json.RawMessage,len(authored.Attachments))
 for index,item:=range authored.Attachments { wrapped[index]=map[string]json.RawMessage{"selected":item} }
 encodedAuthored,err:=json.Marshal(struct {Summary string;Attachments []map[string]json.RawMessage}{Summary:authored.Summary,Attachments:wrapped});if err!=nil{t.Fatal(err)}
 if err:=json.Unmarshal(encodedAuthored,service.richResult);err!=nil{t.Fatal(err)}
 response,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"rich"});if err!=nil{t.Fatal(err)}
 if len(response.Content)!=5{t.Fatalf("content=%+v",response.Content)}
 kinds:=[]string{"text","image","audio","resource_link","resource"}
 encodedContent,err:=json.Marshal(response.Content);if err!=nil{t.Fatal(err)}
 var contentFields []map[string]json.RawMessage
 if err:=json.Unmarshal(encodedContent,&contentFields);err!=nil{t.Fatal(err)}
 for index,fields:=range contentFields {
  var kind string;if err:=json.Unmarshal(fields["type"],&kind);err!=nil{t.Fatal(err)}
  if kind!=kinds[index]{t.Fatalf("kind=%s wanted=%s",kind,kinds[index])}
  if index==0&&!bytes.Contains(fields["_meta"],[]byte("9007199254740993")){t.Fatal("metadata number changed")}
 }
 if strings.Contains(string(response.StructuredContent),"attachments")||strings.Contains(string(response.StructuredContent),"private presentation")||!strings.Contains(string(response.StructuredContent),"domain"){t.Fatalf("structured=%s",response.StructuredContent)}
 for _,spec:=range genrichspecs.Specs() {
  switch spec.Name {
  case genrichspecs.Rich:
   if bytes.Contains(spec.Result.Schema,[]byte("attachments"))||bytes.Contains(spec.Result.ExampleJSON,[]byte("attachments")){t.Fatalf("spec content leaked: %+v",spec.Result)}
   for _,field:=range spec.Result.Fields { for _,segment:=range field.Path { if segment==tools.FixedField("attachments"){t.Fatal("content appears in domain field metadata")} } }
   decoded,err:=spec.Result.Codec.FromJSON(response.StructuredContent);if err!=nil{t.Fatal(err)}
   encoded,err:=spec.Result.Codec.ToJSON(decoded);if err!=nil{t.Fatal(err)}
   if bytes.Contains(encoded,[]byte("attachments")){t.Fatalf("codec content leaked: %s",encoded)}
   var root map[string]json.RawMessage
   if err:=json.Unmarshal(response.StructuredContent,&root);err!=nil{t.Fatal(err)}
   if value,present:=root["value"];present {
    var fields map[string]json.RawMessage
    if err:=json.Unmarshal(value,&fields);err!=nil{t.Fatal(err)}
    fields["attachments"]=json.RawMessage("[]")
    root["value"],err=json.Marshal(fields);if err!=nil{t.Fatal(err)}
   } else { root["attachments"]=json.RawMessage("[]") }
   rejected,err:=json.Marshal(root);if err!=nil{t.Fatal(err)}
   if _,err:=spec.Result.Codec.FromJSON(rejected);err==nil{t.Fatalf("domain codec accepted attachments: %s",rejected)}
  case genrichspecs.ContentOnly:
   if len(spec.Result.Schema)!=0||spec.Result.Codec.FromJSON!=nil{t.Fatalf("content-only advertised result: %+v",spec.Result)}
  }
 }
 executed,err:=genrichexec.NewMCPExecutor(caller).Execute(t.Context(),&agentruntime.ToolCallMeta{},&agentruntime.ToolCall{Name:genrichspecs.Rich,Payload:[]byte("{}")})
 if err!=nil{t.Fatal(err)}
 if executed.ToolResult.Failure!=nil||!reflect.DeepEqual(executed.ToolResult.Blocks,response.Content){t.Fatalf("executor=%+v",executed.ToolResult)}
 if bytes.Contains(response.StructuredContent,[]byte("\"type\"")) {
  service.richView="summary"
  omitted,err:=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"rich"});if err!=nil{t.Fatal(err)}
  if len(omitted.Content)!=0||strings.Contains(string(omitted.StructuredContent),"attachments")||!strings.Contains(string(omitted.StructuredContent),"summary"){t.Fatalf("omitted view=%+v",omitted)}
  service.richView="detailed"
 }
 service.contentResult=&genprompts.ContentOnlyResult{Attachments:service.richResult.Attachments}
 response,err=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"content-only"});if err!=nil{t.Fatal(err)}
 if len(response.Content)!=5||len(response.StructuredContent)!=0{t.Fatalf("content-only=%+v",response)}
 service.contentResult=&genprompts.ContentOnlyResult{}
 response,err=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"content-only"});if err!=nil{t.Fatal(err)}
 if len(response.Content)!=0||len(response.StructuredContent)!=0{t.Fatalf("empty=%+v",response)}
 service.contentResult=nil
 _,err=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"content-only"})
 var protocol *mcpruntime.Error
 if !errors.As(err,&protocol)||protocol.Code!=-32603{t.Fatalf("missing result err=%v",err)}
 service.contentResult=&genprompts.ContentOnlyResult{}
 if err:=json.Unmarshal([]byte("{\"attachments\":[{\"selected\":{\"type\":\"text\",\"value\":{\"text\":\"invalid\",\"_meta\":[]}}}]}"),service.contentResult);err!=nil{t.Fatal(err)}
 _,err=caller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"content-only"})
 if !errors.As(err,&protocol)||protocol.Code!=-32603{t.Fatalf("invalid metadata err=%v",err)}
}
`
