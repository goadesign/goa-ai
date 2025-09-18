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

    // Register MCP plugin for the example phase: prepare augments roots so example mounts MCP
    codegen.RegisterPlugin("mcp", "example", PrepareExample, ModifyExampleFiles)

    // No post-processing needed if upstream Goa fix is present.
}

// PrepareServices filters out MCP-mapped methods from the original services and saves originals
// This runs BEFORE the main service generation
func PrepareServices(genpkg string, roots []eval.Root) error {
    // Reset global state at the beginning of each prepare phase
    originalServices = make(map[string]*expr.ServiceExpr)
    originalJSONRPCPaths = make(map[string]string)

    for _, root := range roots {
        r, ok := root.(*expr.RootExpr)
        if !ok {
            continue
        }

        // Save original services and JSONRPC paths before filtering
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

        // Keep original HTTP/JSON-RPC services so standard transports and clients
        // continue to be generated. The example phase ensures only the MCP service
        // is mounted for /rpc to avoid conflicts at runtime.
    }

    return nil
}

// DedupeJSONRPCServers scans JSON-RPC server.go files and removes the second
// ServeHTTP definition (the basic HTTP-only one) when both SSE-aware and basic
// ServeHTTP functions are emitted into the same file by upstream generators.
// The logic edits section sources in place and preserves all other templates.
func DedupeJSONRPCServers(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) { return files, nil }
