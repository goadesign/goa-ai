package codegen

import (
	"goa.design/goa/v3/expr"
	mcpexpr "goa.design/plugins/v3/mcp/expr"
)

// buildMethods creates all MCP protocol methods
func (b *mcpExprBuilder) buildMethods() []*expr.MethodExpr {
	methods := make([]*expr.MethodExpr, 0, 10)
	methods = append(methods,
		// Core protocol methods
		b.buildInitializeMethod(),
		b.buildPingMethod(),
	)

	// Add tool methods if tools are defined
	if len(b.mcp.Tools) > 0 {
		methods = append(methods, b.buildToolsListMethod(), b.buildToolsCallMethod())
	}

	// Add resource methods if resources are defined
	if len(b.mcp.Resources) > 0 {
		methods = append(methods,
			b.buildResourcesListMethod(),
			b.buildResourcesReadMethod(),
			b.buildResourcesSubscribeMethod(),
			b.buildResourcesUnsubscribeMethod(),
		)
	}

	// Add prompt methods if prompts are defined
	if b.hasPrompts() {
		methods = append(methods, b.buildPromptsListMethod(), b.buildPromptsGetMethod())
	}

	// Client-side features (sampling, roots) removed from server plugin

	// Add notification methods if defined
	if len(b.mcp.Notifications) > 0 {
		methods = append(methods, b.buildNotificationMethods()...)
	}

	// Add subscription methods if defined
	if len(b.mcp.Subscriptions) > 0 {
		methods = append(methods, b.buildSubscriptionMethods()...)
	}

	return methods
}

// buildInitializeMethod creates the initialize method
func (b *mcpExprBuilder) buildInitializeMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "initialize",
		Description: "Initialize MCP session",
		Payload:     b.getOrCreateType("InitializePayload", b.buildInitializePayloadType).AttributeExpr,
		Result:      b.getOrCreateType("InitializeResult", b.buildInitializeResultType).AttributeExpr,
	}
}

// buildPingMethod creates the ping method
func (b *mcpExprBuilder) buildPingMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "ping",
		Description: "Ping the server",
		Result:      b.getOrCreateType("PingResult", b.buildPingResultType).AttributeExpr,
	}
}

// buildToolsListMethod creates the tools/list method
func (b *mcpExprBuilder) buildToolsListMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "tools/list",
		Description: "List available tools",
		Payload:     b.getOrCreateType("ToolsListPayload", b.buildToolsListPayloadType).AttributeExpr,
		Result:      b.getOrCreateType("ToolsListResult", b.buildToolsListResultType).AttributeExpr,
	}
}

// buildToolsCallMethod creates the tools/call method
func (b *mcpExprBuilder) buildToolsCallMethod() *expr.MethodExpr {
	m := &expr.MethodExpr{
		Name:        "tools/call",
		Description: "Call a tool",
		Payload:     b.getOrCreateType("ToolsCallPayload", b.buildToolsCallPayloadType).AttributeExpr,
		Result:      b.getOrCreateType("ToolsCallResult", b.buildToolsCallResultType).AttributeExpr,
	}
	if b.anyToolStreaming() {
		m.Stream = expr.ServerStreamKind
		m.StreamingResult = b.getOrCreateType("ToolsCallResult", b.buildToolsCallResultType).AttributeExpr
	}
	return m
}

// buildResourcesListMethod creates the resources/list method
func (b *mcpExprBuilder) buildResourcesListMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "resources/list",
		Description: "List available resources",
		Payload:     b.getOrCreateType("ResourcesListPayload", b.buildResourcesListPayloadType).AttributeExpr,
		Result:      b.getOrCreateType("ResourcesListResult", b.buildResourcesListResultType).AttributeExpr,
	}
}

// buildResourcesReadMethod creates the resources/read method
func (b *mcpExprBuilder) buildResourcesReadMethod() *expr.MethodExpr {
	m := &expr.MethodExpr{
		Name:        "resources/read",
		Description: "Read a resource",
		Payload:     b.getOrCreateType("ResourcesReadPayload", b.buildResourcesReadPayloadType).AttributeExpr,
		Result:      b.getOrCreateType("ResourcesReadResult", b.buildResourcesReadResultType).AttributeExpr,
	}
	if b.anyResourceStreaming() {
		m.Stream = expr.ServerStreamKind
		m.StreamingResult = b.getOrCreateType("ResourcesReadResult", b.buildResourcesReadResultType).AttributeExpr
	}
	return m
}

// buildResourcesSubscribeMethod creates the resources/subscribe method
func (b *mcpExprBuilder) buildResourcesSubscribeMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "resources/subscribe",
		Description: "Subscribe to resource changes",
		Payload:     b.getOrCreateType("ResourcesSubscribePayload", b.buildSubscribePayloadType).AttributeExpr,
		Result:      nil, // No result, returns nothing on success
	}
}

// buildResourcesUnsubscribeMethod creates the resources/unsubscribe method
func (b *mcpExprBuilder) buildResourcesUnsubscribeMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "resources/unsubscribe",
		Description: "Unsubscribe from resource changes",
		Payload:     b.getOrCreateType("ResourcesUnsubscribePayload", b.buildUnsubscribePayloadType).AttributeExpr,
		Result:      nil, // No result, returns nothing on success
	}
}

// buildPromptsListMethod creates the prompts/list method
func (b *mcpExprBuilder) buildPromptsListMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "prompts/list",
		Description: "List available prompts",
		Payload:     b.getOrCreateType("PromptsListPayload", b.buildPromptsListPayloadType).AttributeExpr,
		Result:      b.getOrCreateType("PromptsListResult", b.buildPromptsListResultType).AttributeExpr,
	}
}

// buildPromptsGetMethod creates the prompts/get method
func (b *mcpExprBuilder) buildPromptsGetMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "prompts/get",
		Description: "Get a prompt by name",
		Payload:     b.getOrCreateType("PromptsGetPayload", b.buildPromptsGetPayloadType).AttributeExpr,
		Result:      b.getOrCreateType("PromptsGetResult", b.buildPromptsGetResultType).AttributeExpr,
	}
}

// buildNotificationMethods creates notification-related methods
func (b *mcpExprBuilder) buildNotificationMethods() []*expr.MethodExpr {
	methods := make([]*expr.MethodExpr, 0, len(b.mcp.Notifications))

	// Create a method for each notification type
	for _, notif := range b.mcp.Notifications {
		method := &expr.MethodExpr{
			Name:        "notify_" + notif.Name,
			Description: notif.Description,
			Payload:     b.getOrCreateType("SendNotificationPayload", b.buildSendNotificationPayloadType).AttributeExpr,
		}
		methods = append(methods, method)
	}

	return methods
}

// buildSubscriptionMethods creates subscription-related methods
func (b *mcpExprBuilder) buildSubscriptionMethods() []*expr.MethodExpr {
	return []*expr.MethodExpr{
		{
			Name:        "subscribe",
			Description: "Subscribe to resource updates",
			Payload:     b.getOrCreateType("SubscribePayload", b.buildSubscribePayloadType).AttributeExpr,
			Result:      b.getOrCreateType("SubscribeResult", b.buildSubscribeResultType).AttributeExpr,
		},
		{
			Name:        "unsubscribe",
			Description: "Unsubscribe from resource updates",
			Payload:     b.getOrCreateType("UnsubscribePayload", b.buildUnsubscribePayloadType).AttributeExpr,
			Result:      b.getOrCreateType("UnsubscribeResult", b.buildUnsubscribeResultType).AttributeExpr,
		},
	}
}

// hasPrompts checks if there are any prompts defined
func (b *mcpExprBuilder) hasPrompts() bool {
	if len(b.mcp.Prompts) > 0 {
		return true
	}

	if mcpexpr.Root != nil {
		dynamicPrompts := mcpexpr.Root.DynamicPrompts[b.originalService.Name]
		if len(dynamicPrompts) > 0 {
			return true
		}
	}

	return false
}

// anyToolStreaming returns true if any referenced tool method is streaming
func (b *mcpExprBuilder) anyToolStreaming() bool {
	for _, t := range b.mcp.Tools {
		if t != nil && t.Method != nil && t.Method.Stream == expr.ServerStreamKind {
			return true
		}
	}
	return false
}

// anyResourceStreaming returns true if any referenced resource method is streaming
func (b *mcpExprBuilder) anyResourceStreaming() bool {
	for _, r := range b.mcp.Resources {
		if r != nil && r.Method != nil && r.Method.Stream == expr.ServerStreamKind {
			return true
		}
	}
	return false
}
