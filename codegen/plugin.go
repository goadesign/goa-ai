package codegen

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// init registers the plugin generator
func init() {
	// Register MCP plugin with PrepareServices and Generate for the gen phase
	codegen.RegisterPluginFirst("mcp", "gen", PrepareServices, Generate)

	// Register MCP plugin for the example phase: prepare augments roots so example mounts MCP
	codegen.RegisterPlugin("mcp", "example", PrepareExample, ModifyExampleFiles)
}

// PrepareServices filters out MCP-mapped methods from the original services and saves originals
// This runs BEFORE the main service generation
// PrepareServices walks the provided roots, records the original services and
// their JSON-RPC paths into a per-run context. It does not mutate the roots.
func PrepareServices(genpkg string, roots []eval.Root) error {
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
	}

	return nil
}

// DedupeJSONRPCServers scans JSON-RPC server files and removes duplicate
// handlers when upstream generators emit both SSE-aware and basic versions.
// Currently a no-op unless future fallback is implemented.
func DedupeJSONRPCServers(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	return files, nil
}
