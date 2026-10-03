// Package codegen checks that Goa core and the MCP plugin write one attached service
// from the same generation run.
package codegen

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "goa.design/goa-ai/codegen/agent"
	agentexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goagenerator "goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

const callerLifecycleGeneratedTestSource = `// These tests run independent HTTP requests against the actual generated MCP server.
package client

import (
 "bytes"
 "context"
 "encoding/json"
 "io"
 "net/http"
 "net/http/httptest"
 "net/url"
 "strings"
 "testing"
 genmcpfmt "generated.local/gen/mcp_fmt"
 genmcpfmtsrv "generated.local/gen/jsonrpc/mcp_fmt/server"
 goahttp "goa.design/goa/v3/http"
 mcpruntime "goa.design/goa-ai/runtime/mcp"
)

type retryDoer func(*http.Request) (*http.Response,error)
func (f retryDoer) Do(r *http.Request) (*http.Response,error) { return f(r) }

type echoService struct { calls int }
func (s *echoService) Echo(context.Context) (string,error) {s.calls++;return "hello",nil}

func TestGeneratedStatelessProtocol(t *testing.T) {
 service:=&echoService{}
 adapter:=genmcpfmt.NewMCPAdapter(service,nil)
 mux:=goahttp.NewMuxer()
 server:=genmcpfmtsrv.New(genmcpfmt.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genmcpfmtsrv.MountWithOrigins(mux,server,[]string{"https://allowed.test"})
 endpoint:=httptest.NewServer(mux);defer endpoint.Close()
 location,err:=url.Parse(endpoint.URL);if err!=nil{t.Fatal(err)}
 client:=NewClient(location.Scheme,location.Host,endpoint.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 caller,err:=NewCaller(client,mcpruntime.ClientInfo{Name:"test",Version:"1"},mcpruntime.InputSupport{},mcpruntime.HTTPRetryPolicy{});if err!=nil{t.Fatal(err)}
 result,err:=caller.CallTool(context.Background(),mcpruntime.CallRequest{Tool:"echo",Payload:json.RawMessage("{}")});if err!=nil{t.Fatal(err)}
 if string(result.StructuredContent)!="\"hello\"" || len(result.Content)!=0 || service.calls!=1 {t.Fatalf("result=%+v calls=%d",result,service.calls)}
 discovered,err:=client.ServerDiscover()(context.Background(),&genmcpfmt.DiscoverPayload{});if err!=nil{t.Fatal(err)}
 discovery:=discovered.(*genmcpfmt.DiscoverResult)
 if discovery.ResultType!="complete"||len(discovery.SupportedVersions)!=1||discovery.SupportedVersions[0]!=mcpruntime.ProtocolVersion{t.Fatalf("discovery=%+v",discovery)}
 catalog,err:=client.ToolsList()(context.Background(),&genmcpfmt.ToolsListPayload{});if err!=nil{t.Fatal(err)}
 if catalog.(*genmcpfmt.ToolsListResult).CacheScope!="private"{t.Fatal("missing cache scope")}
 hints:=catalog.(*genmcpfmt.ToolsListResult).Tools[0].Annotations
 if hints==nil||hints.Title==nil||*hints.Title!="Echo"||hints.ReadOnlyHint==nil||!*hints.ReadOnlyHint||hints.DestructiveHint==nil||*hints.DestructiveHint||hints.IdempotentHint==nil||*hints.IdempotentHint||hints.OpenWorldHint==nil||*hints.OpenWorldHint {t.Fatalf("hints=%+v",hints)}
 var ids []string
 doer:=retryDoer(func(request *http.Request)(*http.Response,error){
  body,err:=io.ReadAll(request.Body);if err!=nil{return nil,err};if err:=request.Body.Close();err!=nil{return nil,err}
  var envelope struct{ID string};if err:=json.Unmarshal(body,&envelope);err!=nil{return nil,err}
  ids=append(ids,envelope.ID)
  request.Body=io.NopCloser(bytes.NewReader(body))
  response,err:=endpoint.Client().Do(request);if err!=nil{return nil,err}
  if len(ids)==1 {if err:=response.Body.Close();err!=nil{return nil,err};response.Header.Set("Content-Type","text/event-stream");response.Body=io.NopCloser(strings.NewReader(": accepted\n\n"))}
  return response,nil
 })
 retryClient:=NewClient(location.Scheme,location.Host,doer,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 retryCaller,err:=NewCaller(retryClient,mcpruntime.ClientInfo{Name:"test",Version:"1"},mcpruntime.InputSupport{},mcpruntime.HTTPRetryPolicy{MaxAttempts:2,TrustToolAnnotations:true});if err!=nil{t.Fatal(err)}
 retried,err:=retryCaller.CallTool(t.Context(),mcpruntime.CallRequest{Tool:"echo",Payload:json.RawMessage("{}")});if err!=nil{t.Fatal(err)}
 if string(retried.StructuredContent)!="\"hello\""||service.calls!=3||len(ids)!=2||ids[0]==ids[1] {t.Fatalf("result=%+v calls=%d ids=%v",retried,service.calls,ids)}

 for _,origins:=range [][]string{{"https://allowed.test"},{""},{"https://evil.test"},{"https://allowed.test","https://evil.test"}} {
  request,err:=http.NewRequestWithContext(t.Context(),"POST",endpoint.URL+"/fmt",strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":\"origin\",\"method\":\"server/discover\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"2026-07-28\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}"));if err!=nil{t.Fatal(err)}
  request.Header.Set("MCP-Protocol-Version",mcpruntime.ProtocolVersion)
  request.Header.Set("MCP-Method","server/discover")
  request.Header["Origin"]=origins
  response,err:=endpoint.Client().Do(request);if err!=nil{t.Fatal(err)}
  if err:=response.Body.Close();err!=nil{t.Fatal(err)}
  expected:=403;if len(origins)==1&&origins[0]=="https://allowed.test"{expected=200}
  if response.StatusCode!=expected{t.Fatalf("origins=%v status=%d",origins,response.StatusCode)}
 }
 for _,method:=range []string{"GET","DELETE"}{
  request,err:=http.NewRequest(method,endpoint.URL+"/fmt",nil);if err!=nil{t.Fatal(err)}
  response,err:=endpoint.Client().Do(request);if err!=nil{t.Fatal(err)}
  if err:=response.Body.Close();err!=nil{t.Fatal(err)}
  if response.StatusCode!=405{t.Fatal(response.StatusCode)}
 }
 for _,test:=range []struct{method string;version string;status,code int}{
  {"tools/call",mcpruntime.ProtocolVersion,200,0},
  {"server/discover","2025-06-18",400,mcpruntime.UnsupportedProtocolVersion},
  {"initialize",mcpruntime.ProtocolVersion,404,mcpruntime.JSONRPCMethodNotFound},
 }{
  body:=[]byte("{\"jsonrpc\":\"2.0\",\"id\":9007199254740993,\"method\":\""+test.method+"\",\"params\":{\"name\":\"echo\",\"arguments\":{},\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\""+test.version+"\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}")
  request,err:=http.NewRequest("POST",endpoint.URL+"/fmt",bytes.NewReader(body));if err!=nil{t.Fatal(err)}
  request.Header.Set("Content-Type","application/json");request.Header.Set("MCP-Protocol-Version",test.version);request.Header.Set("Mcp-Method",test.method);if test.method=="tools/call"{request.Header.Set("Mcp-Name","echo")}
  response,err:=endpoint.Client().Do(request);if err!=nil{t.Fatal(err)}
  encoded,err:=io.ReadAll(response.Body);if err!=nil{t.Fatal(err)};if err:=response.Body.Close();err!=nil{t.Fatal(err)}
  if response.StatusCode!=test.status||!strings.Contains(string(encoded),"9007199254740993"){t.Fatalf("status=%d body=%s",response.StatusCode,encoded)}
  if test.code!=0{var failure struct{Error struct{Code int}};if err:=json.Unmarshal(encoded,&failure);err!=nil{t.Fatal(err)};if failure.Error.Code!=test.code{t.Fatalf("failure=%s",encoded)}}
 }
}
`

const registerStringGeneratedTestSource = `// This file checks that generated remote-tool registration preserves every JSON string character.
package mcpfmt

import (
	"context"
	"encoding/json"
	"testing"

	genmcpcalc "generated.local/gen/calc/toolsets/calc"
	genmcpformatter "generated.local/gen/formatter/toolsets/formatter"
	genmcpselector "generated.local/gen/selector/toolsets/selector"
	genfmtspecs "generated.local/gen/fmt_/toolsets/fmt"
    genfmtexec "generated.local/gen/fmt_/toolsets/fmt/mcp"
    "goa.design/goa-ai/runtime/agent/engine"
    "goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

func TestGeneratedMCPModelAndExecutionCodecsMatch(t *testing.T) {
    payloads := map[string][]byte{
        "fmt.echo": []byte("{}"),
        "calc.add": []byte("{\"operand\":{\"value\":1}}"),
        "calc.subtract": []byte("{\"subtrahend\":{\"value\":1}}"),
        "formatter.render": []byte("{\"value\":\"render me\"}"),
        "selector.read-value": []byte("{}"),
        "selector.read_value": []byte("{}"),
    }
	for _, specs := range [][]tools.ToolSpec{
		genfmtspecs.Specs(),
		genmcpcalc.Specs(),
		genmcpformatter.Specs(),
		genmcpselector.Specs(),
	} {
		for _, spec := range specs {
			t.Run(spec.Name.String(), func(t *testing.T) {
				if _, err := model.NewToolDefinitionFromSpec(spec); err != nil {
					t.Fatal(err)
				}
				if string(spec.ExecutionPayloadSchema) != string(spec.Payload.Schema) {
					t.Fatal("MCP execution schema differs from model input")
				}
				for _, codec := range []tools.JSONCodec[any]{spec.Payload.Codec, spec.ExecutionPayloadCodec} {
					if codec.FromJSON == nil || codec.ToJSON == nil {
						t.Fatal("generated payload codec is incomplete")
					}
					value, err := codec.FromJSON(payloads[spec.Name.String()])
					if err != nil {
						t.Fatal(err)
					}
					encoded, err := codec.ToJSON(value)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := codec.FromJSON(encoded); err != nil {
						t.Fatal(err)
					}
					if _, err := codec.FromJSON([]byte("null")); err == nil {
						t.Fatal("generated payload codec accepted null")
					}
				}
			})
		}
	}
}

func TestRegisteredStringToolDecodesEveryControlCharacter(t *testing.T) {
	want := "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"
	caller := mcpruntime.CallerFunc(func(context.Context, mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
		encoded,err:=json.Marshal(want)
        return mcpruntime.CallResponse{StructuredContent: encoded}, err
	})
	rt := agentsruntime.New(storageinmem.New())
	exec := genfmtexec.NewMCPExecutor(caller)
    if err := rt.RegisterToolset(agentsruntime.ToolsetRegistration{Name: "fmt.fmt", Specs: genfmtspecs.Specs(), ToolMetadataLookup: genfmtspecs.MetadataByName, ActivityRetryPolicy: &engine.RetryPolicy{MaxAttempts: 1}, Execute: func(ctx context.Context, call *agentsruntime.ToolCall) (*agentsruntime.ToolExecutionResult, error) { meta := agentsruntime.ToolCallMetaFromCall(*call); return exec.Execute(ctx, &meta, call) }}); err != nil {
		t.Fatal(err)
	}
	out, err := rt.ExecuteToolActivity(context.Background(), &agentsruntime.ToolInput{
		ToolsetName: "fmt.fmt",
		ToolName:    genfmtspecs.Echo,
		Payload:     rawjson.Message(` + "`{}`" + `),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Failure != nil {
		t.Fatalf("string result failed: %s", out.Failure.Error.Message)
	}
	var got string
	if err := json.Unmarshal(out.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("string result = %q, want %q", got, want)
	}
}
`

func TestMCPPluginUsesCorePlanForAttachedService(t *testing.T) {
	goaAIDirectory := testModuleDirectory(t, "goa.design/goa-ai")
	goaDirectory := testModuleDirectory(t, "goa.design/goa/v3")
	// The generated module is intentionally separate from the workspace that
	// compiled this test.
	t.Setenv("GOWORK", "off")
	restoreMCP := resetMCPCodegenState(t)
	defer restoreMCP()
	previousRoot := expr.Root
	defer func() {
		expr.Root = previousRoot
		eval.Reset()
	}()

	service, methods := testService("calc", "add", "subtract")
	namedTypes := setNamedMCPMethodTypes(methods["add"], methods["subtract"])
	formatter, formatterMethods := testService("formatter", "render")
	locatedRenderPayload := testLocatedRenderPayload()
	formatterMethods["render"].Payload = &expr.AttributeExpr{Type: locatedRenderPayload}
	selector, selectorMethods := testService("selector", "read_value", "read-value")
	contextService, contextMethods := testService("context", "ping")
	fmtService, fmtMethods := testService("fmt", "echo")
	prompts, _ := testService("prompts")
	staticPrompts, _ := testService("static_prompts")
	methodPrompts, promptMethods := testService("method_prompts", "review", "empty")
	promptTypes := methodPromptFixture(promptMethods)
	simplePrompt, simpleMethods := testService("simple_prompt", "build")
	simpleText := promptFixtureType("SimpleText", &expr.Object{{Name: "text", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "text")
	simpleContent := promptFixtureType("SimpleContent", &expr.Union{TypeName: "SimpleChoice", Values: []*expr.NamedAttributeExpr{{Name: "text", Attribute: &expr.AttributeExpr{Type: simpleText}}}})
	simpleContent.Meta = expr.MetaExpr{"struct:pkg:path": {"prompt/shared"}}
	simpleMessage := promptFixtureType("SimpleMessage", &expr.Object{
		{Name: "role", Attribute: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Values: []any{"user"}}}},
		{Name: "content", Attribute: &expr.AttributeExpr{Type: simpleContent}},
	}, "role", "content")
	simpleResult := promptFixtureType("SimpleResult", &expr.Object{{Name: "messages", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: simpleMessage}, NonNullableElems: true}}}})
	simpleMethods["build"].Result = &expr.AttributeExpr{Type: simpleResult}
	promptTypes = append(promptTypes, simpleText, simpleContent, simpleMessage, simpleResult)
	resources, resourceMethods := testService("resources", "read_document")
	blobs, blobMethods := testService("blobs", "read_image")
	blobMethods["read_image"].Result = &expr.AttributeExpr{Type: expr.Bytes}
	root := testRootExpr([]*expr.ServiceExpr{service, formatter, selector, contextService, fmtService, prompts, staticPrompts, resources, blobs, methodPrompts, simplePrompt}, []*expr.HTTPServiceExpr{
		jsonrpcService(service, "/calc"),
		jsonrpcService(formatter, "/formatter"),
		jsonrpcService(selector, "/selector"),
		jsonrpcService(contextService, "/context"),
		jsonrpcService(fmtService, "/fmt"),
		jsonrpcService(prompts, "/prompts"),
		jsonrpcService(staticPrompts, "/static-prompts"),
		jsonrpcService(methodPrompts, "/method-prompts"),
		jsonrpcService(simplePrompt, "/simple-prompt"),
		jsonrpcService(resources, "/resources"),
		jsonrpcService(blobs, "/blobs"),
	})
	httpService := root.API.HTTP.ServiceFor(service, root.API.HTTP)
	httpEndpoint := httpService.EndpointFor(methods["add"])
	httpEndpoint.Routes = []*expr.RouteExpr{{
		Method:   http.MethodPost,
		Path:     "/calculate",
		Endpoint: httpEndpoint,
	}}
	root.API.Name = "calc"
	root.API.Version = "1.0"
	root.API.GRPC = &expr.GRPCExpr{}
	root.API.RandomizerFactory = expr.NewDeterministicRandomizerFactory()
	root.Types = append(root.Types, namedTypes...)
	root.Types = append(root.Types, locatedRenderPayload)
	root.Types = append(root.Types, promptTypes...)
	root.WalkSets(func(eval.ExpressionSet) {})
	for _, current := range []*expr.ServiceExpr{service, formatter, selector, contextService, fmtService, prompts, staticPrompts, resources, blobs, methodPrompts, simplePrompt} {
		for _, method := range current.Methods {
			method.Prepare()
		}
	}
	httpService.Prepare()
	httpEndpoint.Prepare()
	httpEndpoint.Finalize()
	expr.Root = root
	eval.Reset()
	require.NoError(t, eval.Register(root))
	require.NoError(t, eval.Register(mcpexpr.Root))
	require.NoError(t, eval.Register(&agentexpr.RootExpr{}))
	mcpexpr.Root.RegisterMCP(service, &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
			{Name: "subtract", Method: methods["subtract"]},
		},
	})
	mcpexpr.Root.RegisterMCP(formatter, &mcpexpr.MCPExpr{
		Name:    "formatter",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "render", Method: formatterMethods["render"]},
		},
	})
	mcpexpr.Root.RegisterMCP(selector, &mcpexpr.MCPExpr{
		Name:    "selector",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "read_value", Method: selectorMethods["read_value"]},
			{Name: "read-value", Method: selectorMethods["read-value"]},
		},
	})
	mcpexpr.Root.RegisterMCP(contextService, &mcpexpr.MCPExpr{
		Name:    "context",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "ping", Method: contextMethods["ping"]},
		},
	})
	mcpexpr.Root.RegisterMCP(fmtService, &mcpexpr.MCPExpr{
		Name:    "fmt",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "echo", Method: fmtMethods["echo"], Annotations: &mcpexpr.ToolAnnotationsExpr{Title: new("Echo"), ReadOnlyHint: new(true), DestructiveHint: new(false), IdempotentHint: new(false), OpenWorldHint: new(false)}},
		},
	})
	mcpexpr.Root.RegisterMCP(simplePrompt, &mcpexpr.MCPExpr{
		Name: "simple-prompt", Version: "1",
		MethodPrompts: []*mcpexpr.MethodPromptExpr{{Name: "simple", Description: "Return one text message", Method: simpleMethods["build"]}},
	})
	mcpexpr.Root.RegisterMCP(methodPrompts, &mcpexpr.MCPExpr{
		Name: "method-prompts", Version: "1.0.0",
		MethodPrompts: []*mcpexpr.MethodPromptExpr{
			{Name: "review", Description: "Review source code", Method: promptMethods["review"]},
			{Name: "review-copy", Description: "Select the same review operation", Method: promptMethods["review"]},
			{Name: "empty", Description: "Return an empty message sequence", Method: promptMethods["empty"]},
		},
		Tools:   []*mcpexpr.ToolExpr{{Name: "empty-messages", Description: "Return the authored message record", Method: promptMethods["empty"]}},
		Prompts: []*mcpexpr.PromptExpr{{Name: "fixed", Messages: []*mcpexpr.MessageExpr{{Role: "assistant", Content: "Fixed instructions"}}}},
	})
	mcpexpr.Root.RegisterMCP(prompts, &mcpexpr.MCPExpr{
		Name:    "prompts",
		Version: "1.0.0",
		Prompts: []*mcpexpr.PromptExpr{{
			Name: "daily_report",
			Messages: []*mcpexpr.MessageExpr{{
				Role:    "user",
				Content: "Summarize today.",
			}},
		}},
	})
	mcpexpr.Root.RegisterMCP(staticPrompts, &mcpexpr.MCPExpr{
		Name:    "static-prompts",
		Version: "1.0.0",
		Prompts: []*mcpexpr.PromptExpr{{
			Name: "help",
			Messages: []*mcpexpr.MessageExpr{{
				Role:    "user",
				Content: "Explain the available actions.",
			}},
		}},
	})
	mcpexpr.Root.RegisterMCP(resources, &mcpexpr.MCPExpr{
		Name:    "resources",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "documents", URI: "doc://list", MimeType: "application/json", Method: resourceMethods["read_document"]},
		},
	})
	mcpexpr.Root.RegisterMCP(blobs, &mcpexpr.MCPExpr{
		Name: "blobs", Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "image", URI: "asset://image", MimeType: "image/png", Method: blobMethods["read_image"]},
		},
	})
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte(fmt.Sprintf(`module generated.local

go 1.25

require (
	goa.design/goa-ai v0.0.0
	goa.design/goa/v3 v3.0.0
)

replace goa.design/goa-ai => %s

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaAIDirectory), filepath.ToSlash(goaDirectory))),
		0o600,
	))

	_, err := goagenerator.Generate(dir, "gen", false)

	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "gen", "mcp_calc", "service.go"))
	require.FileExists(t, filepath.Join(dir, "gen", "mcp_calc", "adapter_server.go"))
	require.FileExists(t, filepath.Join(dir, "gen", "http", "calc", "server", "server.go"))
	generatedRoot, err := os.OpenRoot(filepath.Join(dir, "gen"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, generatedRoot.Close())
	})
	_, err = generatedRoot.Stat("mcp_calc/adapter/client")
	require.ErrorIs(t, err, os.ErrNotExist)
	serviceSource, err := generatedRoot.ReadFile("mcp_calc/service.go")
	require.NoError(t, err)
	adapterSource, err := generatedRoot.ReadFile("mcp_calc/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(serviceSource), "package mcpcalc")
	require.Contains(t, string(adapterSource), "package mcpcalc")
	codec, err := generatedRoot.ReadFile("mcp_calc/internal/codec/codec.go")
	require.NoError(t, err)
	require.Contains(t, string(codec), "func DecodeAddPayload(")
	require.Contains(t, string(codec), "func EncodeAddResult(")
	require.Contains(t, string(codec), "func DecodeAddResult(")
	require.Contains(t, string(codec), "func EncodeAddResult(in *calc.CalculationResponse)")
	require.Contains(t, string(codec), "func DecodeAddResult(data []byte) (out *calc.CalculationResponse, err error)")
	require.Contains(t, string(codec), "CalculationRequest")
	require.Contains(t, string(codec), "Operand")
	require.Contains(t, string(codec), "v *calc.Calculation")
	require.Contains(t, string(codec), "func DecodeSubtractPayload(")
	adapterServer, err := generatedRoot.ReadFile("mcp_calc/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(adapterServer), "mcpcodec.DecodeAddPayload(arguments)")
	require.Contains(t, string(adapterServer), "mcpcodec.EncodeAddResult(result)")
	require.NotContains(t, string(adapterServer), "\n\t\"bytes\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"io\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"net/http\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"strings\"\n")
	_, err = generatedRoot.Stat("mcp_calc/register.go")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, filepath.Join(dir, "gen", "calc", "toolsets", "calc", "specs.go"))
	require.FileExists(t, filepath.Join(dir, "gen", "calc", "toolsets", "calc", "mcp", "mcp_executor.go"))
	_, err = os.Stat(filepath.Join(dir, "gen", "jsonrpc", "calc", "client"))
	require.ErrorIs(t, err, os.ErrNotExist)
	server, err := generatedRoot.ReadFile("jsonrpc/mcp_calc/server/server.go")
	require.NoError(t, err)
	require.Contains(t, string(server), "\n\t\"bytes\"\n")
	require.Contains(t, string(server), "withMCPTransport(h, allowedOrigins, h.ServeHTTP)")
	require.Contains(t, string(server), "func MountWithOrigins(")
	require.Contains(t, string(server), `mux.Handle("GET", "/calc", mcpMethodNotAllowed(allowedOrigins))`)
	require.Contains(t, string(server), "mcpruntime.ValidateHTTPRequest(r, body,")
	require.Contains(t, string(server), "mcpruntime.WriteProtocolError")
	formatterCodec, err := generatedRoot.ReadFile("mcp_formatter/internal/codec/codec.go")
	require.NoError(t, err)
	require.Contains(t, string(formatterCodec), "func DecodeRenderPayload(")
	require.Contains(t, string(formatterCodec), "func EncodeRenderResult(")
	require.Contains(t, string(formatterCodec), "func DecodeRenderResult(")
	formatterServer, err := generatedRoot.ReadFile("mcp_formatter/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(formatterServer), "mcpcodec.DecodeRenderPayload(arguments)")
	require.Contains(t, string(formatterServer), "encoded, err := mcpcodec.EncodeRenderResult(result)")
	_, err = generatedRoot.Stat("mcp_prompts/prompt_provider.go")
	require.ErrorIs(t, err, os.ErrNotExist)
	promptServer, err := generatedRoot.ReadFile("mcp_prompts/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(promptServer), `case "daily_report":`)
	require.NotContains(t, string(promptServer), "PromptProvider")
	require.NotContains(t, string(promptServer), "promptProvider")
	_, err = generatedRoot.Stat("mcp_static_prompts/prompt_provider.go")
	require.ErrorIs(t, err, os.ErrNotExist)
	selectorService, err := generatedRoot.ReadFile("selector/service.go")
	require.NoError(t, err)
	require.Contains(t, string(selectorService), "ReadValue(context.Context) (res string, err error)")
	require.Contains(t, string(selectorService), "ReadValueEndpoint(context.Context) (res string, err error)")
	selectorServer, err := generatedRoot.ReadFile("mcp_selector/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(selectorServer), "a.service.ReadValue(ctx)")
	require.Contains(t, string(selectorServer), "a.service.ReadValueEndpoint(ctx)")
	selectorJSONRPCStream, err := generatedRoot.ReadFile("jsonrpc/mcp_selector/client/stream.go")
	require.Error(t, err)
	require.Empty(t, selectorJSONRPCStream)
	selectorCaller, err := generatedRoot.ReadFile("jsonrpc/mcp_selector/client/caller.go")
	require.NoError(t, err)
	require.Contains(t, string(selectorCaller), `mcpselector "generated.local/gen/mcp_selector"`)
	require.Contains(t, string(selectorCaller), "mcpruntime.NewHTTPTransport(client.Doer, info,")
	selectorSession, err := generatedRoot.ReadFile("jsonrpc/mcp_selector/client/session.go")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Empty(t, selectorSession)
	resourceServer, err := generatedRoot.ReadFile("mcp_resources/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(resourceServer), `case "doc://list":`)
	require.Contains(t, string(resourceServer), "result, err := a.service.ReadDocument(ctx)")
	require.NotContains(t, string(resourceServer), "ParseQuery")
	_, err = generatedRoot.Stat("mcp_blobs/internal/codec/codec.go")
	require.ErrorIs(t, err, os.ErrNotExist)
	blobServer, err := generatedRoot.ReadFile("mcp_blobs/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(blobServer), "base64.StdEncoding.EncodeToString(result)")
	require.NotContains(t, string(blobServer), "mcpcodec")
	resourceCodec, err := generatedRoot.ReadFile("mcp_resources/internal/codec/codec.go")
	require.NoError(t, err)
	require.Contains(t, string(resourceCodec), "func EncodeReadDocumentResult")
	require.NotContains(t, string(resourceCodec), "ReadDocumentPayloadTransport")
	callerTest, err := generatedRoot.OpenFile(
		"jsonrpc/mcp_fmt/client/caller_lifecycle_test.go",
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
		0o600,
	)
	require.NoError(t, err)
	_, err = callerTest.WriteString(callerLifecycleGeneratedTestSource)
	require.NoError(t, err)
	require.NoError(t, callerTest.Close())
	resourceTest, err := generatedRoot.OpenFile("jsonrpc/mcp_resources/client/resource_content_test.go", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, err = resourceTest.WriteString(resourceContentGeneratedTestSource)
	require.NoError(t, err)
	require.NoError(t, resourceTest.Close())
	contentTest, err := generatedRoot.OpenFile("jsonrpc/mcp_fmt/client/content_contract_test.go", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, err = contentTest.WriteString(contentContractGeneratedTestSource)
	require.NoError(t, err)
	require.NoError(t, contentTest.Close())
	registerTest, err := generatedRoot.OpenFile(
		"mcp_fmt/register_string_test.go",
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
		0o600,
	)
	require.NoError(t, err)
	_, err = registerTest.WriteString(registerStringGeneratedTestSource)
	require.NoError(t, err)
	require.NoError(t, registerTest.Close())
	promptTest, err := generatedRoot.OpenFile("jsonrpc/mcp_method_prompts/client/method_prompt_test.go", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, err = promptTest.WriteString(methodPromptGeneratedTestSource)
	require.NoError(t, err)
	require.NoError(t, promptTest.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./gen/...")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

// testLocatedRenderPayload returns a service payload moved to a generated
// package whose preferred name collides with the private MCP codec package.
func testLocatedRenderPayload() *expr.UserTypeExpr {
	return &expr.UserTypeExpr{
		TypeName: "RenderPayload",
		UID:      "mcp-integration-render-payload",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"mcpcodec"}},
		},
	}
}

// setNamedMCPMethodTypes gives one generated codec authored service types at
// both ends, with another authored type nested inside each one.
func setNamedMCPMethodTypes(add, subtract *expr.MethodExpr) []expr.UserType {
	operand := &expr.UserTypeExpr{
		TypeName: "Operand",
		UID:      "mcp-integration-operand",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"alpha/shared"}},
		},
	}
	request := &expr.UserTypeExpr{
		TypeName: "CalculationRequest",
		UID:      "mcp-integration-request",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "operand", Attribute: &expr.AttributeExpr{Type: operand}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"operand"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"alpha/shared"}},
		},
	}
	subtrahend := &expr.UserTypeExpr{
		TypeName: "Subtrahend",
		UID:      "mcp-integration-subtrahend",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"zeta/shared"}},
		},
	}
	subtractRequest := &expr.UserTypeExpr{
		TypeName: "SubtractRequest",
		UID:      "mcp-integration-subtract-request",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "subtrahend", Attribute: &expr.AttributeExpr{Type: subtrahend}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"subtrahend"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"zeta/shared"}},
		},
	}
	subtractResponse := &expr.UserTypeExpr{
		TypeName: "SubtractResponse",
		UID:      "mcp-integration-subtract-response",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "difference", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"difference"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"zeta/shared"}},
		},
	}
	calculation := &expr.UserTypeExpr{
		TypeName: "Calculation",
		UID:      "mcp-integration-calculation",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
		},
	}
	response := &expr.UserTypeExpr{
		TypeName: "CalculationResponse",
		UID:      "mcp-integration-response",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "calculation", Attribute: &expr.AttributeExpr{Type: calculation}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"calculation"}},
		},
	}
	add.Payload = &expr.AttributeExpr{Type: request}
	add.Result = &expr.AttributeExpr{Type: response}
	subtract.Payload = &expr.AttributeExpr{Type: subtractRequest}
	subtract.Result = &expr.AttributeExpr{Type: subtractResponse}
	return []expr.UserType{operand, request, subtrahend, subtractRequest, subtractResponse, calculation, response}
}

// testModuleDirectory returns the local module selected by the workspace that
// compiled this test.
func testModuleDirectory(t *testing.T, module string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"list", "-m", "-f", "{{.Dir}}", module}
	// #nosec G204 -- the module name comes from this test file.
	command := exec.CommandContext(ctx, "go", args...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.TrimSpace(string(output))
}
