//nolint:lll // long signatures and struct literals are acceptable in generator
package codegen

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	mcpexpr "goa.design/goa-ai/features/mcp/expr"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// Types
type (
	// AdapterData holds the data for generating the adapter
	AdapterData struct {
		ServiceName         string
		ServiceGoName       string
		MCPServiceName      string
		MCPName             string
		MCPVersion          string
		ProtocolVersion     string
		Package             string
		MCPPackage          string
		ServiceJSONRPCAlias string
		ImportPath          string
		Tools               []*ToolAdapter
		Resources           []*ResourceAdapter
		StaticPrompts       []*StaticPromptAdapter
		DynamicPrompts      []*DynamicPromptAdapter
		Notifications       []*NotificationAdapter
		Subscriptions       []*SubscriptionAdapter
		// Streaming flags derived from original service DSL
		ToolsCallStreaming bool
		// Derived flags
		HasWatchableResources bool

		Register     *RegisterData
		ClientCaller *ClientCallerData
	}

	// RegisterData drives generation of runtime registration helpers.
	RegisterData struct {
		Package            string
		HelperName         string
		ServiceName        string
		SuiteName          string
		SuiteQualifiedName string
		Description        string
		Tools              []RegisterTool
	}

	ClientCallerData struct {
		MCPImportPath string
	}

	// RegisterTool represents a single tool entry in the helper file.
	RegisterTool struct {
		QualifiedName string
		Description   string
		PayloadType   string
		ResultType    string
		InputSchema   string
		ExampleArgs   string
	}

	// ToolAdapter represents a tool adapter
	ToolAdapter struct {
		Name               string
		Description        string
		OriginalMethodName string
		HasPayload         bool
		HasResult          bool
		PayloadType        string
		ResultType         string
		InputSchema        string
		IsStreaming        bool
		StreamInterface    string
		StreamEventType    string
		// Simple validations (top-level only)
		RequiredFields []string
		EnumFields     map[string][]string
		EnumFieldsPtr  map[string]bool
		// ExampleArguments contains a minimal valid JSON for tool arguments
		ExampleArguments string
	}

	// ResourceAdapter represents a resource adapter
	ResourceAdapter struct {
		Name               string
		Description        string
		URI                string
		MimeType           string
		OriginalMethodName string
		HasPayload         bool
		HasResult          bool
		PayloadType        string
		ResultType         string
		Watchable          bool
	}

	// StaticPromptAdapter represents a static prompt
	StaticPromptAdapter struct {
		Name        string
		Description string
		Messages    []*PromptMessageAdapter
	}

	// PromptMessageAdapter represents a prompt message
	PromptMessageAdapter struct {
		Role    string
		Content string
	}

	// DynamicPromptAdapter represents a dynamic prompt adapter
	DynamicPromptAdapter struct {
		Name               string
		Description        string
		OriginalMethodName string
		HasPayload         bool
		PayloadType        string
		ResultType         string
		// Arguments describes prompt arguments derived from the payload (dynamic prompts)
		Arguments []PromptArg
		// ExampleArguments contains a minimal valid JSON for prompt arguments
		ExampleArguments string
	}

	// PromptArg is a lightweight representation for generating PromptArgument values
	PromptArg struct {
		Name        string
		Description string
		Required    bool
	}

	// NotificationAdapter represents a notification mapping
	NotificationAdapter struct {
		Name               string
		Description        string
		OriginalMethodName string
	}

	// SubscriptionAdapter represents a subscription mapping
	SubscriptionAdapter struct {
		ResourceName       string
		ResourceURI        string
		OriginalMethodName string
	}

	// adapterGenerator generates the adapter layer between MCP and the original service
	adapterGenerator struct {
		genpkg          string
		originalService *expr.ServiceExpr
		mcp             *mcpexpr.MCPExpr
		mapping         *ServiceMethodMapping
	}
)

// newAdapterGenerator creates a new adapter generator
func newAdapterGenerator(genpkg string, svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, mapping *ServiceMethodMapping) *adapterGenerator {
	return &adapterGenerator{
		genpkg:          genpkg,
		originalService: svc,
		mcp:             mcp,
		mapping:         mapping,
	}
}

// Private methods

// buildAdapterData creates the data for the adapter template
func (g *adapterGenerator) buildAdapterData() *AdapterData {
	data := &AdapterData{
		ServiceName:         g.originalService.Name,
		ServiceGoName:       codegen.Goify(g.originalService.Name, true),
		MCPServiceName:      g.originalService.Name,
		MCPName:             g.mcp.Name,
		MCPVersion:          g.mcp.Version,
		ProtocolVersion:     g.mcp.ProtocolVersion,
		Package:             codegen.SnakeCase(g.originalService.Name),
		MCPPackage:          "mcp" + codegen.SnakeCase(g.originalService.Name),
		ServiceJSONRPCAlias: codegen.SnakeCase(g.originalService.Name) + "jsonrpc",
		ImportPath:          g.genpkg,
		Tools:               g.buildToolAdapters(),
		Resources:           g.buildResourceAdapters(),
		DynamicPrompts:      g.buildDynamicPromptAdapters(),
		Notifications:       g.buildNotificationAdapters(),
		Subscriptions:       g.buildSubscriptionAdapters(),
	}

	// Streaming flags
	data.ToolsCallStreaming = g.anyToolStreaming()

	// Static prompts are handled directly in the adapter
	data.StaticPrompts = g.buildStaticPrompts()

	// Derive watchable resources presence
	for _, r := range data.Resources {
		if r.Watchable {
			data.HasWatchableResources = true
			break
		}
	}

	data.Register = g.buildRegisterData(data)
	data.ClientCaller = g.buildClientCallerData(data, g.genpkg)

	return data
}

func (g *adapterGenerator) buildRegisterData(data *AdapterData) *RegisterData {
	if len(data.Tools) == 0 {
		return nil
	}
	serviceGoName := data.ServiceGoName
	suiteGoName := codegen.Goify(g.mcp.Name, true)
	desc := g.mcp.Description
	if strings.TrimSpace(desc) == "" {
		desc = fmt.Sprintf("MCP suite %s.%s", g.originalService.Name, g.mcp.Name)
	}
	helper := serviceGoName + suiteGoName + "Toolset"
	reg := &RegisterData{
		Package:            data.MCPPackage,
		HelperName:         helper,
		ServiceName:        g.originalService.Name,
		SuiteName:          g.mcp.Name,
		SuiteQualifiedName: fmt.Sprintf("%s.%s", g.originalService.Name, g.mcp.Name),
		Description:        desc,
	}
	for _, tool := range data.Tools {
		schema := tool.InputSchema
		if strings.TrimSpace(schema) == "" {
			schema = "{}"
		}
		payloadType := tool.PayloadType
		if payloadType == "" {
			payloadType = "any"
		}
		resultType := tool.ResultType
		if resultType == "" {
			resultType = "any"
		}
		reg.Tools = append(reg.Tools, RegisterTool{
			QualifiedName: fmt.Sprintf("%s.%s.%s", reg.ServiceName, reg.SuiteName, tool.Name),
			Description:   tool.Description,
			PayloadType:   payloadType,
			ResultType:    resultType,
			InputSchema:   schema,
			ExampleArgs:   tool.ExampleArguments,
		})
	}
	return reg
}

func (g *adapterGenerator) buildClientCallerData(data *AdapterData, genpkg string) *ClientCallerData {
	if data.Register == nil {
		return nil
	}
	svcName := codegen.SnakeCase(g.originalService.Name)
	importPath := path.Join(genpkg, "mcp_"+svcName)
	return &ClientCallerData{
		MCPImportPath: importPath,
	}
}

// buildToolAdapters creates adapter data for tools
func (g *adapterGenerator) buildToolAdapters() []*ToolAdapter {
	adapters := make([]*ToolAdapter, 0, len(g.mcp.Tools))

	for _, tool := range g.mcp.Tools {
		// Check if payload is Empty type (added by Goa during Finalize)
		hasRealPayload := tool.Method.Payload != nil && tool.Method.Payload.Type != expr.Empty

		adapter := &ToolAdapter{
			Name:               tool.Name,
			Description:        tool.Description,
			OriginalMethodName: codegen.Goify(tool.Method.Name, true),
			HasPayload:         hasRealPayload,
			HasResult:          tool.Method.Result != nil,
			IsStreaming:        tool.Method.Stream == expr.ServerStreamKind,
		}

		// Set streaming interface and event types for server-streaming methods
		if adapter.IsStreaming {
			adapter.StreamInterface = codegen.Goify(tool.Method.Name, true) + "ServerStream"
			adapter.StreamEventType = codegen.Goify(tool.Method.Name, true) + "Event"
		}

		// Set payload type reference only for real payloads
		if hasRealPayload {
			adapter.PayloadType = g.getTypeReference(tool.Method.Payload)
			// Generate a minimal JSON Schema for MCP tools/list
			adapter.InputSchema = toJSONSchema(tool.Method.Payload)
			// Collect simple validations for adapter-side checks
			req, enums, enumPtr := g.collectTopLevelValidations(tool.Method.Payload)
			adapter.RequiredFields = req
			adapter.EnumFields = enums
			adapter.EnumFieldsPtr = enumPtr
			// Produce a minimal valid example JSON for arguments
			adapter.ExampleArguments = g.buildExampleJSON(tool.Method.Payload)
		} else {
			adapter.ExampleArguments = "{}"
		}

		// Set result type reference
		if tool.Method.Result != nil {
			adapter.ResultType = g.getTypeReference(tool.Method.Result)
		}

		adapters = append(adapters, adapter)
	}

	return adapters
}

// collectTopLevelValidations extracts required fields and enum values for a top-level object payload
func (g *adapterGenerator) collectTopLevelValidations(attr *expr.AttributeExpr) ([]string, map[string][]string, map[string]bool) {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return nil, nil, nil
	}
	// Unwrap user type
	if ut, ok := attr.Type.(expr.UserType); ok {
		return g.collectTopLevelValidations(ut.Attribute())
	}
	obj, ok := attr.Type.(*expr.Object)
	if !ok {
		return nil, nil, nil
	}
	req := []string{}
	enums := map[string][]string{}
	enumPtr := map[string]bool{}
	// Build a quick map of attribute by name
	fields := map[string]*expr.AttributeExpr{}
	for _, nat := range *obj {
		fields[nat.Name] = nat.Attribute
		// enum capture: stringify values to support string and numeric enums
		if nat.Attribute.Validation == nil || len(nat.Attribute.Validation.Values) == 0 {
			continue
		}
		vals := []string{}
		for _, v := range nat.Attribute.Validation.Values {
			vals = append(vals, fmt.Sprint(v))
		}
		if len(vals) > 0 {
			enums[nat.Name] = vals
		}
	}
	if attr.Validation != nil && len(attr.Validation.Required) > 0 {
		for _, name := range attr.Validation.Required {
			if fa, ok := fields[name]; ok {
				// Only require string fields here (simple non-empty check)
				if pk, okp := fa.Type.(expr.Primitive); okp && pk.Kind() == expr.StringKind {
					req = append(req, name)
				}
			}
		}
	}
	// Determine pointer-ness for enum fields: string enum fields not required are pointers
	reqSet := map[string]struct{}{}
	if attr.Validation != nil {
		for _, n := range attr.Validation.Required {
			reqSet[n] = struct{}{}
		}
	}
	for n := range enums {
		_, isReq := reqSet[n]
		enumPtr[n] = !isReq
	}
	return req, enums, enumPtr
}

// anyToolStreaming returns true if any MCP tool maps to a streaming method
func (g *adapterGenerator) anyToolStreaming() bool {
	for _, t := range g.mcp.Tools {
		if t != nil && t.Method != nil && t.Method.Stream == expr.ServerStreamKind {
			return true
		}
	}
	return false
}

// buildResourceAdapters creates adapter data for resources
func (g *adapterGenerator) buildResourceAdapters() []*ResourceAdapter {
	adapters := make([]*ResourceAdapter, 0, len(g.mcp.Resources))

	for _, resource := range g.mcp.Resources {
		// Check if payload is Empty type (added by Goa during Finalize)
		hasRealPayload := resource.Method.Payload != nil && resource.Method.Payload.Type != expr.Empty

		adapter := &ResourceAdapter{
			Name:               resource.Name,
			Description:        resource.Description,
			URI:                resource.URI,
			MimeType:           resource.MimeType,
			OriginalMethodName: codegen.Goify(resource.Method.Name, true),
			HasPayload:         hasRealPayload,
			HasResult:          resource.Method.Result != nil,
			Watchable:          resource.Watchable,
		}

		// Set payload type reference only for real payloads
		if hasRealPayload {
			adapter.PayloadType = g.getTypeReference(resource.Method.Payload)
		}

		// Set result type reference
		if resource.Method.Result != nil {
			adapter.ResultType = g.getTypeReference(resource.Method.Result)
		}

		adapters = append(adapters, adapter)
	}

	return adapters
}

// buildDynamicPromptAdapters creates adapter data for dynamic prompts
func (g *adapterGenerator) buildDynamicPromptAdapters() []*DynamicPromptAdapter {
	var adapters []*DynamicPromptAdapter

	if mcpexpr.Root != nil {
		dynamicPrompts := mcpexpr.Root.DynamicPrompts[g.originalService.Name]
		for _, dp := range dynamicPrompts {
			// Check if payload is Empty type (added by Goa during Finalize)
			hasRealPayload := dp.Method.Payload != nil && dp.Method.Payload.Type != expr.Empty

			adapter := &DynamicPromptAdapter{
				Name:               dp.Name,
				Description:        dp.Description,
				OriginalMethodName: codegen.Goify(dp.Method.Name, true),
				HasPayload:         hasRealPayload,
			}

			// Set payload type reference only for real payloads
			if hasRealPayload {
				adapter.PayloadType = g.getTypeReference(dp.Method.Payload)
				adapter.Arguments = g.promptArgsFromPayload(dp.Method.Payload)
				adapter.ExampleArguments = g.buildExampleJSON(dp.Method.Payload)
			} else {
				adapter.ExampleArguments = "{}"
			}

			// Set result type reference if present
			if dp.Method.Result != nil {
				adapter.ResultType = g.getTypeReference(dp.Method.Result)
			}

			adapters = append(adapters, adapter)
		}
	}

	return adapters
}

// buildExampleJSON produces a minimal valid JSON string for the given payload attribute.
// It prioritizes required fields and uses enum defaults when available.
func (g *adapterGenerator) buildExampleJSON(attr *expr.AttributeExpr) string {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return "{}"
	}
	// Use Goa's example generator with a deterministic randomizer for stable output
	r := &expr.ExampleGenerator{Randomizer: expr.NewDeterministicRandomizer()}
	v := attr.Example(r)
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// promptArgsFromPayload builds a flat list of prompt arguments from a payload attribute (top-level only)
func (g *adapterGenerator) promptArgsFromPayload(attr *expr.AttributeExpr) []PromptArg {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return nil
	}
	// Unwrap user type
	if ut, ok := attr.Type.(expr.UserType); ok {
		return g.promptArgsFromPayload(ut.Attribute())
	}
	obj, ok := attr.Type.(*expr.Object)
	if !ok {
		return nil
	}
	// Pre-allocate based on number of top-level fields
	out := make([]PromptArg, 0, len(*obj))
	// Build required set
	required := map[string]struct{}{}
	if attr.Validation != nil {
		for _, n := range attr.Validation.Required {
			required[n] = struct{}{}
		}
	}
	for _, nat := range *obj {
		name := nat.Name
		desc := ""
		if nat.Attribute != nil && nat.Attribute.Description != "" {
			desc = nat.Attribute.Description
		}
		_, req := required[name]
		out = append(out, PromptArg{Name: name, Description: desc, Required: req})
	}
	return out
}

// buildNotificationAdapters creates adapter data for notifications
func (g *adapterGenerator) buildNotificationAdapters() []*NotificationAdapter {
	adapters := make([]*NotificationAdapter, 0)
	if g.mcp != nil {
		for _, n := range g.mcp.Notifications {
			adapters = append(adapters, &NotificationAdapter{
				Name:               n.Name,
				Description:        n.Description,
				OriginalMethodName: codegen.Goify(n.Method.Name, true),
			})
		}
	}
	return adapters
}

// buildSubscriptionAdapters creates adapter data for subscriptions
func (g *adapterGenerator) buildSubscriptionAdapters() []*SubscriptionAdapter {
	adapters := make([]*SubscriptionAdapter, 0)
	if g.mcp != nil {
		for _, s := range g.mcp.Subscriptions {
			adapters = append(adapters, &SubscriptionAdapter{
				ResourceName:       s.ResourceName,
				OriginalMethodName: codegen.Goify(s.Method.Name, true),
			})
		}
	}
	return adapters
}

// buildStaticPrompts creates data for static prompts
func (g *adapterGenerator) buildStaticPrompts() []*StaticPromptAdapter {
	prompts := make([]*StaticPromptAdapter, 0, len(g.mcp.Prompts))

	for _, prompt := range g.mcp.Prompts {
		adapter := &StaticPromptAdapter{
			Name:        prompt.Name,
			Description: prompt.Description,
			Messages:    make([]*PromptMessageAdapter, len(prompt.Messages)),
		}

		for i, msg := range prompt.Messages {
			adapter.Messages[i] = &PromptMessageAdapter{
				Role:    msg.Role,
				Content: msg.Content,
			}
		}

		prompts = append(prompts, adapter)
	}

	return prompts
}

// getTypeReference returns a Go type reference for an attribute
func (g *adapterGenerator) getTypeReference(attr *expr.AttributeExpr) string {
	// Use Goa's built-in scope for proper type references
	scope := codegen.NewNameScope()
	svcName := codegen.SnakeCase(g.originalService.Name)

	// Check if it's a user type that needs package qualification
	if _, ok := attr.Type.(expr.UserType); ok {
		// For user types, use GoFullTypeRef with package qualification
		return scope.GoFullTypeRef(attr, svcName)
	}

	// For other types, use GoTypeRef which handles pointers correctly
	return scope.GoTypeRef(attr)
}
