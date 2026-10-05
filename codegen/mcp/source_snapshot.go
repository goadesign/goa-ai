// This file retains authored JSON-RPC transport declarations before the MCP
// generator replaces them. Service pointers keep separate designs with matching
// service names from sharing URL paths or method bindings.

package codegen

import (
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type sourceSnapshot struct {
	transports map[*expr.ServiceExpr]*expr.HTTPServiceExpr
}

// collectSourceSnapshot retains each service's original transport from the
// supplied roots. Generated MCP services are not part of this snapshot.
func collectSourceSnapshot(roots []eval.Root) *sourceSnapshot {
	transports := make(map[*expr.ServiceExpr]*expr.HTTPServiceExpr)
	for _, root := range roots {
		r, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}
		for _, service := range r.API.JSONRPC.Services {
			transports[service.ServiceExpr] = service
		}
	}
	return &sourceSnapshot{transports: transports}
}
