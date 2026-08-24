package codegen

import (
	"sort"

	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type sourceSnapshot struct {
	services      []*expr.ServiceExpr
	jsonrpcRoutes map[string]sourceJSONRPCRoute
}

type sourceJSONRPCRoute struct {
	method string
	path   string
}

// collectSourceSnapshot captures the original services and JSON-RPC routes from
// the current Goa roots. The snapshot is immutable per invocation so generation
// stays deterministic and reentrant while preserving the source transport
// contract for validation.
func collectSourceSnapshot(roots []eval.Root) *sourceSnapshot {
	serviceByName := make(map[string]*expr.ServiceExpr)
	jsonrpcRoutes := make(map[string]sourceJSONRPCRoute)

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
			jsonrpcRoutes[service.ServiceExpr.Name] = sourceJSONRPCRoute{
				method: service.JSONRPCRoute.Method,
				path:   service.JSONRPCRoute.Path,
			}
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
		services:      services,
		jsonrpcRoutes: jsonrpcRoutes,
	}
}

func (s *sourceSnapshot) jsonrpcRoute(serviceName string) (sourceJSONRPCRoute, bool) {
	route, ok := s.jsonrpcRoutes[serviceName]
	return route, ok
}

func (s *sourceSnapshot) jsonrpcPath(serviceName string) (string, bool) {
	route, ok := s.jsonrpcRoute(serviceName)
	if !ok || route.path == "" {
		return "", false
	}
	return route.path, true
}
