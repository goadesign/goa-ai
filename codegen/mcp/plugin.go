package codegen

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// PrepareServices filters out MCP-mapped methods from the original services and saves originals
// This runs BEFORE the main service generation
// PrepareServices walks the provided roots, records the original services and
// their JSON-RPC paths into a per-run context. It does not mutate the roots.
func PrepareServices(_ string, roots []eval.Root) error {
	// Initialize per-run context
	_ = ensureCtx()

	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}

		// Save original services and JSONRPC paths before filtering
		for _, svc := range r.Services {
			setOriginalService(svc)

			if r.API == nil || r.API.JSONRPC == nil {
				continue
			}

			for _, httpSvc := range r.API.JSONRPC.Services {
				if httpSvc.ServiceExpr != nil && httpSvc.ServiceExpr.Name == svc.Name {
					// Get the service-level JSON-RPC route (this is what all endpoints will use)
					if httpSvc.JSONRPCRoute != nil && httpSvc.JSONRPCRoute.Path != "" {
						setOriginalJSONRPCPath(svc.Name, httpSvc.JSONRPCRoute.Path)
					}
					break
				}
			}
		}

		// Filter out HTTP transport for MCP-enabled services to avoid
		// generating conflicting HTTP SSE code. Keep JSON-RPC so the
		// service interface remains JSON-RPC SSE-aware where applicable.
		if r.API != nil && r.API.HTTP != nil {
			filtered := make([]*expr.HTTPServiceExpr, 0, len(r.API.HTTP.Services))
			for _, hs := range r.API.HTTP.Services {
				if hs.ServiceExpr != nil && mcpexpr.Root != nil && mcpexpr.Root.HasMCP(hs.ServiceExpr) {
					// Skip HTTP generation for MCP-enabled service
					continue
				}
				filtered = append(filtered, hs)
			}
			r.API.HTTP.Services = filtered
		}
	}

	return nil
}
