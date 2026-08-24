// This file checks that Goa core and the MCP plugin write one attached service
// from the same generation run.
package codegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goagenerator "goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

const resourceQueryGeneratedTestSource = `package mcpresources

import (
	"context"
	"strings"
	"testing"

	resources "generated.local/gen/resources"
)

type resourceQueryService struct {
	payload *resources.ReadDocumentPayload
}

func (s *resourceQueryService) ReadDocument(_ context.Context, payload *resources.ReadDocumentPayload) (string, error) {
	s.payload = payload
	return "ok", nil
}

func TestResourceQueryContract(t *testing.T) {
	service := new(resourceQueryService)
	adapter := NewMCPAdapter(service, &MCPAdapterOptions{AllowedResourceURIs: []string{"doc://list"}})
	if _, err := adapter.Initialize(context.Background(), &InitializePayload{ProtocolVersion: DefaultProtocolVersion}); err != nil {
		t.Fatalf("initialize adapter: %v", err)
	}
	tests := []struct {
		name string
		uri string
		want string
	}{
		{"missing required scalar", "doc://list?offset=1", "cursor"},
		{"validation failure", "doc://list?cursor=x", "length"},
		{"duplicate scalar", "doc://list?cursor=ok&cursor=again", "exactly one value"},
		{"unknown key", "doc://list?cursor=ok&other=value", "unknown query parameter"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.ResourcesRead(context.Background(), &ResourcesReadPayload{URI: test.uri})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResourcesRead() error = %v, want text %q", err, test.want)
			}
		})
	}

	_, err := adapter.ResourcesRead(context.Background(), &ResourcesReadPayload{
		URI: "doc://list?cursor=ok&offset=3&category=primary&tags=one&tags=two&aliases=first&aliases=second",
	})
	if err != nil {
		t.Fatalf("read valid resource: %v", err)
	}
	if service.payload == nil {
		t.Fatal("service did not receive the resource payload")
	}
	if service.payload.OriginalCursor != "ok" || service.payload.Offset == nil || *service.payload.Offset != 3 {
		t.Fatalf("scalar payload = %#v", service.payload)
	}
	if service.payload.Limit != 25 {
		t.Fatalf("defaulted limit = %d, want 25", service.payload.Limit)
	}
	if service.payload.Category == nil || *service.payload.Category != "primary" {
		t.Fatalf("alias category = %#v", service.payload.Category)
	}
	if len(service.payload.Tags) != 2 || service.payload.Tags[0] != "one" || service.payload.Tags[1] != "two" {
		t.Fatalf("primitive array = %#v", service.payload.Tags)
	}
	if len(service.payload.Aliases) != 2 || service.payload.Aliases[0] != "first" || service.payload.Aliases[1] != "second" {
		t.Fatalf("alias array = %#v", service.payload.Aliases)
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
	fmtMethods["echo"].Payload = &expr.AttributeExpr{Type: expr.String}
	prompts, promptMethods := testService("prompts", "daily_prompt")
	staticPrompts, _ := testService("static_prompts")
	notifications, notificationMethods := testService("notifications", "send_update", "send+update")
	for _, method := range notificationMethods {
		method.Payload = testNotificationPayload()
		method.Result = &expr.AttributeExpr{Type: expr.Empty}
	}
	resources, resourceMethods := testService("resources", "read_document")
	resourceMethods["read_document"].Payload = testResourceSelectorPayload()
	root := testRootExpr([]*expr.ServiceExpr{service, formatter, selector, contextService, fmtService, prompts, staticPrompts, notifications, resources}, []*expr.HTTPServiceExpr{
		jsonrpcService(service, "/calc"),
		jsonrpcService(formatter, "/formatter"),
		jsonrpcService(selector, "/selector"),
		jsonrpcService(contextService, "/context"),
		jsonrpcService(fmtService, "/fmt"),
		jsonrpcService(prompts, "/prompts"),
		jsonrpcService(staticPrompts, "/static-prompts"),
		jsonrpcService(notifications, "/notifications"),
		jsonrpcService(resources, "/resources"),
	})
	root.API.Name = "calc"
	root.API.Version = "1.0"
	root.API.GRPC = &expr.GRPCExpr{}
	root.API.RandomizerFactory = expr.NewDeterministicRandomizerFactory()
	root.Types = append(namedTypes, locatedRenderPayload)
	for _, current := range []*expr.ServiceExpr{service, formatter, selector, contextService, fmtService, prompts, staticPrompts, notifications, resources} {
		for _, method := range current.Methods {
			method.Prepare()
		}
	}
	expr.Root = root
	eval.Reset()
	require.NoError(t, eval.Register(root))
	require.NoError(t, eval.Register(mcpexpr.Root))
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
			{Name: "echo", Method: fmtMethods["echo"]},
		},
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
	mcpexpr.Root.DynamicPrompts[prompts.Name] = []*mcpexpr.DynamicPromptExpr{{
		Name:   "daily-report",
		Method: promptMethods["daily_prompt"],
	}}
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
	mcpexpr.Root.RegisterMCP(notifications, &mcpexpr.MCPExpr{
		Name:    "notifications",
		Version: "1.0.0",
		Notifications: []*mcpexpr.NotificationExpr{
			{Name: "status_update", Method: notificationMethods["send_update"]},
			{Name: "status+update", Method: notificationMethods["send+update"]},
		},
	})
	mcpexpr.Root.RegisterMCP(resources, &mcpexpr.MCPExpr{
		Name:    "resources",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "documents", URI: "doc://list", Method: resourceMethods["read_document"]},
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
	serviceSource, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_calc", "service.go"))
	require.NoError(t, err)
	adapterSource, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_calc", "adapter_server.go"))
	require.NoError(t, err)
	require.Contains(t, string(serviceSource), "package mcpcalc")
	require.Contains(t, string(adapterSource), "package mcpcalc")
	codec, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_calc", "internal", "codec", "codec.go"))
	require.NoError(t, err)
	require.Contains(t, string(codec), "func EncodeAddPayload(")
	require.Contains(t, string(codec), "func DecodeAddPayload(")
	require.Contains(t, string(codec), "func EncodeAddResult(")
	require.Contains(t, string(codec), "func DecodeAddResult(")
	require.Contains(t, string(codec), "func EncodeAddPayload(")
	require.Contains(t, string(codec), "func DecodeAddPayload(")
	require.Contains(t, string(codec), "func EncodeAddResult(in *calc.CalculationResponse)")
	require.Contains(t, string(codec), "func DecodeAddResult(data []byte) (out *calc.CalculationResponse, err error)")
	require.Contains(t, string(codec), "CalculationRequest")
	require.Contains(t, string(codec), "Operand")
	require.Contains(t, string(codec), "v *calc.Calculation")
	require.Contains(t, string(codec), "func EncodeSubtractPayload(")
	require.Contains(t, string(codec), "func DecodeSubtractPayload(")
	adapterServer, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_calc", "adapter_server.go"))
	require.NoError(t, err)
	require.Contains(t, string(adapterServer), "mcpcodec.DecodeAddPayload(arguments)")
	require.Contains(t, string(adapterServer), "mcpcodec.EncodeAddResult(result)")
	require.NotContains(t, string(adapterServer), "\n\t\"bytes\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"io\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"net/http\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"strings\"\n")
	adapterClient, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_calc", "adapter", "client", "adapter.go"))
	require.NoError(t, err)
	require.Contains(t, string(adapterClient), `shared "generated.local/gen/alpha/shared"`)
	require.Contains(t, string(adapterClient), `shared2 "generated.local/gen/zeta/shared"`)
	require.Contains(t, string(adapterClient), "mcpcodec.EncodeAddPayload(v.(*shared.CalculationRequest))")
	require.Contains(t, string(adapterClient), "mcpcodec.EncodeSubtractPayload(v.(*shared2.SubtractRequest))")
	require.Contains(t, string(adapterClient), "mcpcodec.DecodeAddResult([]byte(*r.Content[0].Text))")
	require.NotContains(t, string(adapterClient), "\n\t\"bytes\"\n")
	require.NotContains(t, string(adapterClient), "\n\t\"io\"\n")
	require.NotContains(t, string(adapterClient), "/jsonrpc/calc/client")
	_, err = os.Stat(filepath.Join(dir, "gen", "jsonrpc", "calc", "client"))
	require.ErrorIs(t, err, os.ErrNotExist)
	server, err := os.ReadFile(filepath.Join(dir, "gen", "jsonrpc", "mcp_calc", "server", "server.go"))
	require.NoError(t, err)
	require.Contains(t, string(server), "withMCPPolicyHeaders(h.ServeHTTP)")
	formatterCodec, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_formatter", "internal", "codec", "codec.go"))
	require.NoError(t, err)
	require.Contains(t, string(formatterCodec), "func EncodeRenderPayload(")
	require.Contains(t, string(formatterCodec), "func DecodeRenderPayload(")
	require.Contains(t, string(formatterCodec), "func EncodeRenderResult(")
	require.Contains(t, string(formatterCodec), "func DecodeRenderResult(")
	formatterServer, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_formatter", "adapter_server.go"))
	require.NoError(t, err)
	require.Contains(t, string(formatterServer), "mcpcodec.DecodeRenderPayload(arguments)")
	require.Contains(t, string(formatterServer), "mcpcodec.EncodeRenderResult(result)")
	formatterClient, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_formatter", "adapter", "client", "adapter.go"))
	require.NoError(t, err)
	require.Contains(t, string(formatterClient), `mcpcodec2 "generated.local/gen/mcpcodec"`)
	require.Contains(t, string(formatterClient), "mcpcodec.EncodeRenderPayload(v.(*mcpcodec2.RenderPayload))")
	require.Contains(t, string(formatterClient), "mcpcodec.DecodeRenderResult([]byte(*r.Content[0].Text))")
	promptProvider, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_prompts", "prompt_provider.go"))
	require.NoError(t, err)
	require.Contains(t, string(promptProvider), "GetDailyReportPrompt(ctx context.Context")
	require.Contains(t, string(promptProvider), "GetDailyReportPromptStatic(arguments json.RawMessage)")
	promptServer, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_prompts", "adapter_server.go"))
	require.NoError(t, err)
	require.Contains(t, string(promptServer), "a.promptProvider.GetDailyReportPrompt(ctx, p.Arguments)")
	require.Contains(t, string(promptServer), "a.promptProvider.GetDailyReportPromptStatic(p.Arguments)")
	staticProvider, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_static_prompts", "prompt_provider.go"))
	require.NoError(t, err)
	require.NotContains(t, string(staticProvider), `"context"`)
	require.NotContains(t, string(staticProvider), `generated.local/gen/static_prompts`)
	staticClient, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_static_prompts", "adapter", "client", "adapter.go"))
	require.NoError(t, err)
	require.NotContains(t, string(staticClient), `"context"`)
	require.NotContains(t, string(staticClient), `/mcp_static_prompts"`)
	require.NotContains(t, string(staticClient), `/jsonrpc/mcp_static_prompts/client"`)
	fmtClient, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_fmt", "adapter", "client", "adapter.go"))
	require.NoError(t, err)
	require.Contains(t, string(fmtClient), `fmt_ "generated.local/gen/fmt_"`)
	require.Contains(t, string(fmtClient), "mcpcodec.EncodeEchoPayload(v.(string))")
	notificationClient, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_notifications", "adapter", "client", "adapter.go"))
	require.NoError(t, err)
	require.Contains(t, string(notificationClient), "BuildNotifyStatusUpdateRequest(ctx, nil)")
	require.Contains(t, string(notificationClient), "BuildNotifyStatusUpdateEndpointRequest(ctx, nil)")
	require.Contains(t, string(notificationClient), "DecodeNotifyStatusUpdateResponse(dec, false)")
	require.Contains(t, string(notificationClient), "DecodeNotifyStatusUpdateEndpointResponse(dec, false)")
	selectorService, err := os.ReadFile(filepath.Join(dir, "gen", "selector", "service.go"))
	require.NoError(t, err)
	require.Contains(t, string(selectorService), "ReadValue(context.Context) (res string, err error)")
	require.Contains(t, string(selectorService), "ReadValueEndpoint(context.Context) (res string, err error)")
	selectorServer, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_selector", "adapter_server.go"))
	require.NoError(t, err)
	require.Contains(t, string(selectorServer), "a.service.ReadValue(ctx)")
	require.Contains(t, string(selectorServer), "a.service.ReadValueEndpoint(ctx)")
	selectorClient, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_selector", "adapter", "client", "adapter.go"))
	require.NoError(t, err)
	require.Contains(t, string(selectorClient), "e.ReadValue =")
	require.Contains(t, string(selectorClient), "e.ReadValueEndpoint =")
	require.Contains(t, string(selectorClient), "e.ReadValue,\n\t\te.ReadValueEndpoint,")
	selectorJSONRPCStream, err := os.ReadFile(filepath.Join(dir, "gen", "jsonrpc", "mcp_selector", "client", "stream.go"))
	require.NoError(t, err)
	require.Contains(t, string(selectorJSONRPCStream), `mcpselector "generated.local/gen/mcp_selector"`)
	selectorCaller, err := os.ReadFile(filepath.Join(dir, "gen", "jsonrpc", "mcp_selector", "client", "caller.go"))
	require.NoError(t, err)
	require.Contains(t, string(selectorCaller), `mcpselector "generated.local/gen/mcp_selector"`)
	resourceClient, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_resources", "adapter", "client", "adapter.go"))
	require.NoError(t, err)
	require.Contains(t, string(resourceClient), `query.Add("cursor", string(payload.OriginalCursor))`)
	require.Contains(t, string(resourceClient), "if payload.Offset != nil {")
	require.NotContains(t, string(resourceClient), "payload.Cursor")
	resourceServer, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_resources", "adapter_server.go"))
	require.NoError(t, err)
	require.Contains(t, string(resourceServer), "transport := new(mcpcodec.ReadDocumentPayloadTransport)")
	require.Contains(t, string(resourceServer), "transport.OriginalCursor = &converted")
	require.Contains(t, string(resourceServer), "transport.Limit = &converted")
	require.Contains(t, string(resourceServer), "transport.Category = &converted")
	require.Contains(t, string(resourceServer), "parsed := make([]*string, len(values))")
	require.Contains(t, string(resourceServer), "parsed := make([]*mcpcodec.ReadDocumentPayloadResourceAliasTransport, len(values))")
	require.Contains(t, string(resourceServer), "payload, err := mcpcodec.NewReadDocumentPayload(transport)")
	require.NotContains(t, string(resourceServer), "Field0")
	require.NotContains(t, string(resourceServer), "json.Marshal(&arguments)")
	resourceCodec, err := os.ReadFile(filepath.Join(dir, "gen", "mcp_resources", "internal", "codec", "codec.go"))
	require.NoError(t, err)
	require.Contains(t, string(resourceCodec), "func NewReadDocumentPayload(body *ReadDocumentPayloadTransport)")
	require.NotContains(t, string(resourceCodec), "func DecodeReadDocumentPayload(data []byte)")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "gen", "mcp_resources", "resource_query_test.go"),
		[]byte(resourceQueryGeneratedTestSource),
		0o600,
	))
	command := exec.Command("go", "test", "-mod=mod", "./gen/...")
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

// testResourceSelectorPayload gives one query field a Go selector that differs
// from its JSON name, so the generated MCP client must use Goa's chosen field.
func testResourceSelectorPayload() *expr.AttributeExpr {
	minLength := 2
	alias := &expr.UserTypeExpr{
		TypeName:      "ResourceAlias",
		UID:           "mcp-integration-resource-alias",
		AttributeExpr: &expr.AttributeExpr{Type: expr.String},
	}
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{
				Name: "cursor",
				Attribute: &expr.AttributeExpr{
					Type:       expr.String,
					Validation: &expr.ValidationExpr{MinLength: &minLength},
					Meta:       expr.MetaExpr{"struct:field:name": {"OriginalCursor"}},
				},
			},
			{Name: "offset", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			{Name: "limit", Attribute: &expr.AttributeExpr{Type: expr.UInt, DefaultValue: uint(25)}},
			{Name: "category", Attribute: &expr.AttributeExpr{Type: alias}},
			{Name: "tags", Attribute: &expr.AttributeExpr{Type: &expr.Array{
				ElemType:         &expr.AttributeExpr{Type: expr.String},
				NonNullableElems: true,
			}}},
			{Name: "aliases", Attribute: &expr.AttributeExpr{Type: &expr.Array{
				ElemType:         &expr.AttributeExpr{Type: alias},
				NonNullableElems: true,
			}}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"cursor"}},
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
	return []expr.UserType{operand, request, subtrahend, subtractRequest, calculation, response}
}

// testMCPToolPayload returns the object shape that travels from an MCP client
// to an attached Goa service.
func testMCPToolPayload() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"value"}},
	}
}

// testModuleDirectory returns the local module selected by the workspace that
// compiled this test.
func testModuleDirectory(t *testing.T, module string) string {
	t.Helper()
	args := []string{"list", "-m", "-f", "{{.Dir}}", module}
	command := exec.Command("go", args...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.TrimSpace(string(output))
}
