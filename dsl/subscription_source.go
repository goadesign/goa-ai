// Package dsl binds resource and task subscriptions to an authored Goa stream.
// The service authorizes native URIs and job identities; generated code sends
// their changes on the originating MCP request without exposing correlation IDs.
package dsl

import (
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// SubscriptionSource selects the current server-streaming method as the change
// source for this MCP service. Its payload has optional resources and/or tasks,
// apart from native credentials and URL fields. resources is an array whose URI
// elements declare FormatURI. tasks is an object of optional job-ID arrays named
// after the service's TaskExchange creator methods exposed as MCP tools.
// StreamingResult contains one required change OneOf. acknowledged contains the
// same selection fields as the payload. updated contains a required URI when
// resources are selected. tasks_updated contains the required tasks selection
// object when tasks are selected. The service acknowledges the authorized subset
// first, then identifies changed resources or native jobs. Generated code reads
// each changed job through its configured observation endpoint and sends its
// full state. The shared transport owns correlation, ordering and closure.
// Fixed catalogs do not gain list-change notifications.
func SubscriptionSource() {
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
	if server.SubscriptionSource != nil {
		eval.ReportError("an MCP service can declare only one subscription source")
		return
	}
	server.SubscriptionSource = &exprmcp.SubscriptionSourceExpr{Method: method}
}
