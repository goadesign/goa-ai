// This file writes the adapters that connect generated MCP methods to the
// user service and the MCP JSON-RPC client.
package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

const headerSection = "source-header"
const exampleMCPStubSection = "example-mcp-stub"

// applyMCPPolicyHeadersToJSONRPCMount replaces the JSON-RPC server mount section
// for each generated MCP service so policy headers reach its adapter.
func applyMCPPolicyHeadersToJSONRPCMount(files []*codegen.File, services []*plannedMCPService) {
	paths := make(map[string]struct{}, len(services))
	for _, service := range services {
		paths[filepath.ToSlash(filepath.Join(
			codegen.Gendir,
			"jsonrpc",
			service.adapterData.mcpPathName,
			"server",
			"server.go",
		))] = struct{}{}
	}
	for _, f := range files {
		if f == nil {
			continue
		}
		if _, ok := paths[filepath.ToSlash(f.Path)]; !ok {
			continue
		}
		for _, s := range f.SectionTemplates {
			if s == nil {
				continue
			}
			if s.Name == "jsonrpc-server-mount" {
				s.Source = mcpTemplates.Read("jsonrpc_server_mount")
				continue
			}
		}
	}
}

// generateMCPTransport generates adapter and prompt provider files that adapt
// MCP protocol methods to the original service implementation.
func generateMCPTransport(_ string, svc *expr.ServiceExpr, data *AdapterData) []*codegen.File {
	var files []*codegen.File

	// Write the server adapter in the generated MCP service package.
	adapterPath := filepath.Join(codegen.Gendir, data.mcpPathName, "adapter_server.go")
	pkgName := data.MCPPackage

	files = append(files, &codegen.File{
		Path: adapterPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(fmt.Sprintf("MCP server adapter for %s service", svc.Name), pkgName, data.serverImports),
			{
				Name:   "mcp-adapter-core",
				Source: mcpTemplates.Read("adapter_core"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-broadcast",
				Source: mcpTemplates.Read("adapter_broadcast"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-tools",
				Source: mcpTemplates.Read("adapter_tools"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-resources",
				Source: mcpTemplates.Read("adapter_resources"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-prompts",
				Source: mcpTemplates.Read("adapter_prompts"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-notifications",
				Source: mcpTemplates.Read("adapter_notifications"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
		},
	})

	// Generate protocol version constant in MCP package
	versionPath := filepath.Join(codegen.Gendir, data.mcpPathName, "protocol_version.go")
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
		providerPath := filepath.Join(codegen.Gendir, data.mcpPathName, "prompt_provider.go")
		files = append(files, &codegen.File{
			Path: providerPath,
			SectionTemplates: []*codegen.SectionTemplate{
				codegen.Header(fmt.Sprintf("MCP prompt provider for %s service", svc.Name), pkgName, data.promptProviderImports),
				{
					Name:   "mcp-prompt-provider",
					Source: mcpTemplates.Read("prompt_provider"),
					Data:   data,
				},
			},
		})
	}

	return files
}

// generateMCPClientAdapter generates a client adapter that exposes the original
// service endpoints while calling MCP JSON-RPC methods under the hood.
func generateMCPClientAdapter(_ string, svc *expr.ServiceExpr, data *AdapterData) []*codegen.File {
	files := make([]*codegen.File, 0, 1)

	// Extend data passed to template with aliases needed by imports
	type clientAdapterTemplateData struct {
		*AdapterData
		ServicePkg       string
		MCPPkgAlias      string
		MCPJSONRPCCAlias string
		CodecPackage     string
		AllMethods       []string
	}

	tdata := &clientAdapterTemplateData{
		AdapterData:      data,
		ServicePkg:       data.clientServicePackage,
		MCPPkgAlias:      data.clientMCPPackage,
		MCPJSONRPCCAlias: data.clientJSONRPCPackage,
		CodecPackage:     data.clientCodecPackage,
		AllMethods:       data.clientMethodNames,
	}

	// Put client adapter in a separate subpackage to avoid import cycle
	adapterPkgName := data.clientPackageName
	files = append(files, &codegen.File{
		Path: filepath.Join(codegen.Gendir, data.mcpPathName, "adapter", "client", "adapter.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("MCP client adapter exposing original service endpoints", adapterPkgName, data.clientImports),
			{
				Name:   "mcp-client-adapter",
				Source: mcpTemplates.Read("mcp_client_wrapper"),
				Data:   tdata,
				FuncMap: map[string]any{
					"comment":        codegen.Comment,
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
		return "string(" + valueExpr + ")"
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
