package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/example"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	mcpexpr "goa.design/plugins/v3/mcp/expr"
)

// Example generates MCP code alongside the original service without modifying
// the original roots, then delegates example server/CLI generation to Goa.
func Example(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	// Generate MCP outputs side-by-side for services configured with MCP
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}
		for _, svc := range r.Services {
			if !mcpexpr.Root.HasMCP(svc) {
				continue
			}
			mcp := mcpexpr.Root.GetMCP(svc)
			builder := newMCPExprBuilder(svc, mcp)
			mcpService := builder.BuildServiceExpr()
			// Ensure JSON-RPC path is captured for this service so BuildRootExpr can create HTTP service
			if originalJSONRPCPaths == nil {
				originalJSONRPCPaths = make(map[string]string)
			}
			if _, exists := originalJSONRPCPaths[svc.Name]; !exists && r.API != nil && r.API.JSONRPC != nil {
				for _, jsonrpcSvc := range r.API.JSONRPC.Services {
					if jsonrpcSvc.ServiceExpr == nil || jsonrpcSvc.ServiceExpr.Name != svc.Name {
						continue
					}
					if route := jsonrpcSvc.JSONRPCRoute; route != nil && route.Path != "" {
						originalJSONRPCPaths[svc.Name] = route.Path
					}
					break
				}
			}
			mcpRoot := builder.BuildRootExpr(mcpService)

			if err := builder.PrepareAndValidate(mcpRoot); err != nil {
				return nil, err
			}

			// Generate MCP service and transport code without altering original root
			files = append(files, generateMCPServiceCode(genpkg, mcpRoot, mcpService)...)
			mapping := builder.BuildServiceMapping()
			files = append(files, generateMCPTransport(genpkg, svc, mcp, mapping)...)
		}
	}

	// Generate example server and CLI from the original, unmodified roots
	var exFiles []*codegen.File
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}
		servicesData := service.NewServicesData(r)
		exFiles = append(exFiles, example.ServerFiles(genpkg, r, servicesData)...)
		exFiles = append(exFiles, example.CLIFiles(genpkg, r)...)
	}

	return append(files, exFiles...), nil
}
