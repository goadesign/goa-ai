// Package dsl binds resource update subscriptions to an authored Goa stream.
// The service selects authorized URIs; generated code sends their updates on
// the originating MCP request without exposing transport identifiers.
package dsl

import (
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// ResourceSubscription selects the current server-streaming method as the
// resource update source for this MCP service. Its payload contains only an
// optional resources array of URI strings, apart from annotated credentials.
// Each array element declares FormatURI.
// Its StreamingResult contains one required change OneOf with acknowledged and
// updated object branches. acknowledged contains only an optional resources
// array; updated contains only a required uri string with FormatURI.
// The service first sends the authorized subset in acknowledged, then sends
// updated values until it returns or the request is canceled. An empty accepted
// subset is valid. URI interpretation, access checks, and change detection belong
// to the service. Fixed catalogs do not gain list-change notifications.
func ResourceSubscription() {
	method, ok := eval.Current().(*expr.MethodExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	server := exprmcp.Root.GetMCP(method.Service)
	if server == nil {
		eval.IncompatibleDSL()
		return
	}
	if server.ResourceSubscription != nil {
		eval.ReportError("an MCP service can declare only one resource subscription source")
		return
	}
	server.ResourceSubscription = &exprmcp.ResourceSubscriptionExpr{Method: method}
}
