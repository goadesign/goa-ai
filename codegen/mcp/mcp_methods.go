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
	methods = append(methods, b.buildDiscoverMethod())

	// Add tool methods if tools are defined
	if len(b.mcp.Tools) > 0 {
		methods = append(methods, b.buildToolsListMethod(), b.buildToolsCallMethod())
	}

	// Add resource methods if resources are defined
	if len(b.mcp.Resources) > 0 || b.mcp.ResourceReader != nil {
		methods = append(methods,
			b.buildResourcesListMethod(),
			b.buildResourcesReadMethod(),
			b.buildResourceTemplatesListMethod(),
		)
	}

	// Add prompt methods if prompts are defined
	if b.hasPrompts() {
		methods = append(methods, b.buildPromptsListMethod(), b.buildPromptsGetMethod())
	}

	if len(b.mcp.PromptCompletions)+len(b.mcp.ResourceCompletions) > 0 {
		methods = append(methods, b.buildCompletionMethod())
	}
	if b.mcp.SubscriptionSource != nil {
		methods = append(methods, b.buildSubscriptionsListenMethod())
	}
	if len(b.tasks) > 0 {
		methods = append(methods, b.buildTaskMethods()...)
	}
	if b.mcp.SkillCatalog != nil && b.mcp.SkillLookup != nil {
		methods = append(methods, b.buildSkillsMethods()...)
	}
	if b.mcp.ResourceDirectory != nil {
		methods = append(methods, b.buildResourceDirectoryMethod())
	}
	return methods
}

// buildDiscoverMethod exposes the release's capabilities and supported revision
// without requiring a connection handshake or server-side client state.
func (b *mcpExprBuilder) buildDiscoverMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "server/discover",
		Description: "Describe this server's protocol revision and declared tools, resources, and prompts",
		Payload:     b.userTypeAttr("DiscoverPayload", func() *expr.AttributeExpr { return &expr.AttributeExpr{Type: &expr.Object{}} }),
		Result:      b.userTypeAttr("DiscoverResult", b.buildDiscoverResultType),
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
		Result:      b.inputResultAttr("ToolsCallResult", b.buildToolsCallResultType),
		Errors:      b.buildInputMethodErrors(),
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
		Result:      b.inputResultAttr("ResourcesReadResult", b.buildResourcesReadResultType),
		Errors:      b.buildInputMethodErrors(),
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
		Result:      b.inputResultAttr("PromptsGetResult", b.buildPromptsGetResultType),
		Errors:      b.buildInputMethodErrors(),
	}
}

// hasPrompts checks if there are any prompts defined
func (b *mcpExprBuilder) hasPrompts() bool {
	return len(b.mcp.Prompts)+len(b.mcp.MethodPrompts) > 0
}
