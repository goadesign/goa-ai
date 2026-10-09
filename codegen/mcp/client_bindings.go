// Package codegen writes one private HTTP binding factory per MCP client.
// Both native client and runtime caller constructors use that factory, so Goa's
// credential mappings and tool behavior have one generated source of truth.
package codegen

import (
	"path/filepath"
	"slices"

	"goa.design/goa/v3/codegen"
)

// clientBindingsFile emits the finalized service's native query credentials and
// tool facts. Constructors receive fresh mappings without inspecting schemas
// or reconstructing authentication fields during a request.
func clientBindingsFile(data *AdapterData) *codegen.File {
	return &codegen.File{
		Path: filepath.Join(codegen.Gendir, "jsonrpc", data.mcpPathName, "client", "http_bindings.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("MCP HTTP bindings", "client", data.jsonrpcClientImports.Imports()),
			{Name: "mcp-http-bindings", Source: mcpTemplates.Read("mcp_http_bindings"), Data: data},
		},
	}
}

// credentialQueryBindings derives protocol method mappings from original Goa
// security inputs. It emits each mapped wire name once in deterministic order;
// ordinary domain query values are never treated as credentials.
func credentialQueryBindings(credentials map[string][]*credentialInput) map[string][]string {
	bindings := make(map[string][]string)
	for _, inputs := range credentials {
		for _, input := range inputs {
			if input.location != credentialQueryLocation {
				continue
			}
			for method := range input.sourceNames {
				bindings[method] = append(bindings[method], input.transportName)
			}
		}
	}
	for method, names := range bindings {
		slices.Sort(names)
		bindings[method] = slices.Compact(names)
	}
	return bindings
}
