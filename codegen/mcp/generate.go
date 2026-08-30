// Package codegen writes adapters that connect generated MCP methods to the
// user service.
package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

const headerSection = "source-header"
const exampleMCPStubSection = "example-mcp-stub"

// applyMCPHTTPRulesToJSONRPCMount replaces each MCP server mount with the
// request checks required by the MCP Streamable HTTP transport.
func applyMCPHTTPRulesToJSONRPCMount(files []*codegen.File, services []*plannedMCPService) error {
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
	for _, f := range files {
		if f == nil {
			continue
		}
		service, ok := paths[filepath.ToSlash(f.Path)]
		if !ok {
			continue
		}
		header := findSection(f, headerSection)
		if header == nil {
			return fmt.Errorf("JSON-RPC server %q has no source header", f.Path)
		}
		codegen.AddImport(header, service.adapterData.jsonrpcServerImports.Imports()...)
		found := false
		for _, s := range f.SectionTemplates {
			if s == nil {
				continue
			}
			if s.Name == "jsonrpc-server-mount" {
				s.Source = mcpTemplates.Read("jsonrpc_server_mount")
				found = true
				break
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
		},
	})

	// Generate protocol version constant in MCP package
	versionPath := filepath.Join(codegen.Gendir, data.mcpPathName, "protocol_version.go")
	versionImports := []*codegen.ImportSpec{}
	files = append(files, &codegen.File{
		Path: versionPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("MCP protocol version", pkgName, versionImports),
			{
				Name:   "mcp-protocol-version",
				Source: fmt.Sprintf("const DefaultProtocolVersion = %q\n", data.ProtocolVersion),
			},
		},
	})
	return files
}
