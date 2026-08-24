package codegen

import (
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestPrepareServices_RejectsUnmappedMCPMethods(t *testing.T) {
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

	require.Error(t, err)
	require.ErrorContains(t, err, `service "calc"`)
	require.ErrorContains(t, err, "subtract")
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
	require.Equal(t, "/rpc", root.API.JSONRPC.Services[0].JSONRPCRoute.Path)
	mcpService := root.Services[1]
	toolsCall := mcpService.Method("tools/call")
	require.NotNil(t, toolsCall.Result)
	require.Same(t, toolsCall.Result, toolsCall.StreamingResult)
	require.False(t, toolsCall.IsStreaming())
	require.False(t, toolsCall.HasMixedResults())
	eventsStream := mcpService.Method("events/stream")
	require.NotNil(t, eventsStream.StreamingResult)
	require.Same(t, eventsStream.StreamingResult, eventsStream.Result)
	require.Equal(t, expr.ServerStreamKind, eventsStream.Stream)
	require.False(t, eventsStream.HasMixedResults())
}

func TestGenerateMCPClientAdapter_DoesNotRenderOriginalClientFallback(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("calc", "add")
	methods["add"].Result = &expr.AttributeExpr{Type: expr.Empty}
	mcp := &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
		nil,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	data.Tools[0].ServiceMethodName = "Add"
	data.clientMethodNames = []string{"Add"}
	setTestClientRenderNames(data, svc)
	files := generateMCPClientAdapter("example.com/calc/gen", svc, data)

	require.Len(t, files, 1)
	require.NotContains(t, renderGeneratedFile(t, files[0]), "origClient")
}

func TestGenerateMCPClientAdapter_RendersNotificationEndpoints(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "send_notification")
	methods["send_notification"].Payload = testNotificationPayload()
	methods["send_notification"].Result = &expr.AttributeExpr{Type: expr.Empty}
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Notifications: []*mcpexpr.NotificationExpr{
			{
				Name:   "status_update",
				Method: methods["send_notification"],
			},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
		nil,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	data.CodecImportPath = "example.com/assistant/gen/mcp_assistant/internal/codec"
	data.CodecPackage = "mcpcodec"
	data.NeedsClientCodec = true
	data.Notifications[0].ServiceMethodName = "SendNotification"
	data.Notifications[0].PayloadType = "*assistant.SendNotificationPayload"
	data.Notifications[0].MCPMethodName = "NotifyStatusUpdateEndpoint"
	data.Notifications[0].RequestBuilderName = "BuildNotifyStatusUpdateEndpointRequest"
	data.Notifications[0].ResponseDecoderName = "DecodeNotifyStatusUpdateEndpointResponse"
	data.Notifications[0].Codec = &MethodCodecData{PayloadEncode: "EncodeSendNotificationPayload"}
	data.clientMethodNames = []string{"SendNotification"}
	setTestClientRenderNames(data, svc)
	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)

	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])
	require.Contains(t, rendered, "e.SendNotification =")
	require.Contains(t, rendered, "BuildNotifyStatusUpdateEndpointRequest")
	require.Contains(t, rendered, "DecodeNotifyStatusUpdateEndpointResponse")
}

func TestPromptProviderUsesPlannedMethodNames(t *testing.T) {
	data := &AdapterData{
		StaticPrompts: []*StaticPromptAdapter{{
			Name:               "daily_report",
			ProviderMethodName: "GetDailyReportPrompt",
		}},
		DynamicPrompts: []*DynamicPromptAdapter{{
			Name:               "daily-report",
			ProviderMethodName: "GetDailyReportDynamicPrompt",
		}},
	}

	provider := renderTemplateSection(t, "prompt_provider", data)
	adapter := renderTemplateSection(t, "adapter_prompts", data)

	require.Contains(t, provider, "GetDailyReportPrompt(arguments json.RawMessage)")
	require.Contains(t, provider, "GetDailyReportDynamicPrompt(ctx context.Context, arguments json.RawMessage)")
	require.Contains(t, adapter, "a.promptProvider.GetDailyReportPrompt(p.Arguments)")
	require.Contains(t, adapter, "a.promptProvider.GetDailyReportDynamicPrompt(ctx, p.Arguments)")
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

func TestGenerateMCPClientAdapter_DecodesResourceResultsWithGeneratedCodec(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "read_document")
	methods["read_document"].Payload = testResourceQueryPayload()
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{
				Name:   "documents",
				URI:    "doc://list",
				Method: methods["read_document"],
			},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
		nil,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	data.CodecImportPath = "example.com/assistant/gen/mcp_assistant/internal/codec"
	data.CodecPackage = "mcpcodec"
	data.NeedsClientCodec = true
	data.Resources[0].ServiceMethodName = "ReadDocument"
	data.Resources[0].PayloadType = "*assistant.ReadDocumentPayload"
	data.Resources[0].Codec = &MethodCodecData{ResultDecode: "DecodeReadDocumentResult"}
	data.clientMethodNames = []string{"ReadDocument"}
	setTestClientRenderNames(data, svc)
	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)

	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])
	require.Contains(t, rendered, "mcpcodec.DecodeReadDocumentResult([]byte(*rr.Contents[0].Text))")
	require.NotContains(t, rendered, "/jsonrpc/assistant/client")
	require.NotContains(t, rendered, "origC")
}

func TestGenerateMCPClientAdapter_DecodesDynamicPromptResultsWithGeneratedCodec(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "generate_prompt")
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
	}
	mcpexpr.Root.RegisterMCP(svc, mcp)
	mcpexpr.Root.DynamicPrompts[svc.Name] = []*mcpexpr.DynamicPromptExpr{
		{Name: "assistant_prompt", Method: methods["generate_prompt"]},
	}
	prompts := mcpexpr.Root.DynamicPrompts[svc.Name]
	data, err := newAdapterGenerator(
		svc,
		mcp,
		prompts,
		newMCPExprBuilder(svc, mcp, prompts).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	data.CodecImportPath = "example.com/assistant/gen/mcp_assistant/internal/codec"
	data.CodecPackage = "mcpcodec"
	data.NeedsClientCodec = true
	data.DynamicPrompts[0].ServiceMethodName = "GeneratePrompt"
	data.DynamicPrompts[0].Codec = &MethodCodecData{ResultDecode: "DecodeGeneratePromptResult"}
	data.clientMethodNames = []string{"GeneratePrompt"}
	setTestClientRenderNames(data, svc)
	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)

	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])
	require.Contains(t, rendered, "mcpcodec.DecodeGeneratePromptResult([]byte(*r.Messages[0].Content.Text))")
	require.NotContains(t, rendered, "/jsonrpc/assistant/client")
	require.NotContains(t, rendered, "origC")
}

func TestGenerateMCPClientAdapter_SpecializesResourceQueryConstruction(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "read_document")
	methods["read_document"].Payload = testResourceQueryPayload()
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{
				Name:   "documents",
				URI:    "doc://list",
				Method: methods["read_document"],
			},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
		nil,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	data.CodecImportPath = "example.com/assistant/gen/mcp_assistant/internal/codec"
	data.CodecPackage = "mcpcodec"
	data.NeedsClientCodec = true
	data.Resources[0].ServiceMethodName = "ReadDocument"
	data.Resources[0].PayloadType = "*assistant.ReadDocumentPayload"
	data.Resources[0].Codec = &MethodCodecData{ResultDecode: "DecodeReadDocumentResult"}
	data.clientMethodNames = []string{"ReadDocument"}
	setTestResourceQuerySelectors(t, data.Resources[0])
	setTestClientRenderNames(data, svc)
	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)

	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])
	require.NotContains(t, rendered, "json.Unmarshal")
	require.NotContains(t, rendered, "map[string]any")
	require.NotContains(t, rendered, "sort.Strings")
	require.NotContains(t, rendered, "\"reflect\"")
	require.NotContains(t, rendered, "hasMCPQueryValue")
	require.NotContains(t, rendered, "encodeMCPQueryValue")
	require.Contains(t, rendered, "query := url.Values{}")
	require.Contains(t, rendered, `query.Add("cursor", string(payload.Cursor))`)
	require.Contains(t, rendered, "if payload.Offset != nil {")
	require.Contains(t, rendered, `query.Add("offset", strconv.FormatInt(int64(*payload.Offset), 10))`)
	require.Contains(t, rendered, "if payload.Limit != 0 {")
	require.Contains(t, rendered, `query.Add("limit", strconv.FormatUint(uint64(payload.Limit), 10))`)
	require.Contains(t, rendered, "if payload.Enabled != nil {")
	require.Contains(t, rendered, `query.Add("enabled", strconv.FormatBool(*payload.Enabled))`)
	require.Contains(t, rendered, "if payload.Ratio != nil {")
	require.Contains(t, rendered, `query.Add("ratio", strconv.FormatFloat(*payload.Ratio, 'g', -1, 64))`)
	require.Contains(t, rendered, "for _, value := range payload.Tags {")
	require.Contains(t, rendered, `query.Add("tags", string(value))`)
	require.Contains(t, rendered, `query.Add("tenant", string(payload.Tenant))`)
}

func TestPrepareServices_RejectsNonPostJSONRPCPath(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "analyze")
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcServiceWithMethod(svc, "/rpc", http.MethodGet),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "analyze", Method: methods["analyze"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.Error(t, err)
	require.ErrorContains(t, err, `service "assistant"`)
	require.ErrorContains(t, err, "JSONRPC")
	require.ErrorContains(t, err, "POST")
}

func TestPrepareServices_RejectsIncompatibleNotificationPayload(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "send_notification")
	methods["send_notification"].Payload = &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "status", Attribute: &expr.AttributeExpr{Type: expr.String}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"status"}},
	}
	methods["send_notification"].Result = &expr.AttributeExpr{Type: expr.Empty}
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Notifications: []*mcpexpr.NotificationExpr{
			{Name: "status_update", Method: methods["send_notification"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.Error(t, err)
	require.ErrorContains(t, err, "send_notification")
	require.ErrorContains(t, err, "notification payload")
}

func TestPrepareServices_RejectsResultBearingNotificationMethod(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "send_notification")
	methods["send_notification"].Payload = testNotificationPayload()
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Notifications: []*mcpexpr.NotificationExpr{
			{Name: "status_update", Method: methods["send_notification"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.Error(t, err)
	require.ErrorContains(t, err, "send_notification")
	require.ErrorContains(t, err, "must not declare a result")
}

func TestPrepareServices_RejectsUnsupportedResourceQueryFieldType(t *testing.T) {
	testCases := []struct {
		name      string
		fieldName string
		fieldType expr.DataType
	}{
		{
			name:      "map",
			fieldName: "filters",
			fieldType: &expr.Map{
				KeyType:  &expr.AttributeExpr{Type: expr.String},
				ElemType: &expr.AttributeExpr{Type: expr.String},
			},
		},
		{
			name:      "array any",
			fieldName: "nums",
			fieldType: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.Any}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			restore := resetMCPCodegenState(t)
			defer restore()

			svc, methods := testService("assistant", "read_document")
			methods["read_document"].Payload = &expr.AttributeExpr{
				Type: &expr.Object{
					{
						Name: tc.fieldName,
						Attribute: &expr.AttributeExpr{
							Type: tc.fieldType,
						},
					},
				},
			}
			root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
				jsonrpcService(svc, "/rpc"),
			})
			mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
				Name:    "assistant-mcp",
				Version: "1.0.0",
				Resources: []*mcpexpr.ResourceExpr{
					{Name: "documents", URI: "doc://list", Method: methods["read_document"]},
				},
			})

			err := prepareServices([]eval.Root{root})

			require.Error(t, err)
			require.ErrorContains(t, err, "read_document")
			require.ErrorContains(t, err, "resource query")
			require.ErrorContains(t, err, tc.fieldName)
		})
	}
}

func TestPrepareServices_RejectsResourcePayloadWithoutQueryableFields(t *testing.T) {
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
			{Name: "documents", URI: "doc://list", Method: methods["read_document"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.Error(t, err)
	require.ErrorContains(t, err, "read_document")
	require.ErrorContains(t, err, "resource query")
	require.ErrorContains(t, err, "at least one")
}

func TestPrepareServices_AcceptsNotificationPayloadInheritedFromBase(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "send_notification")
	methods["send_notification"].Result = &expr.AttributeExpr{Type: expr.Empty}
	basePayload := &expr.UserTypeExpr{
		TypeName: "NotificationBase",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "type", Attribute: &expr.AttributeExpr{Type: expr.String}},
				{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"type", "message"}},
		},
	}
	methods["send_notification"].Payload = &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "data", Attribute: &expr.AttributeExpr{Type: expr.Any}},
		},
		Bases: []expr.DataType{basePayload},
	}
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Notifications: []*mcpexpr.NotificationExpr{
			{Name: "status_update", Method: methods["send_notification"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.NoError(t, err)
}

func TestPrepareServices_AcceptsNotificationPayloadDirectFieldsOverBase(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "send_notification")
	methods["send_notification"].Result = &expr.AttributeExpr{Type: expr.Empty}
	basePayload := &expr.UserTypeExpr{
		TypeName: "NotificationBase",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "type", Attribute: &expr.AttributeExpr{Type: expr.Int}},
				{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"type", "message"}},
		},
	}
	methods["send_notification"].Payload = &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{Type: expr.String}},
			{Name: "data", Attribute: &expr.AttributeExpr{Type: expr.Any}},
		},
		Bases:      []expr.DataType{basePayload},
		Validation: &expr.ValidationExpr{Required: []string{"type"}},
	}
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Notifications: []*mcpexpr.NotificationExpr{
			{Name: "status_update", Method: methods["send_notification"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.NoError(t, err)
}

func TestPrepareServices_AcceptedPureMCPServiceAssignsEveryOriginalEndpoint(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService(
		"assistant",
		"analyze",
		"read_document",
		"generate_prompt",
		"send_notification",
	)
	methods["send_notification"].Payload = testNotificationPayload()
	methods["send_notification"].Result = &expr.AttributeExpr{Type: expr.Empty}
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
			{Name: "documents", URI: "doc://list", Method: methods["read_document"]},
		},
		Notifications: []*mcpexpr.NotificationExpr{
			{Name: "status_update", Method: methods["send_notification"]},
		},
	}
	mcpexpr.Root.RegisterMCP(svc, mcp)
	mcpexpr.Root.DynamicPrompts[svc.Name] = []*mcpexpr.DynamicPromptExpr{
		{Name: "assistant_prompt", Method: methods["generate_prompt"]},
	}
	prompts := mcpexpr.Root.DynamicPrompts[svc.Name]

	require.NoError(t, prepareServices([]eval.Root{root}))
	resourcesRead := root.Services[1].Method("resources/read")
	require.NotNil(t, resourcesRead.Result)
	require.Same(t, resourcesRead.Result, resourcesRead.StreamingResult)
	require.False(t, resourcesRead.IsStreaming())
	require.False(t, resourcesRead.HasMixedResults())

	data, err := newAdapterGenerator(
		svc,
		mcp,
		prompts,
		newMCPExprBuilder(svc, mcp, prompts).BuildServiceMapping(),
	).buildAdapterData()
	require.NoError(t, err)
	require.Equal(t, "analyze", data.Tools[0].userMethodName)
	require.Equal(t, "read_document", data.Resources[0].userMethodName)
	require.Equal(t, "generate_prompt", data.DynamicPrompts[0].userMethodName)
	require.Equal(t, "send_notification", data.Notifications[0].userMethodName)
	data.CodecImportPath = "example.com/assistant/gen/mcp_assistant/internal/codec"
	data.CodecPackage = "mcpcodec"
	data.NeedsClientCodec = true
	data.Tools[0].HasPayload = true
	data.Tools[0].PayloadType = "*assistant.AnalyzeRequest"
	data.Tools[0].Codec = &MethodCodecData{
		PayloadEncode: "EncodeAnalyzePayload",
		ResultEncode:  "EncodeAnalyzeResult",
		ResultDecode:  "DecodeAnalyzeResult",
	}
	data.Resources[0].HasPayload = true
	data.Resources[0].PayloadType = "*assistant.ReadDocumentRequest"
	data.Resources[0].Codec = &MethodCodecData{
		ResultEncode: "EncodeReadDocumentResult",
		ResultDecode: "DecodeReadDocumentResult",
	}
	data.DynamicPrompts[0].HasPayload = true
	data.DynamicPrompts[0].PayloadType = "*assistant.GeneratePromptRequest"
	data.DynamicPrompts[0].Codec = &MethodCodecData{
		PayloadEncode: "EncodeGeneratePromptPayload",
		ResultDecode:  "DecodeGeneratePromptResult",
	}
	data.Notifications[0].PayloadType = "*assistant.NotificationRequest"
	data.Notifications[0].Codec = &MethodCodecData{PayloadEncode: "EncodeSendNotificationPayload"}
	data.Notifications[0].MCPMethodName = "NotifyStatusUpdate"
	data.Notifications[0].PayloadRef = "*SendNotificationPayload"
	data.Tools[0].ServiceMethodName = "Analyze"
	data.Resources[0].ServiceMethodName = "ReadDocument"
	data.DynamicPrompts[0].ServiceMethodName = "GeneratePrompt"
	data.Notifications[0].ServiceMethodName = "SendNotification"
	data.clientMethodNames = []string{"Analyze", "ReadDocument", "GeneratePrompt", "SendNotification"}
	setTestClientRenderNames(data, svc)

	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)
	require.Len(t, files, 1)

	rendered := renderGeneratedFile(t, files[0])
	require.NotContains(t, rendered, "func encodeOriginalPayload(")
	require.NotContains(t, rendered, "func decodeOriginalJSONRPCResult(")
	require.NotContains(t, rendered, "reqArgs, _ :=")
	require.NotContains(t, rendered, "req3, _ :=")
	require.Contains(t, rendered, "e.Analyze =")
	require.Contains(t, rendered, "e.ReadDocument =")
	require.Contains(t, rendered, "e.GeneratePrompt =")
	require.Contains(t, rendered, "e.SendNotification =")
	require.Contains(t, rendered, "mcpcodec.EncodeAnalyzePayload(v.(*assistant.AnalyzeRequest))")
	require.Contains(t, rendered, "payload := v.(*assistant.ReadDocumentRequest)")
	require.Contains(t, rendered, "mcpcodec.EncodeGeneratePromptPayload(v.(*assistant.GeneratePromptRequest))")
	require.Contains(t, rendered, "mcpcodec.EncodeSendNotificationPayload(v.(*assistant.NotificationRequest))")
	require.NotContains(t, rendered, "\n\t\"bytes\"\n")
	require.NotContains(t, rendered, "\n\t\"io\"\n")
	require.NotContains(t, rendered, "/jsonrpc/assistant/client")

	data.NeedsServerCodec = true
	data.MCPPackage = "mcpassistant"
	data.serverImports = []*gcodegen.ImportSpec{
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "sync"},
		{Path: "example.com/assistant/gen/assistant", Name: "assistant"},
		{Path: "goa.design/goa-ai/runtime/mcp", Name: "mcpruntime"},
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
	require.Contains(t, server, "func (a *MCPAdapter) NotifyStatusUpdate(ctx context.Context, p *SendNotificationPayload) error")
	require.Contains(t, server, "notification := &mcpruntime.Notification{")
}

func TestPrepareServices_FailsWhenOriginalServiceHasNoJSONRPCPath(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "analyze")
	root := testRootExpr([]*expr.ServiceExpr{svc}, nil)
	mcpexpr.Root.RegisterMCP(svc, &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "analyze", Method: methods["analyze"]},
		},
	})

	err := prepareServices([]eval.Root{root})

	require.Error(t, err)
	require.ErrorContains(t, err, `service "assistant" must declare JSONRPC`)
}

func TestPrepareServices_RejectsUnsupportedPureMCPMethodKinds(t *testing.T) {
	testCases := []struct {
		name string
		mcp  *mcpexpr.MCPExpr
	}{
		{
			name: "subscription",
			mcp: &mcpexpr.MCPExpr{
				Name:    "watcher",
				Version: "1.0.0",
				Subscriptions: []*mcpexpr.SubscriptionExpr{
					{
						ResourceName: "documents",
					},
				},
			},
		},
		{
			name: "subscription monitor",
			mcp: &mcpexpr.MCPExpr{
				Name:    "watcher",
				Version: "1.0.0",
				SubscriptionMonitors: []*mcpexpr.SubscriptionMonitorExpr{
					{
						Name: "events_stream",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			restore := resetMCPCodegenState(t)
			defer restore()

			svc, methods := testService("watcher", "watch_documents")
			switch tc.name {
			case "subscription":
				tc.mcp.Subscriptions[0].Method = methods["watch_documents"]
			case "subscription monitor":
				tc.mcp.SubscriptionMonitors[0].Method = methods["watch_documents"]
			}

			root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
				jsonrpcService(svc, "/rpc"),
			})
			mcpexpr.Root.RegisterMCP(svc, tc.mcp)

			err := prepareServices([]eval.Root{root})

			require.Error(t, err)
			require.ErrorContains(t, err, `service "watcher"`)
			require.ErrorContains(t, err, "watch_documents")
		})
	}
}

func TestPrepareMCPMountsGeneratedServiceOnOriginalServers(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	alpha, alphaMethods := testService("alpha", "list")
	beta, _ := testService("beta", "status")
	root := &expr.RootExpr{
		Services: []*expr.ServiceExpr{alpha, beta},
		API: &expr.APIExpr{
			HTTP: &expr.HTTPExpr{
				Services: []*expr.HTTPServiceExpr{
					httpService(alpha),
					httpService(beta),
				},
			},
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

func testNotificationPayload() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{Type: expr.String}},
			{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String}},
			{Name: "data", Attribute: &expr.AttributeExpr{Type: expr.Any}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"type"}},
	}
}

func testResourceQueryPayload() *expr.AttributeExpr {
	baseQuery := &expr.UserTypeExpr{
		TypeName: "ResourceQueryBase",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "tenant", Attribute: &expr.AttributeExpr{Type: expr.String}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"tenant"}},
		},
	}
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "cursor", Attribute: &expr.AttributeExpr{Type: expr.String}},
			{Name: "offset", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			{Name: "limit", Attribute: &expr.AttributeExpr{Type: expr.UInt, DefaultValue: 25}},
			{Name: "enabled", Attribute: &expr.AttributeExpr{Type: expr.Boolean}},
			{Name: "ratio", Attribute: &expr.AttributeExpr{Type: expr.Float64}},
			{
				Name: "tags",
				Attribute: &expr.AttributeExpr{
					Type: &expr.Array{
						ElemType: &expr.AttributeExpr{Type: expr.String},
					},
				},
			},
		},
		Bases:      []expr.DataType{baseQuery},
		Validation: &expr.ValidationExpr{Required: []string{"cursor"}},
	}
}

func testRootExpr(services []*expr.ServiceExpr, jsonrpcServices []*expr.HTTPServiceExpr) *expr.RootExpr {
	httpServices := make([]*expr.HTTPServiceExpr, 0, len(services))
	servers := make([]*expr.ServerExpr, 0, len(services))
	for _, svc := range services {
		httpServices = append(httpServices, httpService(svc))
		servers = append(servers, &expr.ServerExpr{
			Name:     svc.Name + "-server",
			Services: []string{svc.Name},
		})
	}
	return &expr.RootExpr{
		Services: services,
		API: &expr.APIExpr{
			HTTP: &expr.HTTPExpr{Services: httpServices},
			JSONRPC: &expr.JSONRPCExpr{
				HTTPExpr: expr.HTTPExpr{Services: jsonrpcServices},
			},
			Servers: servers,
		},
	}
}

func httpService(svc *expr.ServiceExpr) *expr.HTTPServiceExpr {
	return &expr.HTTPServiceExpr{ServiceExpr: svc}
}

func jsonrpcService(svc *expr.ServiceExpr, path string) *expr.HTTPServiceExpr {
	return jsonrpcServiceWithMethod(svc, path, http.MethodPost)
}

func jsonrpcServiceWithMethod(svc *expr.ServiceExpr, path string, method string) *expr.HTTPServiceExpr {
	return &expr.HTTPServiceExpr{
		ServiceExpr: svc,
		JSONRPCRoute: &expr.RouteExpr{
			Method: method,
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

func setTestClientRenderNames(data *AdapterData, service *expr.ServiceExpr) {
	serviceName := gcodegen.SnakeCase(service.Name)
	mcpPackage := gcodegen.Goify("mcp_"+serviceName, false)
	data.clientPackageName = mcpPackage + "adapter"
	data.clientServicePackage = serviceName
	data.clientMCPPackage = mcpPackage
	data.clientJSONRPCPackage = mcpPackage + "jsonrpcc"
	data.clientCodecPackage = data.CodecPackage
}

// setTestResourceQuerySelectors supplies the service-plan facts normally added
// by the plugin before it renders the client adapter in this direct unit test.
func setTestResourceQuerySelectors(t *testing.T, resource *ResourceAdapter) {
	t.Helper()
	selectors := map[string]struct {
		name    string
		pointer bool
	}{
		"cursor":  {name: "Cursor"},
		"enabled": {name: "Enabled", pointer: true},
		"limit":   {name: "Limit"},
		"offset":  {name: "Offset", pointer: true},
		"ratio":   {name: "Ratio", pointer: true},
		"tags":    {name: "Tags"},
		"tenant":  {name: "Tenant"},
	}
	for _, field := range resource.QueryFields {
		selector, ok := selectors[field.QueryKey]
		require.True(t, ok, "missing selector for query field %q", field.QueryKey)
		field.ClientSelector = selector.name
		field.ClientPointer = selector.pointer
	}
}

// prepareServices returns the error raised while adding MCP services to a test root.
func prepareServices(roots []eval.Root) error {
	_, err := prepareMCPServices(append(roots, mcpexpr.Root))
	return err
}
