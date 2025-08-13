package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
	mcpexpr "goa.design/plugins/v3/mcp/expr"
)

// Types
type (
	// AdapterData holds the data for generating the adapter
	AdapterData struct {
		ServiceName    string
		ServiceGoName  string
		MCPServiceName string
		MCPName        string
		MCPVersion     string
		Package        string
		MCPPackage     string
		ImportPath     string
		Tools          []*ToolAdapter
		Resources      []*ResourceAdapter
		StaticPrompts  []*StaticPromptAdapter
		DynamicPrompts []*DynamicPromptAdapter
		// client-side features removed: sampling, roots
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
	}

	// (removed) SamplingAdapter, RootsAdapter

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
// Generate generates the adapter implementation file
func (g *adapterGenerator) Generate() *codegen.File {
	svcName := codegen.SnakeCase(g.originalService.Name)
	path := filepath.Join(codegen.Gendir, "mcp", svcName, "adapter.go")

	data := g.buildAdapterData()

	// Build imports
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "time"},
		{Path: g.genpkg + "/" + svcName, Name: svcName},
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(fmt.Sprintf("MCP adapter for %s service", g.originalService.Name), svcName, imports),
	}

	// Add prompt provider interface if needed
	if len(data.StaticPrompts) > 0 || len(data.DynamicPrompts) > 0 {
		sections = append(sections, &codegen.SectionTemplate{
			Name:   "mcp-prompt-provider",
			Source: mcpTemplates.Read("prompt_provider"),
			Data:   data,
			FuncMap: map[string]any{
				"goify": func(s string) string { return codegen.Goify(s, true) },
			},
		})
	}

	sections = append(sections, &codegen.SectionTemplate{
		Name:   "mcp-adapter",
		Source: mcpTemplates.Read("adapter"),
		Data:   data,
		FuncMap: map[string]any{
			"goify":     func(s string) string { return codegen.Goify(s, true) },
			"snakeCase": codegen.SnakeCase,
		},
	})

	return &codegen.File{
		Path:             path,
		SectionTemplates: sections,
	}
}

// buildAdapterData creates the data for the adapter template
func (g *adapterGenerator) buildAdapterData() *AdapterData {
	data := &AdapterData{
		ServiceName:    g.originalService.Name,
		ServiceGoName:  codegen.Goify(g.originalService.Name, true),
		MCPServiceName: g.originalService.Name,
		MCPName:        g.mcp.Name,
		MCPVersion:     g.mcp.Version,
		Package:        codegen.SnakeCase(g.originalService.Name),
		MCPPackage:     "mcp" + codegen.SnakeCase(g.originalService.Name),
		ImportPath:     g.genpkg,
		Tools:          g.buildToolAdapters(),
		Resources:      g.buildResourceAdapters(),
		DynamicPrompts: g.buildDynamicPromptAdapters(),
		// client-side features removed
	}

	// client-side features removed

	// Static prompts are handled directly in the adapter
	data.StaticPrompts = g.buildStaticPrompts()

	return data
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
		}

		// Set payload type reference only for real payloads
		if hasRealPayload {
			adapter.PayloadType = g.getTypeReference(tool.Method.Payload)
		}

		// Set result type reference
		if tool.Method.Result != nil {
			adapter.ResultType = g.getTypeReference(tool.Method.Result)
		}

		adapters = append(adapters, adapter)
	}

	return adapters
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
			}

			adapters = append(adapters, adapter)
		}
	}

	return adapters
}

// (removed) buildSamplingAdapters

// (removed) buildRootsAdapters

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
