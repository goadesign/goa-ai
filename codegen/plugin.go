package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// originalServices stores the original services before filtering
// This is needed because pure MCP services are removed from the root
// but we still need to generate MCP code for them
var originalServices map[string]*expr.ServiceExpr

// originalJSONRPCPaths stores the original JSONRPC paths for each service
// This is needed because the paths are defined in the design but may be
// modified during filtering
var originalJSONRPCPaths map[string]string

// init registers the plugin generator
func init() {
	// Register MCP plugin with PrepareServices and Generate for the gen phase
	codegen.RegisterPluginFirst("mcp", "gen", PrepareServices, Generate)

	// Register MCP plugin for the example phase
	codegen.RegisterPlugin("mcp", "example", nil, Example)
}

// PrepareServices filters out MCP-mapped methods from the original services and saves originals
// This runs BEFORE the main service generation
func PrepareServices(genpkg string, roots []eval.Root) error {
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}

		// Save original services and JSONRPC paths before filtering
		if originalServices == nil {
			originalServices = make(map[string]*expr.ServiceExpr)
		}
		if originalJSONRPCPaths == nil {
			originalJSONRPCPaths = make(map[string]string)
		}
		for _, svc := range r.Services {
			originalServices[svc.Name] = svc

			if r.API == nil || r.API.JSONRPC == nil {
				continue
			}

			for _, httpSvc := range r.API.JSONRPC.Services {
				if httpSvc.ServiceExpr != nil && httpSvc.ServiceExpr.Name == svc.Name {
					// Get the service-level JSON-RPC route (this is what all endpoints will use)
					if httpSvc.JSONRPCRoute != nil && httpSvc.JSONRPCRoute.Path != "" {
						originalJSONRPCPaths[svc.Name] = httpSvc.JSONRPCRoute.Path
					}
					break
				}
			}
		}
	}

	return nil
}
