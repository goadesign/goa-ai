//nolint:lll // codegen uses long string templates and replacements for clarity
package codegen

import (
	"fmt"
	"path/filepath"
	"strings"

	mcpexpr "goa.design/goa-ai/expr"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	httpcodegen "goa.design/goa/v3/http/codegen"
	jsonrpccodegen "goa.design/goa/v3/jsonrpc/codegen"
)

const headerSection = "source-header"

// Generate orchestrates MCP code generation for services that declare MCP
// configuration in the DSL. It composes Goa service and JSON-RPC generators
// and adds adapter/client helpers.
func Generate(genpkg string, _ []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	// Process MCP services from original services
	for _, svc := range getOriginalServices() {
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

		// Build mapping and adapter data early so we can customize generated clients
		mapping := exprBuilder.BuildServiceMapping()
		adapterGen := newAdapterGenerator(genpkg, svc, mcp, mapping)
		adapterData := adapterGen.buildAdapterData()

		// Generate MCP service code using Goa's standard generators (with retry hooks)
		mcpFiles := generateMCPServiceCode(genpkg, mcpRoot, mcpService, adapterData)
		files = append(files, mcpFiles...)

		// Generate MCP transport that wraps the original service
		files = append(files, generateMCPTransport(genpkg, svc, mcp, mapping)...)

		// Generate MCP client adapter that wraps the MCP JSON-RPC client
		files = append(files, generateMCPClientAdapter(genpkg, svc, mcp, mapping)...)
	}

	// After all MCP files are generated, patch CLI support to instantiate MCP adapter client
	files = patchGeneratedCLISupportToUseMCPAdapter(files)

	return files, nil
}

// generateMCPServiceCode generates the MCP service layer and JSON-RPC transport
// using Goa's built-in generators.
func generateMCPServiceCode(genpkg string, root *expr.RootExpr, mcpService *expr.ServiceExpr, adapterData *AdapterData) []*codegen.File {
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
	// Patch base server files to inject header-driven policy into context
	patchMCPJSONRPCServerBaseFiles(mcpService, base)
	sse := jsonrpccodegen.SSEServerFiles(genpkg, httpServices)
	// Patch SSE server files to pass context to encoder and calls
	patchMCPJSONRPCServerSSEFiles(mcpService, sse)
	files = append(files, base...)
	files = append(files, sse...)
	files = append(files, jsonrpccodegen.ServerTypeFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.PathFiles(httpServices)...)
	// Add client-side JSON-RPC for MCP service so adapters can depend on it
	files = append(files, jsonrpccodegen.ClientTypeFiles(genpkg, httpServices)...)
	clientFiles := jsonrpccodegen.ClientFiles(genpkg, httpServices)
	// Patch client files to bake RetryableError helpers directly into client.go
	patchMCPJSONRPCClientFiles(genpkg, mcpService, adapterData, clientFiles)
	files = append(files, clientFiles...)

	return files
}

// patchMCPJSONRPCServerSSEFiles tweaks the generated SSE server stream to
// propagate request context to the encoder and event emission.
func patchMCPJSONRPCServerSSEFiles(mcpService *expr.ServiceExpr, sseFiles []*codegen.File) {
	// Locate jsonrpc/mcp_<svc>/server/stream.go
	svcNameAll := codegen.SnakeCase(mcpService.Name) // e.g., mcp_assistant
	baseSvc := strings.TrimPrefix(svcNameAll, "mcp_")
	target := filepath.Join(codegen.Gendir, "jsonrpc", "mcp_"+baseSvc, "server", "stream.go")
	for _, f := range sseFiles {
		if filepath.ToSlash(f.Path) != filepath.ToSlash(target) {
			continue
		}
		for _, s := range f.SectionTemplates {
			if strings.Contains(s.Source, "ToolsCallServerStream") && strings.Contains(s.Source, "sendSSEEvent(") {
				// Update helper signature to accept ctx
				s.Source = strings.ReplaceAll(s.Source,
					"func (s *ToolsCallServerStream) sendSSEEvent(eventType string, v any) error {",
					"func (s *ToolsCallServerStream) sendSSEEvent(ctx context.Context, eventType string, v any) error {")
				// Pass ctx to encoder
				s.Source = strings.ReplaceAll(s.Source,
					"s.encoder(context.Background(), ew).Encode(v)",
					"s.encoder(ctx, ew).Encode(v)")
				// Update call sites to pass ctx
				s.Source = strings.ReplaceAll(s.Source,
					"return s.sendSSEEvent(\"notification\", message)",
					"return s.sendSSEEvent(ctx, \"notification\", message)")
				s.Source = strings.ReplaceAll(s.Source,
					"return s.sendSSEEvent(\"response\", message)",
					"return s.sendSSEEvent(ctx, \"response\", message)")
				s.Source = strings.ReplaceAll(s.Source,
					"return s.sendSSEEvent(\"error\", response)",
					"return s.sendSSEEvent(ctx, \"error\", response)")
				// Improve SendError error code mapping where present
				if strings.Contains(s.Source, "func (s *ToolsCallServerStream) SendError(") {
					s.Source = strings.ReplaceAll(s.Source,
						"code := jsonrpc.InternalError\n\tif _, ok := err.(*goa.ServiceError); ok {\n\t\tcode = jsonrpc.InvalidParams\n\t}",
						"code := jsonrpc.InternalError\n\tvar en goa.GoaErrorNamer\n\tif errors.As(err, &en) {"+
							"\n\t\tswitch en.GoaErrorName() {\n\t\tcase \"invalid_params\": code = jsonrpc.InvalidParams"+
							"\n\t\tcase \"method_not_found\": code = jsonrpc.MethodNotFound\n\t\tdefault: code = jsonrpc.InternalError\n\t\t}\n\t}"+
							" else if _, ok := err.(*goa.ServiceError); ok {\n\t\tcode = jsonrpc.InvalidParams\n\t}")
				}
			}
		}
		// Ensure errors package is imported in the stream file header
		for _, s := range f.SectionTemplates {
			if s.Name == headerSection {
				codegen.AddImport(s, &codegen.ImportSpec{Path: "errors"})
				break
			}
		}
	}
}

// patchMCPJSONRPCServerBaseFiles injects header-based allow/deny names into the request context
// inside Server.processRequest so downstream services (adapter) can consume them.
func patchMCPJSONRPCServerBaseFiles(mcpService *expr.ServiceExpr, baseFiles []*codegen.File) {
	svcNameAll := codegen.SnakeCase(mcpService.Name)
	baseSvc := strings.TrimPrefix(svcNameAll, "mcp_")
	target := filepath.Join(codegen.Gendir, "jsonrpc", "mcp_"+baseSvc, "server", "server.go")
	for _, f := range baseFiles {
		if filepath.ToSlash(f.Path) != filepath.ToSlash(target) {
			continue
		}
		// Ensure we can add imports
		var header *codegen.SectionTemplate
		for _, s := range f.SectionTemplates {
			if s.Name == headerSection {
				header = s
				break
			}
		}
		if header != nil {
			codegen.AddImport(header, &codegen.ImportSpec{Path: "context"})
		}
		for _, s := range f.SectionTemplates {
			// Locate processRequest and inject right after function signature
			sig := "func (s *Server) processRequest(ctx context.Context, r *http.Request, req *jsonrpc.RawRequest, w http.ResponseWriter) {"
			if strings.Contains(s.Source, sig) {
				inj := sig + "\n\t// Inject MCP resource policy from headers into context" +
					"\n\tctx = context.WithValue(ctx, \"mcp_allow_names\", r.Header.Get(\"x-mcp-allow-names\"))" +
					"\n\tctx = context.WithValue(ctx, \"mcp_deny_names\", r.Header.Get(\"x-mcp-deny-names\"))"
				s.Source = strings.Replace(s.Source, sig, inj, 1)
			}
		}
	}
}

// patchMCPJSONRPCClientFiles mutates generated JSON-RPC client files to use retry support.
func patchMCPJSONRPCClientFiles(genpkg string, mcpService *expr.ServiceExpr, data *AdapterData, clientFiles []*codegen.File) {
	// mcpService.Name is already prefixed with "mcp_"; derive original service snake name
	svcNameAll := codegen.SnakeCase(mcpService.Name)  // e.g., "mcp_assistant"
	baseSvc := strings.TrimPrefix(svcNameAll, "mcp_") // e.g., "assistant"

	clientPath := filepath.Join(codegen.Gendir, "jsonrpc", "mcp_"+baseSvc, "client", "client.go")
	encodeDecodePath := filepath.Join(codegen.Gendir, "jsonrpc", "mcp_"+baseSvc, "client", "encode_decode.go")
	streamPath := filepath.Join(codegen.Gendir, "jsonrpc", "mcp_"+baseSvc, "client", "stream.go")
	for _, f := range clientFiles {
		if filepath.ToSlash(f.Path) != filepath.ToSlash(clientPath) {
			// Patch encode/decode to set Accept header for SSE tools/call
			if filepath.ToSlash(f.Path) == filepath.ToSlash(encodeDecodePath) {
				for _, s := range f.SectionTemplates {
					// Prefer patching BuildToolsCallRequest so header is always present
					if strings.Contains(s.Source, "func (c *Client) BuildToolsCallRequest(") {
						s.Source = strings.Replace(s.Source,
							"return req, nil",
							"req.Header.Set(\"Accept\", \"text/event-stream\")\n\treturn req, nil",
							1)
					}
					// Also patch EncodeToolsCallRequest to set Accept header
					if strings.Contains(s.Source, "func EncodeToolsCallRequest(") && strings.Contains(s.Source, "return func(req *http.Request, v any) error {") {
						s.Source = strings.Replace(s.Source,
							"return func(req *http.Request, v any) error {",
							"return func(req *http.Request, v any) error {\n\t\t// Request SSE stream for tools/call\n\t\treq.Header.Set(\"Accept\", \"text/event-stream\")",
							1)
					}
				}
				continue
			}
			// Patch client stream to satisfy interface and use typed errors
			if filepath.ToSlash(f.Path) == filepath.ToSlash(streamPath) {
				for _, s := range f.SectionTemplates {
					// Replace fmt.Errorf JSON-RPC error returns with typed JSONRPCError
					s.Source = strings.ReplaceAll(s.Source,
						"return zero, fmt.Errorf(\"JSON-RPC error %d: %s\", response.Error.Code, response.Error.Message)",
						"return zero, &JSONRPCError{Code: int(response.Error.Code), Message: response.Error.Message}")
					// Rename any ToolsCallClientStream Recv signature to RecvWithContext
					s.Source = strings.Replace(s.Source,
						"func (s *ToolsCallClientStream) Recv(ctx context.Context)",
						"func (s *ToolsCallClientStream) RecvWithContext(ctx context.Context)",
						1)
					s.Source = strings.ReplaceAll(s.Source,
						") Recv(ctx context.Context) (*",
						") RecvWithContext(ctx context.Context) (*")
					s.Source = strings.ReplaceAll(s.Source, " s.Recv(ctx)", " s.RecvWithContext(ctx)")
					// Insert ctx cancellation handling in receive loop
					s.Source = strings.Replace(s.Source,
						"for {\n\t\teventType, data, err := s.parseSSEEvent()",
						"for {\n\t\tselect {\n\t\tcase <-ctx.Done():\n\t\t\ts.closed = true\n\t\t\treturn zero, ctx.Err()\n\t\tdefault:\n\t\t}\n\t\teventType, data, err := s.parseSSEEvent()",
						1)
				}
				// Do not append extra wrappers; keep original Recv(ctx) signature
				continue
			}
			continue
		}

		// 1) Patch sections broadly (wrap stream, swap decoder) without relying on specific names
		for _, s := range f.SectionTemplates {
			// tools/call stream wrapper: look for method marker and the return of raw stream
			if strings.Contains(s.Source, `"tools/call"`) && strings.Contains(s.Source, "return stream, nil") {
				mcpAliasLocal := codegen.Goify("mcp_"+baseSvc, false)
				toolExtract := `\t\ttool := ""\n\t\tif p, ok := v.(*` + mcpAliasLocal + `.ToolsCallPayload); ok { tool = p.Name }\n\t\treturn c.wrapToolsCallStream(tool, stream), nil`
				s.Source = strings.ReplaceAll(s.Source, "\t\treturn stream, nil", toolExtract)
			}
			// prompts/get decoder swap to WithRetry variant
			if strings.Contains(s.Source, "DecodePromptsGetResponse(") {
				s.Source = strings.ReplaceAll(s.Source, "DecodePromptsGetResponse(", "DecodePromptsGetResponseWithRetry(")
			}
		}

		// 2) Ensure header imports include dependencies used by helper section using AddImport
		var hdr *codegen.SectionTemplate
		for _, s := range f.SectionTemplates {
			if s.Name == headerSection {
				hdr = s
				break
			}
		}
		mcpAlias := codegen.Goify("mcp_"+baseSvc, false)
		codegen.AddImport(
			hdr,
			&codegen.ImportSpec{Path: genpkg + "/mcp_" + baseSvc, Name: mcpAlias},
			&codegen.ImportSpec{Path: "goa.design/goa-ai/retry", Name: "retry"},
			&codegen.ImportSpec{Path: "errors"},
			&codegen.ImportSpec{Path: "encoding/json"},
			&codegen.ImportSpec{Path: "sync"},
		)

		// 3) Append helper section via template into client.go (no separate file)
		type clientRetryTemplateData struct {
			Tools          []*ToolAdapter
			MCPAlias       string
			MCPServiceName string
		}
		tdata := &clientRetryTemplateData{
			Tools:          data.Tools,
			MCPAlias:       codegen.Goify("mcp_"+baseSvc, false),
			MCPServiceName: mcpService.Name,
		}
		f.SectionTemplates = append(f.SectionTemplates, &codegen.SectionTemplate{
			Name:   "mcp-jsonrpc-client-retry-baked",
			Source: mcpTemplates.Read("client_retry_helpers"),
			Data:   tdata,
		})
	}
}

// patchGeneratedCLISupportToUseMCPAdapter modifies gen/jsonrpc/cli/<svc>/cli.go to import
// the MCP adapter client and instantiate it instead of the original service client.
func patchGeneratedCLISupportToUseMCPAdapter(files []*codegen.File) []*codegen.File {
	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		if !strings.Contains(p, "/jsonrpc/cli/") || !strings.HasSuffix(p, "/cli.go") {
			continue
		}
		// Extract service name from path
		svc := ""
		if idx := strings.Index(p, "/jsonrpc/cli/"); idx >= 0 {
			rest := p[idx+len("/jsonrpc/cli/"):]
			if j := strings.Index(rest, "/"); j > 0 {
				svc = rest[:j]
			}
		}
		if svc == "" {
			continue
		}
		// Locate header to add adapter import and capture original alias
		var header *codegen.SectionTemplate
		var origAlias string
		for _, s := range f.SectionTemplates {
			if s.Name != headerSection {
				continue
			}
			header = s
			if data, ok := header.Data.(map[string]any); ok {
				if imv, ok2 := data["Imports"]; ok2 {
					if specs, ok3 := imv.([]*codegen.ImportSpec); ok3 {
						for _, spec := range specs {
							if strings.HasSuffix(spec.Path, "/gen/jsonrpc/"+svc+"/client") {
								base := strings.TrimSuffix(spec.Path, "/gen/jsonrpc/"+svc+"/client")
								adapterPath := base + "/gen/mcp_" + svc + "/adapter/client"
								codegen.AddImport(header, &codegen.ImportSpec{Path: adapterPath, Name: "mcpac"})
								origAlias = spec.Name
								break
							}
						}
					}
				}
			}
			break
		}
		if origAlias == "" {
			origAlias = svc + "c"
		}
		// Swap constructor in parse-endpoint section; if not found, fall back to all non-header sections
		var replaced bool
		for _, s := range f.SectionTemplates {
			if s.Name != "parse-endpoint" {
				continue
			}
			// Replace by alias-specific and generic forms
			before := "" + origAlias + ".NewClient("
			if strings.Contains(s.Source, before) {
				s.Source = strings.ReplaceAll(s.Source, before, "mcpac.NewClient(")
				replaced = true
			}
			// Also remove assignment to the original client variable if present (e.g., c := <alias>.NewClient)
			if strings.Contains(s.Source, "c := "+origAlias+".NewClient(") {
				s.Source = strings.ReplaceAll(s.Source, "c := "+origAlias+".NewClient(", "c := mcpac.NewClient(")
				replaced = true
			}
		}
		if !replaced {
			for _, s := range f.SectionTemplates {
				if s.Name == headerSection {
					continue
				}
				s.Source = strings.ReplaceAll(s.Source, origAlias+".NewClient(", "mcpac.NewClient(")
			}
		}
	}
	return files
}

// generateMCPTransport generates adapter and prompt provider files that adapt
// MCP protocol methods to the original service implementation.
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
		{Path: "path"},
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
func generateMCPClientAdapter(genpkg string, svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, mapping *ServiceMethodMapping) []*codegen.File {
	var files []*codegen.File

	svcName := codegen.SnakeCase(svc.Name)
	// Match the package alias used elsewhere (strip underscores)
	mcpPkgAlias := codegen.Goify("mcp_"+svcName, false)
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
		{Path: "encoding/json"},
		{Path: "net/url"},
		{Path: "sort"},
		{Path: "strings"},
		{Path: "goa.design/goa/v3/http", Name: "goahttp"},
		{Path: "goa.design/goa/v3/jsonrpc", Name: "jsonrpc"},
		{Path: "goa.design/goa-ai/retry", Name: "retry"},
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
