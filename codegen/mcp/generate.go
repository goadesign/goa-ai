// Package codegen writes adapters that connect generated MCP methods to the
// user service.
package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// mcpTransportData adds tool headers to Goa's finalized transport names.
	mcpTransportData struct {
		Transport          any
		Tools              []*ToolAdapter
		SubscriptionSource *subscriptionAdapter
		ResourcePolicy     *resourcePolicy
	}
)

const headerSection = "source-header"
const exampleMCPStubSection = "example-mcp-stub"

// applyMCPHTTPRules installs the current MCP request checks in the generated
// server and client sections. Both server entry points use one checked handler.
func applyMCPHTTPRules(files []*codegen.File, services []*plannedMCPService) error {
	paths := make(map[string]*plannedMCPService, len(services))
	for _, service := range services {
		paths[filepath.ToSlash(filepath.Join(
			codegen.Gendir,
			"jsonrpc",
			service.adapterData.mcpPathName,
			"server",
			"server.go",
		))] = service
	}
	clients := make(map[string]*plannedMCPService, len(services))
	for _, service := range services {
		clients[filepath.ToSlash(filepath.Join(codegen.Gendir, "jsonrpc", service.adapterData.mcpPathName, "client", "client.go"))] = service
	}
	for _, f := range files {
		if f == nil {
			continue
		}
		if service, ok := clients[filepath.ToSlash(f.Path)]; ok {
			header := findHeaderSection(f)
			if header == nil {
				return fmt.Errorf("MCP client %q has no source header", f.Path)
			}
			codegen.AddImport(header, service.adapterData.jsonrpcClientImports.Imports()...)
			for _, section := range f.SectionTemplates {
				if section.Name != "jsonrpc-client-init" {
					continue
				}
				section.Source = mcpTemplates.Read("jsonrpc_client_init")
				section.Data = mcpTransportData{Transport: section.Data, Tools: service.adapterData.Tools}
			}
			continue
		}
		service, ok := paths[filepath.ToSlash(f.Path)]
		if !ok {
			continue
		}
		header := findHeaderSection(f)
		if header == nil {
			return fmt.Errorf("JSON-RPC server %q has no source header", f.Path)
		}
		codegen.AddImport(header, service.adapterData.jsonrpcServerImports.Imports()...)
		found := false
		for _, s := range f.SectionTemplates {
			if s == nil {
				continue
			}
			switch s.Name {
			case "jsonrpc-server-mount":
				s.Source = mcpTemplates.Read("jsonrpc_server_mount")
				s.Data = mcpTransportData{
					Transport:          s.Data,
					Tools:              service.adapterData.Tools,
					SubscriptionSource: service.adapterData.SubscriptionSource,
					ResourcePolicy:     service.adapterData.ResourcePolicy,
				}
				found = true
			case "jsonrpc-server-init":
				s.Source = mcpTemplates.Read("jsonrpc_server_init")
				s.Data = mcpTransportData{Transport: s.Data, ResourcePolicy: service.adapterData.ResourcePolicy}
			case "jsonrpc-server-struct":
				s.Source = mcpTemplates.Read("jsonrpc_server_struct")
			case "jsonrpc-server-use":
				s.Source = mcpTemplates.Read("jsonrpc_server_use")
			case "jsonrpc-server-handler":
				s.Source = mcpTemplates.Read("jsonrpc_server_handler")
			case "jsonrpc-server-encode-error":
				s.Source = mcpTemplates.Read("jsonrpc_server_encode_error")
			}
		}
		if !found {
			return fmt.Errorf("JSON-RPC server %q has no mount section", f.Path)
		}
		delete(paths, filepath.ToSlash(f.Path))
	}
	for filePath := range paths {
		return fmt.Errorf("goa did not generate MCP JSON-RPC server %q", filePath)
	}
	return nil
}

// generateMCPTransport generates files that adapt MCP protocol methods to the
// original service implementation.
func generateMCPTransport(_ string, svc *expr.ServiceExpr, data *AdapterData) []*codegen.File {
	files := make([]*codegen.File, 0, 1)

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
				Name:   "mcp-adapter-tools",
				Source: mcpTemplates.Read("catalog_page") + mcpTemplates.Read("adapter_tools"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:   "mcp-adapter-tasks",
				Source: mcpTemplates.Read("adapter_tasks"),
				Data:   data,
				FuncMap: map[string]any{
					"quote":          func(s string) string { return fmt.Sprintf("%q", s) },
					"taskOperations": taskOperations,
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
				Source: mcpTemplates.Read("catalog_page") + mcpTemplates.Read("adapter_prompts"),
				Data:   data,
				FuncMap: map[string]any{
					"comment": codegen.Comment,
					"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
				},
			},
			{
				Name:    "mcp-adapter-resource-subscription",
				Source:  mcpTemplates.Read("adapter_subscription_source"),
				Data:    data,
				FuncMap: map[string]any{"quote": func(s string) string { return fmt.Sprintf("%q", s) }},
			},
			{
				Name:    "mcp-adapter-completion",
				Source:  mcpTemplates.Read("adapter_completion"),
				Data:    data,
				FuncMap: map[string]any{"quote": func(s string) string { return fmt.Sprintf("%q", s) }},
			},
		},
	})

	return files
}
