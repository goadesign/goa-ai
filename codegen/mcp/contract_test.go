package codegen

import (
	"bytes"
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

	err := PrepareServices("", []eval.Root{root})

	require.Error(t, err)
	require.ErrorContains(t, err, `service "calc"`)
	require.ErrorContains(t, err, "subtract")
}

func TestGenerate_RejectsUnmappedPureMCPMethodsWithoutPrepareServices(t *testing.T) {
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

	_, err := Generate("example.com/calc/gen", []eval.Root{root}, nil)

	require.Error(t, err)
	require.ErrorContains(t, err, `service "calc"`)
	require.ErrorContains(t, err, "subtract")
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
		"example.com/calc/gen",
		svc,
		mcp,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
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
		"example.com/assistant/gen",
		svc,
		mcp,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)

	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])
	require.Contains(t, rendered, "e.SendNotification =")
	require.Contains(t, rendered, "NotifyStatusUpdate")
	require.NotContains(t, rendered, "NotifyNotifyStatusUpdate")
	require.Contains(t, rendered, "notificationPayload := &")
	require.Contains(t, rendered, "SendNotificationPayload{")
	require.Contains(t, rendered, "notificationPayload.Message =")
}

func TestGenerateMCPClientAdapter_RendersOriginalClientForResourceResults(t *testing.T) {
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
		"example.com/assistant/gen",
		svc,
		mcp,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)

	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])
	require.Contains(t, rendered, "origC :=")
	require.Contains(t, rendered, "origC.BuildReadDocumentRequest")
}

func TestGenerateMCPClientAdapter_RendersOriginalClientForDynamicPrompts(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "generate_prompt")
	root := testRootExpr([]*expr.ServiceExpr{svc}, []*expr.HTTPServiceExpr{
		jsonrpcService(svc, "/rpc"),
	})
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
	}
	mcpexpr.Root.RegisterMCP(svc, mcp)
	mcpexpr.Root.DynamicPrompts[svc.Name] = []*mcpexpr.DynamicPromptExpr{
		{Name: "assistant_prompt", Method: methods["generate_prompt"]},
	}
	data, err := newAdapterGenerator(
		"example.com/assistant/gen",
		svc,
		mcp,
		newMCPExprBuilder(svc, mcp, collectSourceSnapshot([]eval.Root{root})).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)

	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])
	require.Contains(t, rendered, "origC :=")
	require.Contains(t, rendered, "origC.BuildGeneratePromptRequest")
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
		"example.com/assistant/gen",
		svc,
		mcp,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()

	require.NoError(t, err)
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
	require.Contains(t, rendered, `query.Add("cursor", payload.Cursor)`)
	require.Contains(t, rendered, "if payload.Offset != nil {")
	require.Contains(t, rendered, `query.Add("offset", strconv.FormatInt(int64(*payload.Offset), 10))`)
	require.Contains(t, rendered, "if payload.Limit != 0 {")
	require.Contains(t, rendered, `query.Add("limit", strconv.FormatUint(uint64(payload.Limit), 10))`)
	require.Contains(t, rendered, "if payload.Enabled != nil {")
	require.Contains(t, rendered, `query.Add("enabled", strconv.FormatBool(*payload.Enabled))`)
	require.Contains(t, rendered, "if payload.Ratio != nil {")
	require.Contains(t, rendered, `query.Add("ratio", strconv.FormatFloat(*payload.Ratio, 'g', -1, 64))`)
	require.Contains(t, rendered, "for _, value := range payload.Tags {")
	require.Contains(t, rendered, `query.Add("tags", value)`)
	require.Contains(t, rendered, `query.Add("tenant", payload.Tenant)`)
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

	err := PrepareServices("", []eval.Root{root})

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

	err := PrepareServices("", []eval.Root{root})

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

	err := PrepareServices("", []eval.Root{root})

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

			err := PrepareServices("", []eval.Root{root})

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

	err := PrepareServices("", []eval.Root{root})

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

	err := PrepareServices("", []eval.Root{root})

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

	err := PrepareServices("", []eval.Root{root})

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

	require.NoError(t, PrepareServices("", []eval.Root{root}))

	data, err := newAdapterGenerator(
		"example.com/assistant/gen",
		svc,
		mcp,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()
	require.NoError(t, err)

	files := generateMCPClientAdapter("example.com/assistant/gen", svc, data)
	require.Len(t, files, 1)

	rendered := renderGeneratedFile(t, files[0])
	require.Contains(t, rendered, "func encodeOriginalPayload(")
	require.Contains(t, rendered, "func decodeOriginalJSONRPCResult(")
	require.NotContains(t, rendered, "reqArgs, _ :=")
	require.NotContains(t, rendered, "req3, _ :=")
	require.Contains(t, rendered, "e.Analyze =")
	require.Contains(t, rendered, "e.ReadDocument =")
	require.Contains(t, rendered, "e.GeneratePrompt =")
	require.Contains(t, rendered, "e.SendNotification =")
}

func TestGenerate_FailsWhenOriginalServiceHasNoJSONRPCPath(t *testing.T) {
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

	_, err := Generate("example.com/assistant/gen", []eval.Root{root}, nil)

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

			err := PrepareServices("", []eval.Root{root})

			require.Error(t, err)
			require.ErrorContains(t, err, `service "watcher"`)
			require.ErrorContains(t, err, "watch_documents")
		})
	}
}

func TestPrepareExample_OnlyMountsMCPOnOriginalServers(t *testing.T) {
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
				{Name: "alpha-server", Services: []string{"alpha"}},
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

	err := PrepareExample("", []eval.Root{root})

	require.NoError(t, err)
	require.True(t, slices.Contains(root.API.Servers[0].Services, "mcp_alpha"))
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
