package codegen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"goa.design/goa-ai/codegen/shared"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	httpcodegen "goa.design/goa/v3/http/codegen"
	jsonrpccodegen "goa.design/goa/v3/jsonrpc/codegen"
)

const headerSection = "source-header"
const exampleMCPStubSection = "example-mcp-stub"

// Generate orchestrates MCP code generation for services that declare MCP
// configuration in the DSL. It validates the pure-MCP contract from the current
// roots so direct callers cannot bypass PrepareServices, then composes Goa
// service and JSON-RPC generators and adds adapter/client helpers.
func Generate(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	source := collectSourceSnapshot(roots)
	for _, svc := range source.services {
		if !mcpexpr.Root.HasMCP(svc) {
			continue
		}

		// Generate MCP service with MCP endpoints
		mcp := mcpexpr.Root.GetMCP(svc)
		if err := validatePureMCPService(svc, mcp, source); err != nil {
			return nil, err
		}

		// Build MCP service expression
		exprBuilder := newMCPExprBuilder(svc, mcp, source)
		mcpService := exprBuilder.BuildServiceExpr()

		// Create temporary root for MCP generation
		mcpRoot := exprBuilder.BuildRootExpr(mcpService)

		// Prepare, validate, and finalize MCP expressions
		if err := exprBuilder.PrepareAndValidate(mcpRoot); err != nil {
			return nil, fmt.Errorf("MCP expression validation failed: %w", err)
		}

		// Build mapping and adapter data early so we can customize generated clients
		mapping := exprBuilder.BuildServiceMapping()
		adapterGen := newAdapterGenerator(genpkg, svc, mcp, mapping)
		adapterData, err := adapterGen.buildAdapterData()
		if err != nil {
			return nil, err
		}
		if reg := registerFile(adapterData); reg != nil {
			files = append(files, reg)
		}
		if caller := clientCallerFile(adapterData, codegen.SnakeCase(svc.Name)); caller != nil {
			files = append(files, caller)
		}

		// Generate MCP service code using Goa's standard generators (with retry hooks)
		mcpFiles := generateMCPServiceCode(genpkg, mcpRoot, mcpService)
		files = append(files, mcpFiles...)

		// Generate MCP transport that wraps the original service
		transportFiles := generateMCPTransport(genpkg, svc, adapterData)
		files = append(files, transportFiles...)

		// Generate MCP client adapter that wraps the MCP JSON-RPC client
		clientFiles := generateMCPClientAdapter(genpkg, svc, adapterData)
		files = append(files, clientFiles...)
	}

	return files, nil
}

// generateMCPServiceCode generates the MCP service layer and JSON-RPC transport
// using Goa's built-in generators.
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
	files = append(files, jsonrpccodegen.ServerFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.SSEServerFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.ServerTypeFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.PathFiles(httpServices)...)
	// Add client-side JSON-RPC for MCP service so adapters can depend on it
	files = append(files, jsonrpccodegen.ClientTypeFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.ClientFiles(genpkg, httpServices)...)

	applyMCPPolicyHeadersToJSONRPCMount(files)
	return files
}

// applyMCPPolicyHeadersToJSONRPCMount replaces the JSON-RPC server mount section
// with a goa-ai-owned template that propagates MCP policy headers into the
// request context.
//
// This avoids any string-based patching while ensuring header-driven allow/deny
// policy can be enforced by MCP adapters without requiring example/server wiring
// changes.
func applyMCPPolicyHeadersToJSONRPCMount(files []*codegen.File) {
	for _, f := range files {
		if f == nil {
			continue
		}
		if filepath.Base(filepath.Dir(filepath.ToSlash(f.Path))) != "server" || filepath.Base(f.Path) != "server.go" {
			continue
		}
		for _, s := range f.SectionTemplates {
			if s == nil {
				continue
			}
			if s.Name == "jsonrpc-server-mount" {
				s.Source = mcpTemplates.Read("jsonrpc_server_mount")
			}
		}
	}
}

// generateMCPTransport generates adapter and prompt provider files that adapt
// MCP protocol methods to the original service implementation.
func generateMCPTransport(genpkg string, svc *expr.ServiceExpr, data *AdapterData) []*codegen.File {
	var files []*codegen.File
	svcName := codegen.SnakeCase(svc.Name)

	// Generate server adapter in gen/mcp_<service>/adapter_server.go (same package as MCP service)
	adapterPath := filepath.Join(codegen.Gendir, "mcp_"+svcName, "adapter_server.go")
	pkgName := data.MCPPackage

	adapterImports := []*codegen.ImportSpec{
		{Path: "bytes"},
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "io"},
		{Path: "net/http"},
		{Path: "net/url"},
		{Path: "path"},
		{Path: "strconv"},
		{Path: "strings"},
		{Path: "sync"},
		{Path: genpkg + "/" + svcName, Name: svcName},
		{Path: "goa.design/goa-ai/runtime/mcp", Name: "mcpruntime"},
		{Path: "goa.design/goa/v3/http", Name: "goahttp"},
		{Path: "goa.design/goa/v3/pkg", Name: "goa"},
	}
	// Include external user type imports referenced by method payloads/results.
	existing := make(map[string]struct{}, len(adapterImports))
	for _, im := range adapterImports {
		if im != nil && im.Path != "" {
			existing[im.Path] = struct{}{}
		}
	}
	extra := make(map[string]*codegen.ImportSpec)
	for _, m := range svc.Methods {
		if m.Payload != nil {
			for _, im := range shared.GatherAttributeImports(genpkg, m.Payload) {
				if im != nil && im.Path != "" {
					extra[im.Path] = im
				}
			}
		}
		if m.Result != nil {
			for _, im := range shared.GatherAttributeImports(genpkg, m.Result) {
				if im != nil && im.Path != "" {
					extra[im.Path] = im
				}
			}
		}
	}
	if len(extra) > 0 {
		// Deterministic order
		paths := make([]string, 0, len(extra))
		for p := range extra {
			if _, ok := existing[p]; ok {
				continue
			}
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			adapterImports = append(adapterImports, extra[p])
		}
	}
	files = append(files, &codegen.File{
		Path: adapterPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(fmt.Sprintf("MCP server adapter for %s service", svc.Name), pkgName, adapterImports),
			{
				Name:   "mcp-adapter-core",
				Source: mcpTemplates.Read("adapter_core"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-broadcast",
				Source: mcpTemplates.Read("adapter_broadcast"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-tools",
				Source: mcpTemplates.Read("adapter_tools"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-resources",
				Source: mcpTemplates.Read("adapter_resources"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-prompts",
				Source: mcpTemplates.Read("adapter_prompts"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-notifications",
				Source: mcpTemplates.Read("adapter_notifications"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-subscriptions",
				Source: mcpTemplates.Read("adapter_subscriptions"),
				Data:   data,
				FuncMap: map[string]any{
					"goify":   func(s string) string { return codegen.Goify(s, true) },
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
		},
	})

	// Generate protocol version constant in MCP package
	versionPath := filepath.Join(codegen.Gendir, "mcp_"+svcName, "protocol_version.go")
	versionImports := []*codegen.ImportSpec{}
	pv := data.ProtocolVersion
	if pv == "" {
		// Default to integration test expected version when none provided via DSL
		pv = "2025-06-18"
	}
	files = append(files, &codegen.File{
		Path: versionPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("MCP protocol version", pkgName, versionImports),
			{
				Name:   "mcp-protocol-version",
				Source: fmt.Sprintf("const DefaultProtocolVersion = %q\n", pv),
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
func generateMCPClientAdapter(genpkg string, svc *expr.ServiceExpr, data *AdapterData) []*codegen.File {
	files := make([]*codegen.File, 0, 1)

	svcName := codegen.SnakeCase(svc.Name)
	// Match the package alias used elsewhere (strip underscores)
	mcpPkgAlias := codegen.Goify("mcp_"+svcName, false)
	svcJSONRPCCAlias := svcName + "jsonrpcc"
	mcpJSONRPCCAlias := mcpPkgAlias + "jsonrpcc"

	// Extend data passed to template with aliases needed by imports
	type clientAdapterTemplateData struct {
		*AdapterData
		ServiceGoName    string
		ServicePkg       string
		MCPPkgAlias      string
		SvcJSONRPCCAlias string
		MCPJSONRPCCAlias string
		AllMethods       []string
	}

	allMethods := make([]string, len(svc.Methods))
	for i, m := range svc.Methods {
		allMethods[i] = codegen.Goify(m.Name, true)
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

	needsFmt := len(data.Tools) > 0 || len(data.Resources) > 0 || len(data.DynamicPrompts) > 0
	needsQueryURL := false
	for _, resource := range data.Resources {
		if resource.HasPayload {
			needsQueryURL = true
			break
		}
	}

	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "io"},
		{Path: "net/http"},
		{Path: "goa.design/goa/v3/http", Name: "goahttp"},
		{Path: genpkg + "/" + svcName, Name: svcName},
		// Import the MCP service package for types since we're now in a subpackage
		{Path: genpkg + "/mcp_" + svcName, Name: mcpPkgAlias},
		{Path: genpkg + "/jsonrpc/mcp_" + svcName + "/client", Name: mcpJSONRPCCAlias},
	}
	if needsFmt {
		imports = append(imports, &codegen.ImportSpec{Path: "fmt"})
	}
	if needsQueryURL {
		imports = append(imports, &codegen.ImportSpec{Path: "net/url"})
	}
	if data.NeedsQueryFormatting {
		imports = append(imports, &codegen.ImportSpec{Path: "strconv"})
	}
	if len(data.Tools) > 0 || len(data.DynamicPrompts) > 0 {
		imports = append(imports, &codegen.ImportSpec{
			Path: "goa.design/goa-ai/runtime/mcp/retry",
			Name: "retry",
		})
	}
	if data.NeedsOriginalClient {
		imports = append(imports,
			&codegen.ImportSpec{Path: "bytes"},
			&codegen.ImportSpec{Path: "goa.design/goa/v3/jsonrpc", Name: "jsonrpc"},
			&codegen.ImportSpec{Path: genpkg + "/jsonrpc/" + svcName + "/client", Name: svcJSONRPCCAlias},
		)
	} else if len(data.Notifications) > 0 {
		imports = append(imports, &codegen.ImportSpec{Path: "goa.design/goa/v3/jsonrpc", Name: "jsonrpc"})
	}
	if len(data.Notifications) > 0 {
		imports = append(imports,
			&codegen.ImportSpec{Path: "encoding/json"},
			&codegen.ImportSpec{Path: "github.com/google/uuid"},
		)
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
					"comment":        codegen.Comment,
					"goify":          func(s string) string { return codegen.Goify(s, true) },
					"queryValueExpr": resourceQueryValueExpr,
				},
			},
		},
	})

	return files
}

// resourceQueryValueExpr renders the direct Go expression that converts one
// primitive query value into the string form expected by url.Values.
func resourceQueryValueExpr(formatKind string, valueExpr string) string {
	switch formatKind {
	case resourceQueryFormatString:
		return valueExpr
	case resourceQueryFormatBool:
		return "strconv.FormatBool(" + valueExpr + ")"
	case resourceQueryFormatInt:
		return "strconv.FormatInt(int64(" + valueExpr + "), 10)"
	case resourceQueryFormatUint:
		return "strconv.FormatUint(uint64(" + valueExpr + "), 10)"
	case resourceQueryFormatFloat32:
		return "strconv.FormatFloat(float64(" + valueExpr + "), 'g', -1, 32)"
	case resourceQueryFormatFloat64:
		return "strconv.FormatFloat(" + valueExpr + ", 'g', -1, 64)"
	default:
		panic("unsupported resource query format kind: " + formatKind)
	}
}
