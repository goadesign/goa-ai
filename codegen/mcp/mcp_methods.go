// Package codegen defines the MCP protocol methods added beside one authored
// Goa service. Application methods remain unchanged; these methods exist only
// on the generated MCP protocol service.
package codegen

import (
	"goa.design/goa/v3/expr"
)

// buildMethods creates all MCP protocol methods
func (b *mcpExprBuilder) buildMethods() []*expr.MethodExpr {
	methods := make([]*expr.MethodExpr, 0, 10)
	methods = append(methods,
		b.buildInitializeMethod(),
		b.buildInitializedMethod(),
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
		)
	}

	// Add prompt methods if prompts are defined
	if b.hasPrompts() {
		methods = append(methods, b.buildPromptsListMethod(), b.buildPromptsGetMethod())
	}

	return methods
}

// buildInitializedMethod creates the notification that completes the MCP
// handshake. The transport marks this method as a JSON-RPC notification, so
// its empty payload is sent without an ID and receives no response.
func (b *mcpExprBuilder) buildInitializedMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "notifications/initialized",
		Description: "Mark an initialized MCP session ready for requests",
		Payload: b.userTypeAttr("InitializedPayload", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: &expr.Object{}}
		}),
	}
}

// buildInitializeMethod creates the initialize method
func (b *mcpExprBuilder) buildInitializeMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "initialize",
		Description: "Initialize MCP session",
		Payload:     b.userTypeAttr("InitializePayload", b.buildInitializePayloadType),
		Result:      b.userTypeAttr("InitializeResult", b.buildInitializeResultType),
	}
}

// buildPingMethod creates the ping method
func (b *mcpExprBuilder) buildPingMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "ping",
		Description: "Ping the server",
		Result:      b.userTypeAttr("PingResult", b.buildPingResultType),
	}
}

// buildToolsListMethod creates the tools/list method
func (b *mcpExprBuilder) buildToolsListMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "tools/list",
		Description: "List available tools",
		Payload:     b.userTypeAttr("ToolsListPayload", b.buildToolsListPayloadType),
		Result:      b.userTypeAttr("ToolsListResult", b.buildToolsListResultType),
		Errors:      buildMCPMethodErrors(mcpInvalidParamsError),
	}
}

// buildToolsCallMethod creates the tools/call method
func (b *mcpExprBuilder) buildToolsCallMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "tools/call",
		Description: "Call a tool",
		Payload:     b.userTypeAttr("ToolsCallPayload", b.buildToolsCallPayloadType),
		Result:      b.userTypeAttr("ToolsCallResult", b.buildToolsCallResultType),
		Errors:      buildMCPMethodErrors(mcpDispatchErrors[:]...),
	}
}

// buildResourcesListMethod creates the resources/list method
func (b *mcpExprBuilder) buildResourcesListMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "resources/list",
		Description: "List available resources",
		Payload:     b.userTypeAttr("ResourcesListPayload", b.buildResourcesListPayloadType),
		Result:      b.userTypeAttr("ResourcesListResult", b.buildResourcesListResultType),
		Errors:      buildMCPMethodErrors(mcpInvalidParamsError),
	}
}

// buildResourcesReadMethod creates the resources/read method
func (b *mcpExprBuilder) buildResourcesReadMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "resources/read",
		Description: "Read a resource",
		Payload:     b.userTypeAttr("ResourcesReadPayload", b.buildResourcesReadPayloadType),
		Result:      b.userTypeAttr("ResourcesReadResult", b.buildResourcesReadResultType),
		Errors:      buildMCPMethodErrors(mcpDispatchErrors[:]...),
	}
}

// buildPromptsListMethod creates the prompts/list method
func (b *mcpExprBuilder) buildPromptsListMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "prompts/list",
		Description: "List available prompts",
		Payload:     b.userTypeAttr("PromptsListPayload", b.buildPromptsListPayloadType),
		Result:      b.userTypeAttr("PromptsListResult", b.buildPromptsListResultType),
		Errors:      buildMCPMethodErrors(mcpInvalidParamsError),
	}
}

// buildPromptsGetMethod creates the prompts/get method
func (b *mcpExprBuilder) buildPromptsGetMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "prompts/get",
		Description: "Get a prompt by name",
		Payload:     b.userTypeAttr("PromptsGetPayload", b.buildPromptsGetPayloadType),
		Result:      b.userTypeAttr("PromptsGetResult", b.buildPromptsGetResultType),
		Errors:      buildMCPMethodErrors(mcpDispatchErrors[:]...),
	}
}

// hasPrompts checks if there are any prompts defined
func (b *mcpExprBuilder) hasPrompts() bool {
	return len(b.mcp.Prompts) > 0
}
