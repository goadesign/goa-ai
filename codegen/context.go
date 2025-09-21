package codegen

import (
	"sync"

	"goa.design/goa/v3/expr"
)

// genCtx holds per-run mutable state captured during prepare.
type genCtx struct {
	originalServices     map[string]*expr.ServiceExpr
	originalJSONRPCPaths map[string]string
}

var (
	ctxMu      sync.RWMutex
	currentCtx *genCtx
)

// ensureCtx initializes and returns the current generation context.
func ensureCtx() *genCtx {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	if currentCtx == nil {
		currentCtx = &genCtx{
			originalServices:     make(map[string]*expr.ServiceExpr),
			originalJSONRPCPaths: make(map[string]string),
		}
	}
	return currentCtx
}

// getCtx returns the current generation context, creating one if needed.
func getCtx() *genCtx {
	ctxMu.RLock()
	c := currentCtx
	ctxMu.RUnlock()
	if c != nil {
		return c
	}
	return ensureCtx()
}

// setOriginalService records the original service expression by name.
func setOriginalService(s *expr.ServiceExpr) {
	c := ensureCtx()
	c.originalServices[s.Name] = s
}

// setOriginalJSONRPCPath records the JSON-RPC path for a service.
func setOriginalJSONRPCPath(serviceName, path string) {
	c := ensureCtx()
	c.originalJSONRPCPaths[serviceName] = path
}

// getOriginalServices returns the map of original service expressions.
func getOriginalServices() map[string]*expr.ServiceExpr {
	return getCtx().originalServices
}

// getOriginalJSONRPCPath returns the JSON-RPC path for the named service.
func getOriginalJSONRPCPath(serviceName string) (string, bool) {
	c := getCtx()
	p, ok := c.originalJSONRPCPaths[serviceName]
	return p, ok
}
