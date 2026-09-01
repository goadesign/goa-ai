// This file copies the authored services and JSON-RPC routes before the MCP
// generator adds its service to the Goa design.

package codegen

import (
	"sort"

	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type sourceSnapshot struct {
	services     []*expr.ServiceExpr
	jsonrpcPaths map[string]string
}

// collectSourceSnapshot copies the authored services and JSON-RPC routes before
// the generator adds its MCP service. Later checks use this copy so they do not
// mistake generated services or routes for user declarations.
func collectSourceSnapshot(roots []eval.Root) *sourceSnapshot {
	serviceByName := make(map[string]*expr.ServiceExpr)
	jsonrpcPaths := make(map[string]string)

	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}
		for _, svc := range r.Services {
			serviceByName[svc.Name] = svc
		}
		if r.API == nil || r.API.JSONRPC == nil {
			continue
		}
		for _, service := range r.API.JSONRPC.Services {
			if service.ServiceExpr == nil || service.JSONRPCRoute == nil {
				continue
			}
			jsonrpcPaths[service.ServiceExpr.Name] = service.JSONRPCRoute.Path
		}
	}

	serviceNames := make([]string, 0, len(serviceByName))
	for name := range serviceByName {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	services := make([]*expr.ServiceExpr, 0, len(serviceNames))
	for _, name := range serviceNames {
		services = append(services, serviceByName[name])
	}

	return &sourceSnapshot{
		services:     services,
		jsonrpcPaths: jsonrpcPaths,
	}
}
