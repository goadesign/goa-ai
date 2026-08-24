package codegen

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// PrepareServices validates the full pure-MCP generation contract before
// filtering HTTP transport generation for MCP-enabled services. Callers may use
// Generate directly, but when PrepareServices is part of the pipeline it
// guarantees invalid MCP designs fail before any transport pruning happens.
func PrepareServices(_ string, roots []eval.Root) error {
	source := collectSourceSnapshot(roots)
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}

		for _, svc := range r.Services {
			if mcpexpr.Root != nil && mcpexpr.Root.HasMCP(svc) {
				if err := validatePureMCPService(svc, mcpexpr.Root.GetMCP(svc), source); err != nil {
					return err
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
