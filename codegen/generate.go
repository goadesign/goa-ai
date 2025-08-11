package codegen

import (
	"fmt"
	"path/filepath"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	httpcodegen "goa.design/goa/v3/http/codegen"
	jsonrpccodegen "goa.design/goa/v3/jsonrpc/codegen"
	mcpexpr "goa.design/plugins/v3/mcp/expr"
)

// mcpTemplates defined in templates.go

// Generate orchestrates all MCP code generation
func Generate(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	// Process MCP services from original services
	for _, svc := range originalServices {
		if !mcpexpr.Root.HasMCP(svc) {
			continue
		}

		// Generate MCP service with MCP endpoints
		mcp := mcpexpr.Root.GetMCP(svc)

		// Build MCP service expression
		exprBuilder := newMCPExprBuilder(svc, mcp)
		mcpService := exprBuilder.BuildServiceExpr()

		// Create temporary root for MCP generation
		mcpRoot := exprBuilder.BuildRootExpr(mcpService)

		// Prepare, validate, and finalize MCP expressions
		if err := exprBuilder.PrepareAndValidate(mcpRoot); err != nil {
			return nil, fmt.Errorf("MCP expression validation failed: %w", err)
		}

		// Generate MCP service code using Goa's standard generators
		mcpFiles := generateMCPServiceCode(genpkg, mcpRoot, mcpService)
		files = append(files, mcpFiles...)

		// Generate MCP transport that wraps the original service
		mapping := exprBuilder.BuildServiceMapping()
		files = append(files, generateMCPTransport(genpkg, svc, mcp, mapping)...)
	}

	return files, nil
}

// (removed) generateMCPAdapter: unused

// generateMCPServiceCode generates all MCP service code using Goa's codegen
func generateMCPServiceCode(genpkg string, root *expr.RootExpr, mcpService *expr.ServiceExpr) []*codegen.File {
	files := make([]*codegen.File, 0, 16)

	// Create services data from temporary MCP root
	servicesData := service.NewServicesData(root)

	// Generate MCP service layer only (no HTTP transports for original service)
	userTypePkgs := make(map[string][]string)
	serviceFiles := service.Files(genpkg, mcpService, servicesData, userTypePkgs)
	for _, f := range serviceFiles {
		if strings.HasSuffix(filepath.ToSlash(f.Path), "/service.go") && len(f.SectionTemplates) > 0 {
			service.AddServiceDataMetaTypeImports(f.SectionTemplates[0], mcpService, servicesData.Get(mcpService.Name))
		}
	}
	files = append(files, serviceFiles...)
	files = append(files, service.EndpointFile(genpkg, mcpService, servicesData))
	files = append(files, service.ClientFile(genpkg, mcpService, servicesData))

	// Generate JSON-RPC transport for MCP service only
	httpServices := httpcodegen.NewServicesData(servicesData, &root.API.JSONRPC.HTTPExpr)
	httpServices.Root = root
	files = append(files, jsonrpccodegen.ServerFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.ServerTypeFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.SSEServerFiles(genpkg, httpServices)...)
	// MCP JSON-RPC client (encoders/decoders, types)
	files = append(files, jsonrpccodegen.ClientFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.ClientTypeFiles(genpkg, httpServices)...)

	// Generate JSON-RPC path files (both server and client)
	files = append(files, jsonrpccodegen.PathFiles(httpServices)...)

	// Do not generate HTTP encode/decode for MCP

	// Generate CLI support for MCP endpoints
	cliFiles := jsonrpccodegen.ClientCLIFiles(genpkg, httpServices)
	files = append(files, cliFiles...)

	// Let Goa JSON-RPC codegen emit client paths using the per-endpoint Routes set above
	// No custom path file needed

	return files
}

// generateMCPTransport generates MCP transport layer files
func generateMCPTransport(genpkg string, svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, mapping *ServiceMethodMapping) []*codegen.File {
	var files []*codegen.File
	svcName := codegen.SnakeCase(svc.Name)
	// Use a stable package name without underscores to match Goa's service package naming
	pkgName := "mcp" + strings.ReplaceAll(svcName, "_", "")

	// Generate adapter file in gen/mcp_<service>/adapter.go
	adapterPath := filepath.Join(codegen.Gendir, "mcp_"+svcName, "adapter.go")
	adapterGen := newAdapterGenerator(genpkg, svc, mcp, mapping)
	data := adapterGen.buildAdapterData()

	adapterImports := []*codegen.ImportSpec{
		{Path: "bytes"},
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "io"},
		{Path: "net/http"},
		{Path: genpkg + "/" + svcName, Name: svcName},
		// Use JSON-RPC server decoders for the original service
		{Path: genpkg + "/jsonrpc/" + svcName + "/server", Name: svcName + "jsonrpc"},
		{Path: "goa.design/goa/v3/http", Name: "goahttp"},
		{Path: "goa.design/goa/v3/pkg", Name: "goa"},
	}
	files = append(files, &codegen.File{
		Path: adapterPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(fmt.Sprintf("MCP adapter for %s service", svc.Name), pkgName, adapterImports),
			{
				Name:   "mcp-adapter",
				Source: mcpTemplates.Read("adapter"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
				},
			},
		},
	})

	// If prompts are present, generate prompt_provider in a separate file
	if len(data.StaticPrompts) > 0 || len(data.DynamicPrompts) > 0 {
		providerPath := filepath.Join(codegen.Gendir, "mcp_"+svcName, "prompt_provider.go")
		providerImports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "encoding/json"},
			{Path: genpkg + "/" + svcName, Name: svcName},
		}
		files = append(files, &codegen.File{
			Path: providerPath,
			SectionTemplates: []*codegen.SectionTemplate{
				codegen.Header(fmt.Sprintf("MCP prompt provider for %s service", svc.Name), pkgName, providerImports),
				{
					Name:   "mcp-prompt-provider",
					Source: mcpTemplates.Read("prompt_provider"),
					Data:   data,
					FuncMap: map[string]any{
						"goify": func(s string) string { return codegen.Goify(s, true) },
					},
				},
			},
		})
	}

	return files
}
