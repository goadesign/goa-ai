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

		// Generate MCP client adapter that wraps the MCP JSON-RPC client
		files = append(files, generateMCPClientAdapter(genpkg, svc, mcp, mapping)...)
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

    // Generate both base and SSE server files.
    base := jsonrpccodegen.ServerFiles(genpkg, httpServices)
    sse := jsonrpccodegen.SSEServerFiles(genpkg, httpServices)
    files = append(files, base...)
    files = append(files, sse...)
    files = append(files, jsonrpccodegen.ServerTypeFiles(genpkg, httpServices)...)
    files = append(files, jsonrpccodegen.PathFiles(httpServices)...)
	// Add client-side JSON-RPC for MCP service so adapters can depend on it
	files = append(files, jsonrpccodegen.ClientTypeFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.ClientFiles(genpkg, httpServices)...)
	// NOTE: SSEClientFiles may not exist depending on Goa version
	// files = append(files, jsonrpccodegen.SSEClientFiles(genpkg, httpServices)...)

    return files
}

// (no dedupe or merge of JSON-RPC server handlers is performed here).

// generateMCPTransport generates MCP transport layer files
func generateMCPTransport(genpkg string, svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, mapping *ServiceMethodMapping) []*codegen.File {
	var files []*codegen.File
	svcName := codegen.SnakeCase(svc.Name)

	// Generate server adapter in gen/mcp_<service>/adapter_server.go (same package as MCP service)
	adapterPath := filepath.Join(codegen.Gendir, "mcp_"+svcName, "adapter_server.go")
	adapterGen := newAdapterGenerator(genpkg, svc, mcp, mapping)
	data := adapterGen.buildAdapterData()
	pkgName := data.MCPPackage

    adapterImports := []*codegen.ImportSpec{
        {Path: "bytes"},
        {Path: "context"},
        {Path: "encoding/json"},
        {Path: "fmt"},
        {Path: "io"},
        {Path: "net/http"},
        {Path: "net/url"},
        {Path: "strconv"},
        {Path: "strings"},
        {Path: "sync"},
        {Path: genpkg + "/" + svcName, Name: svcName},
        {Path: "goa.design/goa/v3/http", Name: "goahttp"},
		{Path: "goa.design/goa/v3/pkg", Name: "goa"},
    }
	files = append(files, &codegen.File{
		Path: adapterPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(fmt.Sprintf("MCP server adapter for %s service", svc.Name), pkgName, adapterImports),
			{
				Name:   "mcp-adapter",
				Source: mcpTemplates.Read("adapter"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
		},
	})

	// If prompts are present, generate prompt_provider in a separate file (same package)
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

// generateMCPClientAdapter generates a client adapter that exposes the original
// service endpoints while calling MCP JSON-RPC methods under the hood.
func generateMCPClientAdapter(genpkg string, svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, mapping *ServiceMethodMapping) []*codegen.File {
	var files []*codegen.File

	svcName := codegen.SnakeCase(svc.Name)
	// Match the package alias used elsewhere (strip underscores)
	mcpPkgAlias := "mcp" + strings.ReplaceAll(svcName, "_", "")
	svcJSONRPCCAlias := svcName + "jsonrpcc"
	mcpJSONRPCCAlias := mcpPkgAlias + "jsonrpcc"

	adapterGen := newAdapterGenerator(genpkg, svc, mcp, mapping)
	data := adapterGen.buildAdapterData()

	// Extend data passed to template with aliases needed by imports
	type methodInfo struct {
		Name     string
		IsMapped bool // Whether this method is mapped to an MCP construct
	}

	type clientAdapterTemplateData struct {
		*AdapterData
		ServiceGoName    string
		ServicePkg       string
		MCPPkgAlias      string
		SvcJSONRPCCAlias string
		MCPJSONRPCCAlias string
		AllMethods       []methodInfo // All service methods with mapping info
	}

	// Build set of mapped methods
	mapped := make(map[string]struct{})
	for _, t := range data.Tools {
		mapped[t.OriginalMethodName] = struct{}{}
	}
	for _, r := range data.Resources {
		mapped[r.OriginalMethodName] = struct{}{}
	}
	for _, dp := range data.DynamicPrompts {
		mapped[dp.OriginalMethodName] = struct{}{}
	}

	// Collect all service method names and check if they're mapped to MCP constructs
	allMethods := make([]methodInfo, len(svc.Methods))
	for i, m := range svc.Methods {
		methodName := codegen.Goify(m.Name, true)
		_, ok := mapped[methodName]
		allMethods[i] = methodInfo{
			Name:     methodName,
			IsMapped: ok,
		}
	}

	tdata := &clientAdapterTemplateData{
		AdapterData:      data,
		ServiceGoName:    codegen.Goify(svc.Name, true),
		ServicePkg:       svcName,
		MCPPkgAlias:      mcpPkgAlias,
		SvcJSONRPCCAlias: svcJSONRPCCAlias,
		MCPJSONRPCCAlias: mcpJSONRPCCAlias,
		AllMethods:       allMethods,
	}

	imports := []*codegen.ImportSpec{
		{Path: "bytes"},
		{Path: "context"},
		{Path: "fmt"},
		{Path: "net/http"},
		{Path: "io"},
		{Path: "goa.design/goa/v3/http", Name: "goahttp"},
		{Path: "goa.design/goa/v3/jsonrpc", Name: "jsonrpc"},
		{Path: genpkg + "/" + svcName, Name: svcName},
		{Path: genpkg + "/jsonrpc/" + svcName + "/client", Name: svcJSONRPCCAlias},
		// Import the MCP service package for types since we're now in a subpackage
		{Path: genpkg + "/mcp_" + svcName, Name: mcpPkgAlias},
		{Path: genpkg + "/jsonrpc/mcp_" + svcName + "/client", Name: mcpJSONRPCCAlias},
	}

	// Put client adapter in a separate subpackage to avoid import cycle
	adapterPkgName := mcpPkgAlias + "adapter"
	files = append(files, &codegen.File{
		Path: filepath.Join(codegen.Gendir, "mcp_"+svcName, "adapter", "client", "adapter.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("MCP client adapter exposing original service endpoints", adapterPkgName, imports),
			{
				Name:   "mcp-client-adapter",
				Source: mcpTemplates.Read("mcp_client_wrapper"),
				Data:   tdata,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
				},
			},
		},
	})

	return files
}
