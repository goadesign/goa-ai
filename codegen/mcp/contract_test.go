// This file verifies the service contract built by the MCP generator before
// Goa plans and renders it.
package codegen

import (
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestPrepareServices_PreservesMethodsWithoutMCPDeclarations(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("calc", "add", "subtract")
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.NoError(t, err)
	require.Same(t, methods["subtract"], svc.Method("subtract"))
	require.Nil(t, root.Service("mcp_calc").Method("subtract"))
	require.Len(t, mcpexpr.Root.GetMCP(svc).Tools, 1)
}

func TestPrepareServices_AttachesGeneratedMCPDesign(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("calc", "add")
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.NoError(t, err)
	require.Len(t, root.Services, 2)
	require.Equal(t, "mcp_calc", root.Services[1].Name)
	require.NotEmpty(t, root.Types)
	require.Empty(t, root.API.HTTP.Services)
	require.Len(t, root.API.JSONRPC.Services, 1)
	require.Same(t, root.Services[1], root.API.JSONRPC.Services[0].ServiceExpr)
	require.Equal(t, []string{"/rpc"}, root.API.JSONRPC.Services[0].HTTPEndpoints[0].Routes[0].FullPaths())
	mcpService := root.Services[1]
	discover := mcpService.Method("server/discover")
	require.NotNil(t, discover)
	require.True(t, discover.Payload.IsRequired("_meta"))
	require.Nil(t, mcpService.Method("initialize"))
	require.Nil(t, mcpService.Method("notifications/initialized"))
	toolsCall := mcpService.Method("tools/call")
	require.NotNil(t, toolsCall.Result)
	require.Same(t, toolsCall.Result, toolsCall.StreamingResult)
	require.False(t, toolsCall.IsStreaming())
	require.False(t, toolsCall.HasMixedResults())
	require.Nil(t, mcpService.Method("resources/subscribe"))
	require.Nil(t, mcpService.Method("resources/unsubscribe"))
	require.Nil(t, mcpService.Method("events/stream"))
	require.Nil(t, mcpService.Method("ping"))
}

func TestPrepareServices_BuildsCurrentMCPWireTypes(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "call", "read")
	methods["call"].Result = &expr.AttributeExpr{Type: &expr.Object{
		{Name: "answer", Attribute: &expr.AttributeExpr{Type: expr.String}},
	}}
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "call", Method: methods["call"]},
		},
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "read", URI: "doc://read", MimeType: "application/json", Method: methods["read"]},
		},
		Prompts: []*mcpexpr.PromptExpr{{
			Name: "help",
			Messages: []*mcpexpr.MessageExpr{{
				Role:    "user",
				Content: "Help the user.",
			}},
		}},
	})

	require.NoError(t, prepareServices([]eval.Root{root}))
	toolInfo := testRootType(t, root, "ToolInfo")
	require.True(t, toolInfo.IsRequired("name"))
	require.True(t, toolInfo.IsRequired("inputSchema"))
	require.NotNil(t, expr.AsObject(toolInfo.Type).Attribute("outputSchema"))
	callResult := testRootType(t, root, "ToolsCallResult")
	require.NotNil(t, expr.AsObject(callResult.Type).Attribute("structuredContent"))
	content := testRootType(t, root, "ContentItem")
	require.Equal(t, []string{"type"}, content.Validation.Required)
	require.Equal(t, []any{"text", "image", "audio", "resource_link", "resource"}, expr.AsObject(content.Type).Attribute("type").Validation.Values)
	require.NotNil(t, expr.AsObject(content.Type).Attribute("mimeType"))
	require.NotNil(t, expr.AsObject(content.Type).Attribute("data"))
	require.NotNil(t, expr.AsObject(content.Type).Attribute("uri"))

	resource := testRootType(t, root, "ResourceInfo")
	require.True(t, resource.IsRequired("uri"))
	require.True(t, resource.IsRequired("name"))
	resourceContent := testRootType(t, root, "ResourceContent")
	require.Equal(t, []string{"uri"}, resourceContent.Validation.Required)
	require.NotNil(t, expr.AsObject(resourceContent.Type).Attribute("blob"))

	promptArgument := testRootType(t, root, "PromptArgument")
	require.True(t, promptArgument.IsRequired("name"))
	require.False(t, promptArgument.IsRequired("required"))
	promptMessage := testRootType(t, root, "PromptMessage")
	role := expr.AsObject(promptMessage.Type).Attribute("role")
	require.Equal(t, []any{"user", "assistant"}, role.Validation.Values)
	require.Same(t, content.Type, expr.AsObject(promptMessage.Type).Attribute("content").Type.(*expr.UserTypeExpr).Type)
	promptsGet := testRootType(t, root, "PromptsGetPayload")
	arguments := expr.AsMap(expr.AsObject(promptsGet.Type).Attribute("arguments").Type)
	require.NotNil(t, arguments)
	require.Equal(t, expr.String, arguments.KeyType.Type)
	require.Equal(t, expr.String, arguments.ElemType.Type)

	for _, typeName := range []string{"ToolsListResult", "ResourcesListResult", "PromptsListResult"} {
		result := testRootType(t, root, typeName)
		require.NotNil(t, expr.AsObject(result.Type).Attribute("nextCursor"), typeName)
	}
	for _, tc := range []struct {
		typeName  string
		fieldName string
	}{
		{typeName: "ToolsListResult", fieldName: "tools"},
		{typeName: "ToolsCallResult", fieldName: "content"},
		{typeName: "ResourcesListResult", fieldName: "resources"},
		{typeName: "ResourcesReadResult", fieldName: "contents"},
		{typeName: "PromptsListResult", fieldName: "prompts"},
		{typeName: "PromptInfo", fieldName: "arguments"},
		{typeName: "PromptsGetResult", fieldName: "messages"},
	} {
		attribute := expr.AsObject(testRootType(t, root, tc.typeName).Type).Attribute(tc.fieldName)
		require.True(t, expr.AsArray(attribute.Type).NonNullableElems, "%s.%s", tc.typeName, tc.fieldName)
	}
}

func TestPrepareServices_DoesNotBuildNotificationRequestSurface(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("calc", "add")
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
		},
	})

	require.NoError(t, prepareServices([]eval.Root{root}))
	for _, method := range root.Services[1].Methods {
		require.False(t, strings.HasPrefix(method.Name, "notify_"), method.Name)
	}
	for _, userType := range root.Types {
		require.NotEqual(t, "SendNotificationPayload", userType.Name())
	}
}

func TestBuildAdapterDataRejectsAnExampleThatCannotBeEncodedAsJSON(t *testing.T) {
	svc, methods := testService("calc", "add")
	methods["add"].Payload = &expr.AttributeExpr{
		Type: expr.String,
		UserExamples: []*expr.ExampleExpr{{
			Value: make(chan int),
		}},
	}
	mcp := &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
		},
	}

	_, err := newAdapterGenerator(testSchemaAPI(),
		svc,
		mcp,
	).buildAdapterData()

	require.ErrorContains(t, err, `build example for tool "add"`)
	require.ErrorContains(t, err, "unsupported type: chan int")
}

func TestStaticPromptsRenderWithoutAProvider(t *testing.T) {
	data := &AdapterData{
		PayloadRefs: testProtocolPayloadRefs(),
		MCPPackage:  "mcpassistant",
		StaticPrompts: []*StaticPromptAdapter{{
			Name:        "daily_report",
			Description: "Summarize the day",
			Messages: []*PromptMessageAdapter{{
				Role:    "user",
				Content: "Summarize today.",
			}},
		}},
	}

	files := generateMCPTransport("example.com/assistant/gen", &expr.ServiceExpr{Name: "assistant"}, data)
	require.Len(t, files, 1)
	for _, file := range files {
		require.NotEqual(t, "gen/mcp_assistant/prompt_provider.go", filepath.ToSlash(file.Path))
	}

	adapter := renderGeneratedFile(t, files[0])
	require.Contains(t, adapter, `case "daily_report":`)
	require.Contains(t, adapter, `Text: stringPtr("Summarize today.")`)
	require.NotContains(t, adapter, "PromptProvider")
	require.NotContains(t, adapter, "promptProvider")
}

func renderTemplateSection(t *testing.T, name string, data any) string {
	t.Helper()
	file := &gcodegen.File{SectionTemplates: []*gcodegen.SectionTemplate{{
		Name:   name,
		Source: mcpTemplates.Read(name),
		Data:   data,
		FuncMap: map[string]any{
			"comment": gcodegen.Comment,
			"quote":   func(value string) string { return fmt.Sprintf("%q", value) },
		},
	}}}
	return renderGeneratedFile(t, file)
}

func TestPrepareServices_RejectsResourcePayload(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "read_document")
	methods["read_document"].Payload = &expr.AttributeExpr{Type: expr.String}
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "documents", URI: "doc://list", MimeType: "application/json", Method: methods["read_document"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.Error(t, err)
	require.ErrorContains(t, err, "read_document")
	require.ErrorContains(t, err, "must not define a payload")
}

func TestPrepareServices_AcceptedMCPServiceAssignsEveryOriginalEndpoint(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService(
		"assistant",
		"analyze",
		"read_document",
	)
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "analyze", Method: methods["analyze"]},
		},
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "documents", URI: "doc://list", MimeType: "application/json", Method: methods["read_document"]},
		},
	}
	mcpexpr.Root.RegisterMCP(svc, mcp)

	require.NoError(t, prepareServices([]eval.Root{root}))
	resourcesRead := root.Services[1].Method("resources/read")
	require.NotNil(t, resourcesRead.Result)
	require.Same(t, resourcesRead.Result, resourcesRead.StreamingResult)
	require.False(t, resourcesRead.IsStreaming())
	require.False(t, resourcesRead.HasMixedResults())

	data, err := newAdapterGenerator(testSchemaAPI(),
		svc,
		mcp,
	).buildAdapterData()
	require.NoError(t, err)
	require.Equal(t, "analyze", data.Tools[0].userMethodName)
	require.Equal(t, "read_document", data.Resources[0].userMethodName)
	data.CodecImportPath = testCodecImportPath
	data.CodecPackage = testCodecPackage
	data.Tools[0].Codec = &MethodCodecData{
		PayloadDecode: "DecodeAnalyzePayload",
		ResultEncode:  testCodecPackage + ".EncodeAnalyzeResult",
	}
	data.Resources[0].Codec = &MethodCodecData{
		ResultEncode: testCodecPackage + ".EncodeReadDocumentResult",
	}
	data.EndpointsName = "Endpoints"
	data.Tools[0].Endpoint = &endpointMethodAdapter{CallName: "invokeMCPMethod0"}
	data.Resources[0].Endpoint = &endpointMethodAdapter{CallName: "invokeMCPMethod1"}

	data.NeedsServerCodec = true
	data.MCPPackage = "mcpassistant"
	data.serverImports = []*gcodegen.ImportSpec{
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "example.com/assistant/gen/assistant", Name: "assistant"},
		{Path: "goa.design/goa/v3/http", Name: "goahttp"},
		{Path: "goa.design/goa/v3/pkg", Name: "goa"},
		{Path: "net/url"},
		{Path: "strings"},
		{Path: data.CodecImportPath, Name: data.CodecPackage},
	}
	serverFiles := generateMCPTransport("example.com/assistant/gen", svc, data)
	require.NotEmpty(t, serverFiles)
	server := renderGeneratedFile(t, serverFiles[0])
	require.Contains(t, server, "\n\t\"strings\"\n")
	require.NotContains(t, server, "\n\t\"bytes\"\n")
	require.NotContains(t, server, "\n\t\"io\"\n")
	require.NotContains(t, server, "\n\t\"net/http\"\n")
	require.NotContains(t, server, "\n\t\"path\"\n")
	require.NotContains(t, server, "\n\t\"strconv\"\n")
}

func TestPrepareMCPMountsGeneratedServiceOnOriginalServers(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	alpha, alphaMethods := testService("alpha", "list")
	beta, _ := testService("beta", "status")
	root := &expr.RootExpr{
		Services: []*expr.ServiceExpr{alpha, beta},
		API: &expr.APIExpr{
			HTTP: &expr.HTTPExpr{},
			JSONRPC: &expr.JSONRPCExpr{
				HTTPExpr: expr.HTTPExpr{
					Services: []*expr.HTTPServiceExpr{
						jsonrpcService(alpha, "/rpc"),
						jsonrpcService(beta, "/rpc"),
					},
				},
			},
			Servers: []*expr.ServerExpr{
				{Name: "alpha-server", Services: []string{"alpha", "mcp_alpha"}},
				{Name: "beta-server", Services: []string{"beta"}},
			},
		},
	}
	mcpexpr.Root.RegisterMCP(alpha, &mcpexpr.MCPExpr{
		Name:    "alpha",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "list", Method: alphaMethods["list"]},
		},
	})

	_, err := prepareMCPServices([]eval.Root{root, mcpexpr.Root})

	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "mcp_alpha"}, root.API.Servers[0].Services)
	require.False(t, slices.Contains(root.API.Servers[1].Services, "mcp_alpha"))
}

func resetMCPCodegenState(t *testing.T) func() {
	t.Helper()

	previousRoot := mcpexpr.Root
	mcpexpr.Root = mcpexpr.NewRoot()

	return func() {
		mcpexpr.Root = previousRoot
	}
}

func testService(name string, methodNames ...string) (*expr.ServiceExpr, map[string]*expr.MethodExpr) {
	svc := &expr.ServiceExpr{Name: name}
	methods := make(map[string]*expr.MethodExpr, len(methodNames))
	for _, methodName := range methodNames {
		method := &expr.MethodExpr{
			Name:    methodName,
			Service: svc,
			Payload: &expr.AttributeExpr{Type: expr.Empty},
			Result:  &expr.AttributeExpr{Type: expr.String},
		}
		svc.Methods = append(svc.Methods, method)
		methods[methodName] = method
	}
	return svc, methods
}

func testRootType(t *testing.T, root *expr.RootExpr, name string) *expr.UserTypeExpr {
	t.Helper()
	for _, userType := range root.Types {
		if userType.Name() == name {
			return userType.(*expr.UserTypeExpr)
		}
	}
	t.Fatalf("generated type %q not found", name)
	return nil
}

func testRootExpr(services []*expr.ServiceExpr, jsonrpcServices []*expr.HTTPServiceExpr) *expr.RootExpr {
	servers := make([]*expr.ServerExpr, 0, len(services))
	for _, svc := range services {
		servers = append(servers, &expr.ServerExpr{
			Name:     svc.Name + "-server",
			Services: []string{svc.Name},
			Hosts: []*expr.HostExpr{{
				Name:      "test",
				URIs:      []expr.URIExpr{"http://localhost:8080"},
				Variables: &expr.AttributeExpr{Type: &expr.Object{}},
			}},
		})
	}
	return &expr.RootExpr{
		Services: services,
		API: &expr.APIExpr{
			HTTP: &expr.HTTPExpr{},
			JSONRPC: &expr.JSONRPCExpr{
				HTTPExpr: expr.HTTPExpr{Services: jsonrpcServices},
			},
			Servers: servers,
		},
	}
}

func jsonrpcService(svc *expr.ServiceExpr, path string) *expr.HTTPServiceExpr {
	return &expr.HTTPServiceExpr{
		ServiceExpr: svc,
		Root:        &expr.HTTPExpr{},
		JSONRPCRoute: &expr.RouteExpr{
			Method: http.MethodPost,
			Path:   path,
		},
	}
}

func renderGeneratedFile(t *testing.T, file *gcodegen.File) string {
	t.Helper()

	var output bytes.Buffer
	for _, section := range file.SectionTemplates {
		tmpl := template.New(section.Name).Funcs(template.FuncMap{
			"comment": gcodegen.Comment,
			"commandLine": func() string {
				return ""
			},
		})
		if section.FuncMap != nil {
			tmpl = tmpl.Funcs(section.FuncMap)
		}
		parsed, err := tmpl.Parse(section.Source)
		require.NoError(t, err)

		var rendered bytes.Buffer
		err = parsed.Execute(&rendered, section.Data)
		require.NoError(t, err)
		output.Write(rendered.Bytes())
	}

	require.NotEmpty(t, output.String(), filepath.ToSlash(file.Path))
	return output.String()
}

// prepareServices returns the error raised while adding MCP services to a test root.
func prepareServices(roots []eval.Root) error {
	_, err := prepareMCPServices(append(roots, mcpexpr.Root))
	return err
}

// testSchemaAPI gives isolated generator tests a deterministic example source.
func testSchemaAPI() *expr.APIExpr {
	return &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
}

// testProtocolPayloadRefs supplies the exact synthetic method types used by
// isolated template tests. Generated runtime tests resolve actual Goa layouts.
func testProtocolPayloadRefs() map[string]string {
	return map[string]string{
		"server/discover":          "*DiscoverPayload",
		"tools/list":               "*ToolsListPayload",
		"tools/call":               "*ToolsCallPayload",
		"resources/list":           "*ResourcesListPayload",
		"resources/read":           "*ResourcesReadPayload",
		"resources/templates/list": "*ResourceTemplatesListPayload",
		"prompts/list":             "*PromptsListPayload",
		"prompts/get":              "*PromptsGetPayload",
		"completion/complete":      "*CompletionCompletePayload",
		"subscriptions/listen":     "*SubscriptionsListenPayload",
	}
}
