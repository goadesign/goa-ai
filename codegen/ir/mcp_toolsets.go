// Package ir gives every Goa MCP server a service-owned tool contract and caller
// helper. Agent references may reuse that contract; no agent is required to
// generate or register an executable remote binding.
package ir

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	agentexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
)

// addMCPToolsets derives contracts from the evaluated MCP root without adding
// synthetic declarations to the user's DSL. Existing definitions are reused
// only when they identify this exact service and MCP name.
func addMCPToolsets(genpkg string, roots []eval.Root, services map[string]*Service, toolsets []*Toolset, exports []*ToolsetRef) ([]*Toolset, []*ToolsetRef, error) {
	for _, root := range roots {
		mcp, ok := root.(*mcpexpr.RootExpr)
		if !ok {
			continue
		}
		names := make([]string, 0, len(mcp.MCPServers))
		for name := range mcp.MCPServers {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			server := mcp.MCPServers[name]
			if len(server.Tools) == 0 {
				continue
			}
			service := services[name]
			if service == nil {
				return nil, nil, fmt.Errorf("MCP service %q has no generated service", name)
			}
			var definition *Toolset
			for _, existing := range toolsets {
				provider := existing.Expr.Provider
				if existing.Name == server.Name && provider != nil && provider.Kind == agentexpr.ProviderMCP && provider.MCPService == name && provider.MCPToolset == server.Name && provider.MCPSource == agentexpr.MCPSourceGoa {
					definition = existing
					break
				}
			}
			if definition == nil {
				expression := &agentexpr.ToolsetExpr{Name: server.Name, Description: server.Description, Provider: &agentexpr.ProviderExpr{Kind: agentexpr.ProviderMCP, MCPSource: agentexpr.MCPSourceGoa, MCPService: name, MCPToolset: server.Name}}
				var err error
				definition, err = newToolset(genpkg, expression, Owner{Kind: OwnerKindService, ServiceName: name, ServicePathName: service.PathName})
				if err != nil {
					return nil, nil, err
				}
				for _, existing := range toolsets {
					if existing.SpecsDir == definition.SpecsDir {
						return nil, nil, fmt.Errorf("MCP contract %q collides with toolset %q at %q", name+"."+server.Name, existing.Name, definition.SpecsDir)
					}
				}
				toolsets = append(toolsets, definition)
			}
			reference := &ToolsetRef{Expr: definition.Expr, Definition: definition, Kind: ToolsetRefKindServiceExport, Name: server.Name, Slug: definition.Slug, QualifiedName: name + "." + server.Name, Description: server.Description, Service: service, ServiceName: name, SourceService: service, SourceServiceName: name, SpecsPackageName: definition.SpecsPackageName, SpecsImportPath: definition.SpecsImportPath, SpecsDir: definition.SpecsDir, PackageName: "mcp", PackageImportPath: path.Join(definition.SpecsImportPath, "mcp"), Dir: filepath.Join(definition.SpecsDir, "mcp")}
			reference.Provider = buildToolsetProvider(genpkg, service, nil, reference, definition.Expr)
			if definition.Owner.Ref == nil {
				definition.Owner.Ref = reference
			}
			exports = append(exports, reference)
		}
	}
	slices.SortFunc(toolsets, func(a, b *Toolset) int { return strings.Compare(a.SpecsDir, b.SpecsDir) })
	return toolsets, exports, nil
}
